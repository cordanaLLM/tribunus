package eventlog

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tekmora/epitrop/internal/gitexec"
)

const testRef = "refs/epitrop/events"

func testGitRecord() GitRecord {
	return GitRecord{Ref: testRef, AuthorName: "epitrop test", AuthorEmail: "epitrop@example.invalid", Timeout: 30 * time.Second}
}

// gitAt runs git in dir as an operator would, without the machine's own settings.
func gitAt(t *testing.T, dir string, args ...string) string {
	t.Helper()
	env := gitexec.Env(os.Environ(), "GIT_AUTHOR_NAME=operator", "GIT_AUTHOR_EMAIL=operator@example.invalid",
		"GIT_COMMITTER_NAME=operator", "GIT_COMMITTER_EMAIL=operator@example.invalid")
	out, err := gitexec.Run(testContext(t), dir, env, nil, args...)
	if err != nil {
		t.Fatalf("git %v = %v", args, err)
	}
	return out
}

// gitLog makes an operator's repository with one commit, a staged file and an untracked one,
// and a log in its .epitrop directory: 11 records in segments of 3 (1-3, 4-6, 7-9, then 10
// and 11 as files), a checkpoint at 10, a temporary file in events/ and in segments/, and a
// key file beside events/. It returns the repository and the log's writer.
func gitLog(t *testing.T) (string, string, *Writer) {
	t.Helper()
	withSegmentRecords(t, 3)
	repo := t.TempDir()
	gitAt(t, repo, "init", "--quiet", "--initial-branch=main")
	plant(t, filepath.Join(repo, "README.md"), "operator's file\n")
	gitAt(t, repo, "add", "README.md")
	gitAt(t, repo, "commit", "--quiet", "-m", "operator's commit")
	plant(t, filepath.Join(repo, "staged.txt"), "staged\n")
	gitAt(t, repo, "add", "staged.txt")
	plant(t, filepath.Join(repo, "untracked.txt"), "untracked\n")

	dir := filepath.Join(repo, ".epitrop")
	_, signer, limits := testLog(t)
	w := openWriter(t, dir, signer, limits)
	appendTasks(t, dir, w, 11)
	hash10 := hashHex([]byte(readText(t, eventPath(eventsDir(dir), 10))))
	st := State{Tasks: map[string]Task{"task-0": {ID: "task-0", State: "open", Payload: raw(`{"state":"open"}`)}}}
	if err := w.WriteCheckpoint(testContext(t), "tasks", 10, hash10, st); err != nil {
		t.Fatalf("WriteCheckpoint() = %v, want nil", err)
	}
	rotate(t, w)
	plant(t, filepath.Join(eventsDir(dir), ".HEAD.json.tmp-1"), "half a head")
	plant(t, filepath.Join(eventsDir(dir), "segments", ".x.jsonl.tmp-1"), "half a segment")
	plant(t, filepath.Join(dir, "key.seed"), "not part of the log")
	return repo, dir, w
}

func plant(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll(%s) = %v, want nil", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) = %v, want nil", path, err)
	}
}

func recordInGit(t *testing.T, w *Writer) string {
	t.Helper()
	commit, err := w.RecordInGit(testContext(t), testGitRecord())
	if err != nil {
		t.Fatalf("RecordInGit() = %v, want nil", err)
	}
	return commit
}

// logContent reads every file under events/ and checkpoints/ whose name has no leading dot,
// keyed by its slash path relative to dir.
func logContent(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, top := range []string{"events", "checkpoints"} {
		err := filepath.WalkDir(filepath.Join(dir, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil || strings.HasPrefix(d.Name(), ".") || d.IsDir() {
				return err
			}
			rel, relErr := filepath.Rel(dir, path)
			files[filepath.ToSlash(rel)] = readText(t, path)
			return relErr
		})
		if err != nil {
			t.Fatalf("WalkDir(%s) = %v, want nil", top, err)
		}
	}
	return files
}

// committedContent reads every file commit holds.
func committedContent(t *testing.T, repo string, commit string) map[string]string {
	t.Helper()
	files := map[string]string{}
	names := strings.Split(gitAt(t, repo, "ls-tree", "-r", "--name-only", commit), "\n")
	for _, name := range names {
		files[name] = gitAt(t, repo, "cat-file", "blob", commit+":"+name)
	}
	return files
}

