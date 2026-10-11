package slot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tekmora/epitrop/internal/units"
)

const (
	// MaxInputBytes bounds the brief and the resume note.
	MaxInputBytes = 64 << 10
	// MaxDecisions bounds the answered decisions a run is given.
	MaxDecisions = 256
	// BriefFile, ResumeFile and DecisionsFile are what the runner writes into InDir.
	BriefFile     = InDir + "/brief.md"
	ResumeFile    = InDir + "/resume.md"
	DecisionsFile = InDir + "/decisions.tsv"
)

// Decision is one answered decision a unit depends on: its id and the answer.
type Decision struct {
	ID     string
	Answer string
}

// Inputs is what the runner puts into a run's workspace.
type Inputs struct {
	// Brief is the unit: goal, done-when, files.
	Brief []byte
	// Resume is the state of the repository the run starts from.
	Resume []byte
	// Decisions are the answers the unit depends on.
	Decisions []Decision
}

// Request names one run's workspace: which repository, from which commit, for which unit,
// and where.
type Request struct {
	// Repo is the repository the unit belongs to.
	Repo string
	// Base is the commit the unit starts from, as the repository names it.
	Base string
	// Unit is the work unit's id.
	Unit string
	// Dir is the workspace to create. It must not exist.
	Dir string
	In  Inputs
}

// Workspace is a prepared workspace.
type Workspace struct {
	Dir string
	// Branch is the branch the run commits to, unit/<id> with the id's colon written as "=".
	Branch string
	// Base is the commit the branch starts at.
	Base string
}

// Branch returns the branch of a unit. A unit id may hold a colon, which a branch name may
// not; it is written as "=", a character no unit id holds, so two units never share a branch.
func Branch(unit string) string {
	return "unit/" + strings.ReplaceAll(unit, ":", "=")
}

// Prepare makes the workspace of one run: a fresh clone of the repository, on the unit's
// branch at the base commit, with the runner's files under InDir and an empty OutDir.
//
// The clone shares nothing with the repository it was made from. Its objects are copies,
// not hard links, and it has no remote: a run that can write its whole workspace can then
// not reach the repository through it. The files of the contract are kept out of the run's
// commits by the clone's own exclude file.
func Prepare(ctx context.Context, req Request) (Workspace, error) {
	if err := checkRequest(ctx, req); err != nil {
		return Workspace{}, err
	}
	branch := Branch(req.Unit)
	if _, err := git(ctx, "", "check-ref-format", "--branch", branch); err != nil {
		return Workspace{}, fmt.Errorf("slot: unit %q gives no valid branch name %q: %w", req.Unit, branch, err)
	}
	base, err := git(ctx, req.Repo, "rev-parse", "--verify", "--quiet", req.Base+"^{commit}")
	if err != nil {
		return Workspace{}, fmt.Errorf("slot: base %q is no commit of %s: %w", req.Base, req.Repo, err)
	}
	if _, err = git(ctx, "", "clone", "--quiet", "--no-hardlinks", "--no-tags", "--no-checkout", "--", req.Repo, req.Dir); err != nil {
		return Workspace{}, fmt.Errorf("slot: clone %s: %w", req.Repo, err)
	}
	ws := Workspace{Dir: req.Dir, Branch: branch, Base: base}
	if err = finishClone(ctx, ws, req.In); err != nil {
		return Workspace{}, errors.Join(err, removeWorkspace(req.Dir))
	}
	return ws, nil
}

func checkRequest(ctx context.Context, req Request) error {
	if ctx == nil {
		return errors.New("slot: prepare: nil context")
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("slot: prepare requires a context deadline")
	}
	if !units.IsValidID(req.Unit) {
		return fmt.Errorf("slot: invalid unit id %q", req.Unit)
	}
	if !filepath.IsAbs(req.Repo) || !filepath.IsAbs(req.Dir) {
		return errors.New("slot: repository and workspace are absolute paths")
	}
	if req.Base == "" || strings.HasPrefix(req.Base, "-") {
		return fmt.Errorf("slot: base %q is no commit name", req.Base)
	}
	if _, err := os.Lstat(req.Dir); err == nil {
		return fmt.Errorf("slot: workspace %s exists", req.Dir)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("slot: workspace %s: %w", req.Dir, err)
	}
	return checkInputs(req.In)
}

