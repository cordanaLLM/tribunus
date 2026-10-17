package eventlog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tekmora/epitrop/internal/gitexec"
)

// GitRecord says where RecordInGit commits the log and who commits it.
type GitRecord struct {
	// Ref is the ref the commits go to, under refs/. Nothing checks it out.
	Ref string
	// AuthorName and AuthorEmail are the control plane's own identity, author and committer.
	AuthorName  string
	AuthorEmail string
	// Timeout bounds every git command of one RecordInGit together.
	Timeout time.Duration
}

// gitRun is how RecordInGit runs git; a test replaces it to make one step fail or race.
var gitRun = gitexec.Run

// RecordInGit commits the log's events/ and checkpoints/ as they are onto g.Ref of the
// repository the log directory lies in, and returns that commit. When the ref already holds
// the same files it makes no commit and returns the ref's commit; a log without events/
// records nothing and returns "".
//
// The files are read under the log lock, byte for byte (no clean filter, no line-ending
// conversion), so the commit holds one consistent log. Temporary files and the lock file,
// whose names start with a dot, stay out. The commit is built with git's plumbing and a
// private index and moves the ref only when the ref still names the commit it was built on:
// the operator's index, working tree and checked-out branch are never touched, and a failure
// leaves the log and the ref as they were, with at most objects no ref names. Git does not
// sign the commit; every record in it carries its own receipt.
func (w *Writer) RecordInGit(ctx context.Context, g GitRecord) (commit string, err error) {
	if err = readyContext(ctx, "git record"); err != nil {
		return "", err
	}
	if err = g.validate(); err != nil {
		return "", err
	}
	if _, statErr := os.Stat(w.eventsDir); errors.Is(statErr, os.ErrNotExist) {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, g.Timeout)
	defer cancel()
	repo, err := openGitRepo(ctx, w.dir, g, w.limits.Clock.Now())
	if err != nil {
		return "", err
	}
	defer func() {
		if rmErr := os.RemoveAll(repo.indexDir); rmErr != nil {
			err = errors.Join(err, fmt.Errorf("eventlog: git record: remove index: %w", rmErr))
		}
	}()
	seq, err := w.stageInGit(ctx, repo)
	if err != nil {
		return "", err
	}
	return repo.commit(ctx, g.Ref, seq)
}

func (g GitRecord) validate() error {
	if !strings.HasPrefix(g.Ref, "refs/") {
		return fmt.Errorf("eventlog: git record: ref %q is not under refs/", g.Ref)
	}
	if !plainIdentity(g.AuthorName) || !plainIdentity(g.AuthorEmail) {
		return fmt.Errorf("eventlog: git record: author %q <%q> must be set, without angle brackets or control characters", g.AuthorName, g.AuthorEmail)
	}
	if g.Timeout <= 0 {
		return errors.New("eventlog: git record: timeout must be positive")
	}
	return nil
}

func plainIdentity(s string) bool {
	return strings.TrimSpace(s) != "" && !strings.ContainsAny(s, "<>") && !hasControl(s)
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

// gitRepo is one RecordInGit's view of the repository: the log directory as the working
// tree, a private index in a temporary directory, and the identity and time of the commit.
type gitRepo struct {
	dir      string
	env      []string
	indexDir string
}

func openGitRepo(ctx context.Context, dir string, g GitRecord, now time.Time) (gitRepo, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return gitRepo{}, fmt.Errorf("eventlog: git record: %w", err)
	}
	base := gitexec.Env(os.Environ())
	if _, err = gitRun(ctx, abs, base, nil, "check-ref-format", g.Ref); err != nil {
		return gitRepo{}, fmt.Errorf("eventlog: git record: ref %q: %w", g.Ref, err)
	}
	gitDir, err := gitRun(ctx, abs, base, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return gitRepo{}, fmt.Errorf("eventlog: git record: %s is not in a git repository: %w", abs, err)
	}
	indexDir, err := os.MkdirTemp("", "epitrop-git-record-*")
	if err != nil {
		return gitRepo{}, fmt.Errorf("eventlog: git record: index: %w", err)
	}
	date := fmt.Sprintf("@%d +0000", now.Unix())
	env := gitexec.Env(os.Environ(),
		"GIT_DIR="+gitDir, "GIT_WORK_TREE="+abs, "GIT_INDEX_FILE="+filepath.Join(indexDir, "index"),
		"GIT_AUTHOR_NAME="+g.AuthorName, "GIT_AUTHOR_EMAIL="+g.AuthorEmail, "GIT_AUTHOR_DATE="+date,
		"GIT_COMMITTER_NAME="+g.AuthorName, "GIT_COMMITTER_EMAIL="+g.AuthorEmail, "GIT_COMMITTER_DATE="+date)
	return gitRepo{dir: abs, env: env, indexDir: indexDir}, nil
}