// trimmed matches what gitexec.Run returns for a file's content.
func trimmed(files map[string]string) map[string]string {
	out := make(map[string]string, len(files))
	for name, body := range files {
		out[name] = strings.TrimSpace(body)
	}
	return out
}

func TestRecordInGitCommitsTheLogByteForByte(t *testing.T) {
	repo, dir, w := gitLog(t)
	// A clean filter and line-ending conversion would change what is committed; the log's
	// files must be committed as they are.
	gitAt(t, repo, "config", "filter.upper.clean", "tr a-z A-Z")
	gitAt(t, repo, "config", "core.autocrlf", "true")
	plant(t, filepath.Join(repo, ".git", "info", "attributes"), "* filter=upper\n")

	commit := recordInGit(t, w)
	if got := gitAt(t, repo, "rev-parse", testRef); got != commit {
		t.Fatalf("%s = %s, want the returned commit %s", testRef, got, commit)
	}
	want := trimmed(logContent(t, dir))
	got := committedContent(t, repo, commit)
	if !maps.Equal(got, want) {
		t.Fatalf("the commit holds %q,\nwant %q", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
	for _, name := range []string{"events/HEAD.json", "events/00000000000000000011.json", "events/segments/00000000000000000001-00000000000000000003.jsonl", "checkpoints/tasks/00000000000000000010.json"} {
		if _, ok := got[name]; !ok {
			t.Errorf("the commit lacks %s", name)
		}
	}
	if msg := gitAt(t, repo, "log", "-1", "--format=%an <%ae>|%cn <%ce>|%s", commit); msg != "epitrop test <epitrop@example.invalid>|epitrop test <epitrop@example.invalid>|epitrop: event log through seq 11" {
		t.Errorf("the commit reads %q", msg)
	}
}

func TestRecordInGitLeavesTheOperatorsRepositoryAlone(t *testing.T) {
	repo, _, w := gitLog(t)
	before := []string{
		gitAt(t, repo, "rev-parse", "HEAD"), gitAt(t, repo, "symbolic-ref", "HEAD"),
		gitAt(t, repo, "status", "--porcelain=v1", "--untracked-files=all"), gitAt(t, repo, "diff", "--cached", "--name-status"),
	}
	recordInGit(t, w)
	after := []string{
		gitAt(t, repo, "rev-parse", "HEAD"), gitAt(t, repo, "symbolic-ref", "HEAD"),
		gitAt(t, repo, "status", "--porcelain=v1", "--untracked-files=all"), gitAt(t, repo, "diff", "--cached", "--name-status"),
	}
	if !slices.Equal(after, before) {
		t.Fatalf("the operator's repository changed:\n%q\nwant\n%q", after, before)
	}
}

func TestRecordInGitFollowsTheRefAndSkipsAnUnchangedLog(t *testing.T) {
	repo, dir, w := gitLog(t)
	first := recordInGit(t, w)
	if again := recordInGit(t, w); again != first {
		t.Fatalf("RecordInGit() on an unchanged log = %s, want the ref's commit %s", again, first)
	}
	appendTasks(t, dir, w, 1)
	second := recordInGit(t, w)
	if second == first {
		t.Fatal("RecordInGit() after an append made no commit")
	}
	if parent := gitAt(t, repo, "rev-parse", second+"^"); parent != first {
		t.Fatalf("the second commit follows %s, want %s", parent, first)
	}
	if got, want := committedContent(t, repo, second), trimmed(logContent(t, dir)); !maps.Equal(got, want) {
		t.Fatalf("the second commit holds %q, want %q", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
}

// withGitRun replaces how RecordInGit runs git for one test.
func withGitRun(t *testing.T, run func(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) (string, error)) {
	t.Helper()
	old := gitRun
	gitRun = run
	t.Cleanup(func() { gitRun = old })
}

func TestRecordInGitRefusesARefMovedMeanwhile(t *testing.T) {
	repo, dir, w := gitLog(t)
	recordInGit(t, w)
	appendTasks(t, dir, w, 1)
	other := gitAt(t, repo, "commit-tree", "-m", "another writer", gitAt(t, repo, "rev-parse", "HEAD^{tree}"))
	withGitRun(t, func(ctx context.Context, d string, env []string, stdin io.Reader, args ...string) (string, error) {
		if args[0] == "update-ref" {
			gitAt(t, repo, "update-ref", testRef, other)
		}
		return gitexec.Run(ctx, d, env, stdin, args...)
	})
	if _, err := w.RecordInGit(testContext(t), testGitRecord()); err == nil {
		t.Fatal("RecordInGit() over a ref moved meanwhile = nil, want an error")
	}
	if got := gitAt(t, repo, "rev-parse", testRef); got != other {
		t.Fatalf("%s = %s, want the other writer's %s", testRef, got, other)
	}
}

func TestRecordInGitFailureLeavesTheLogAndTheRef(t *testing.T) {
	repo, dir, w := gitLog(t)
	first := recordInGit(t, w)
	appendTasks(t, dir, w, 1)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	before := logContent(t, dir)
	withGitRun(t, func(ctx context.Context, d string, env []string, stdin io.Reader, args ...string) (string, error) {
		if args[0] == "commit-tree" {
			return "", errors.New("planted failure")
		}
		return gitexec.Run(ctx, d, env, stdin, args...)
	})
	if _, err := w.RecordInGit(testContext(t), testGitRecord()); err == nil || !strings.Contains(err.Error(), "planted failure") {
		t.Fatalf("RecordInGit() = %v, want the planted failure", err)
	}
	if got := gitAt(t, repo, "rev-parse", testRef); got != first {
		t.Fatalf("%s = %s, want it unchanged at %s", testRef, got, first)
	}
	if after := logContent(t, dir); !maps.Equal(after, before) {
		t.Fatal("a failed RecordInGit changed the log")
	}
	if left, err := os.ReadDir(tmp); err != nil || len(left) != 0 {
		t.Fatalf("a failed RecordInGit left %v in the temporary directory (%v)", left, err)
	}
}

func TestRecordInGitWaitsForTheLogLock(t *testing.T) {
	_, dir, w := gitLog(t)
	lock, err := acquireLock(testContext(t), eventsDir(dir), time.Second)
	if err != nil {
		t.Fatalf("acquireLock() = %v, want nil", err)
	}
	t.Cleanup(func() {
		if closeErr := lock.Close(); closeErr != nil {
			t.Errorf("lock.Close() = %v, want nil", closeErr)
		}
	})
	w.limits.LockTimeout = 50 * time.Millisecond
	if _, err = w.RecordInGit(testContext(t), testGitRecord()); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("RecordInGit() while another holds the lock = %v, want ErrLockTimeout", err)
	}
}

func TestRecordInGitRefusesBadSettings(t *testing.T) {
	repo, _, w := gitLog(t)
	cases := map[string]func(*GitRecord){
		"ref outside refs/":  func(g *GitRecord) { g.Ref = "heads/events" },
		"malformed ref":      func(g *GitRecord) { g.Ref = "refs/epitrop/bad..ref" },
		"no author":          func(g *GitRecord) { g.AuthorName = " " },
		"no email":           func(g *GitRecord) { g.AuthorEmail = "" },
		"bracket in email":   func(g *GitRecord) { g.AuthorEmail = "a>b@example.invalid" },
		"newline in name":    func(g *GitRecord) { g.AuthorName = "a\nb" },
		"no timeout":         func(g *GitRecord) { g.Timeout = 0 },
		"timeout below zero": func(g *GitRecord) { g.Timeout = -time.Second },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			g := testGitRecord()
			change(&g)
			if _, err := w.RecordInGit(testContext(t), g); err == nil {
				t.Fatalf("RecordInGit(%+v) = nil, want an error", g)
			}
		})
	}
	if refs := gitAt(t, repo, "for-each-ref", "refs/epitrop/"); refs != "" {
		t.Fatalf("refused settings left refs %q", refs)
	}
}

func TestRecordInGitRefusesALogOutsideARepository(t *testing.T) {
	dir, signer, limits := testLog(t)
	w := openWriter(t, dir, signer, limits)
	appendTasks(t, dir, w, 1)
	if _, err := w.RecordInGit(testContext(t), testGitRecord()); err == nil || !strings.Contains(err.Error(), "not in a git repository") {
		t.Fatalf("RecordInGit() outside a repository = %v, want it refused", err)
	}
}

func TestRecordInGitRecordsNothingWithoutEvents(t *testing.T) {
	dir, signer, limits := testLog(t)
	if commit, err := openWriter(t, dir, signer, limits).RecordInGit(testContext(t), testGitRecord()); commit != "" || err != nil {
		t.Fatalf("RecordInGit() of an empty log = %q, %v; want \"\", nil", commit, err)
	}
}