func checkInputs(in Inputs) error {
	if len(in.Brief) == 0 {
		return errors.New("slot: the brief is empty")
	}
	if len(in.Brief) > MaxInputBytes || len(in.Resume) > MaxInputBytes {
		return fmt.Errorf("slot: brief and resume note are at most %d bytes each", MaxInputBytes)
	}
	if len(in.Decisions) > MaxDecisions {
		return fmt.Errorf("slot: at most %d decisions, got %d", MaxDecisions, len(in.Decisions))
	}
	for i := 0; i < len(in.Decisions); i++ {
		if in.Decisions[i].ID == "" || strings.ContainsAny(in.Decisions[i].ID+in.Decisions[i].Answer, "\t\n\r") {
			return fmt.Errorf("slot: decision %d has no id, or a tab or line break in its id or answer", i)
		}
	}
	return nil
}

// finishClone turns the bare clone into the run's workspace.
func finishClone(ctx context.Context, ws Workspace, in Inputs) error {
	steps := [][]string{
		{"remote", "remove", "origin"},
		{"switch", "--quiet", "--create", ws.Branch, ws.Base},
	}
	for i := 0; i < len(steps); i++ {
		if _, err := git(ctx, ws.Dir, steps[i]...); err != nil {
			return fmt.Errorf("slot: git %s: %w", steps[i][0], err)
		}
	}
	exclude := filepath.Join(ws.Dir, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o750); err != nil {
		return fmt.Errorf("slot: %w", err)
	}
	if err := os.WriteFile(exclude, []byte("/.slot/\n"), 0o600); err != nil {
		return fmt.Errorf("slot: %w", err)
	}
	return writeInputs(ws.Dir, in)
}

func writeInputs(dir string, in Inputs) error {
	for _, sub := range []string{InDir, EvidenceDir} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(sub)), 0o750); err != nil {
			return fmt.Errorf("slot: %w", err)
		}
	}
	var decisions bytes.Buffer
	for i := 0; i < len(in.Decisions); i++ {
		decisions.WriteString(in.Decisions[i].ID + "\t" + in.Decisions[i].Answer + "\n")
	}
	files := []struct {
		name string
		body []byte
	}{{BriefFile, in.Brief}, {ResumeFile, in.Resume}, {DecisionsFile, decisions.Bytes()}}
	for i := 0; i < len(files); i++ {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(files[i].name)), files[i].body, 0o600); err != nil {
			return fmt.Errorf("slot: %w", err)
		}
	}
	return nil
}

// removeWorkspace removes a workspace that could not be finished, so that a failed Prepare
// leaves nothing a later run would take for a workspace.
func removeWorkspace(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("slot: remove the unfinished workspace: %w", err)
	}
	return nil
}

// gitEnv returns the environment git runs in: the caller's without any of git's own
// variables, then the settings of this package.
//
// Git exports GIT_DIR, GIT_WORK_TREE and GIT_INDEX_FILE to every hook it runs. A runner
// started from a hook would hand them on, and a command meant for the clone would then act
// on the hook's repository. GIT_CONFIG_COUNT and GIT_CONFIG_PARAMETERS would carry settings
// in the same way. None of git's variables is needed here, so all of them are dropped.
func gitEnv(environ []string) []string {
	env := make([]string, 0, len(environ)+4)
	for i := 0; i < len(environ); i++ {
		name, _, _ := strings.Cut(environ[i], "=")
		if !strings.HasPrefix(strings.ToUpper(name), "GIT_") {
			env = append(env, environ[i])
		}
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "LC_ALL=C")
}

// git is how this package runs git; a test replaces it to make one step fail.
var git = runGit

// runGit runs one git command, in dir when dir is set, and returns its trimmed output. It never
// prompts and reads no configuration but the repository's own: the operator's global
// settings, hooks path and credential helpers are not for a slot's clone.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	// #nosec G204 -- git is called with a fixed subcommand; names from the request follow
	// "--" or are checked not to start with "-".
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv(os.Environ())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}