func (r gitRepo) git(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	out, err := gitRun(ctx, r.dir, r.env, stdin, args...)
	if err != nil {
		return "", fmt.Errorf("eventlog: git record: git %s: %w", args[0], err)
	}
	return out, nil
}

// stageInGit reads the log's files into the private index under the log lock, and returns
// the seq HEAD names.
func (w *Writer) stageInGit(ctx context.Context, repo gitRepo) (seq uint64, err error) {
	lock, err := acquireLock(ctx, w.eventsDir, w.limits.LockTimeout)
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	head, err := w.currentHead()
	if err != nil {
		return 0, err
	}
	files, err := logFiles(repo.dir)
	if err != nil {
		return 0, err
	}
	var paths strings.Builder
	for i := 0; i < len(files); i++ {
		paths.WriteString(filepath.Join(repo.dir, filepath.FromSlash(files[i])) + "\n")
	}
	out, err := repo.git(ctx, strings.NewReader(paths.String()), "hash-object", "-w", "--no-filters", "--stdin-paths")
	if err != nil {
		return 0, err
	}
	oids := strings.Fields(out)
	if len(oids) != len(files) {
		return 0, fmt.Errorf("eventlog: git record: hash-object returned %d ids for %d files", len(oids), len(files))
	}
	var info strings.Builder
	for i := 0; i < len(files); i++ {
		info.WriteString("100644 " + oids[i] + "\t" + files[i] + "\n")
	}
	if _, err = repo.git(ctx, strings.NewReader(info.String()), "update-index", "--add", "--index-info"); err != nil {
		return 0, err
	}
	return head.Seq, nil
}

// commit writes the staged tree as a commit on ref unless ref already holds that tree, and
// moves ref only when it still names the commit the new one follows.
func (r gitRepo) commit(ctx context.Context, ref string, seq uint64) (string, error) {
	tree, err := r.git(ctx, nil, "write-tree")
	if err != nil {
		return "", err
	}
	parent, err := r.refCommit(ctx, ref)
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", tree, "-m", fmt.Sprintf("epitrop: event log through seq %d", seq)}
	if parent != "" {
		parentTree, treeErr := r.git(ctx, nil, "rev-parse", "--verify", parent+"^{tree}")
		if treeErr != nil {
			return "", treeErr
		}
		if parentTree == tree {
			return parent, nil
		}
		args = append(args, "-p", parent)
	}
	commit, err := r.git(ctx, nil, args...)
	if err != nil {
		return "", err
	}
	// An empty old value means the ref must not exist yet; either way git refuses the
	// update when another writer moved the ref meanwhile.
	if _, err = r.git(ctx, nil, "update-ref", "-m", "epitrop: event log", ref, commit, parent); err != nil {
		return "", err
	}
	return commit, nil
}

// refCommit returns the commit ref names, or "" when ref does not exist.
func (r gitRepo) refCommit(ctx context.Context, ref string) (string, error) {
	out, err := gitRun(ctx, r.dir, r.env, nil, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("eventlog: git record: read %s: %w", ref, err)
	}
	return out, nil
}

// logFiles lists the files RecordInGit commits, as slash paths relative to the log
// directory: events/*, events/segments/* and checkpoints/<name>/*. Names that start with a
// dot (the lock, temporary files) and anything but a regular file stay out.
func logFiles(dir string) ([]string, error) {
	groups := []string{"events", "events/segments"}
	names, err := regularNames(filepath.Join(dir, "checkpoints"), true)
	if err != nil {
		return nil, err
	}
	for i := 0; i < len(names); i++ {
		groups = append(groups, "checkpoints/"+names[i])
	}
	var files []string
	for i := 0; i < len(groups); i++ {
		names, err = regularNames(filepath.Join(dir, filepath.FromSlash(groups[i])), false)
		if err != nil {
			return nil, err
		}
		for j := 0; j < len(names); j++ {
			files = append(files, groups[i]+"/"+names[j])
		}
	}
	return files, nil
}

// regularNames lists the names in dir that do not start with a dot: directories when dirs
// is set, regular files otherwise. A missing dir has none.
func regularNames(dir string, dirs bool) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("eventlog: git record: %w", err)
	}
	var names []string
	for i := 0; i < len(entries); i++ {
		name := entries[i].Name()
		if strings.HasPrefix(name, ".") || (dirs && !entries[i].IsDir()) || (!dirs && !entries[i].Type().IsRegular()) {
			continue
		}
		if hasControl(name) {
			return nil, fmt.Errorf("eventlog: git record: %q in %s holds a control character", name, dir)
		}
		names = append(names, name)
	}
	return names, nil
}
