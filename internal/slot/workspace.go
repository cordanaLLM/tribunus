package slot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tekmora/epitrop/internal/gitexec"
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

// ParseDecisions reads the answered decisions of a unit from their file form, the one the
// workspace gets: one decision per line, the id, a tab, the answer. An empty line is
// skipped. The bounds and the form of an id and an answer are checked when the workspace is
// prepared.
func ParseDecisions(body []byte) ([]Decision, error) {
	lines := strings.Split(string(body), "\n")
	if len(lines) > MaxDecisions+1 {
		return nil, fmt.Errorf("slot: at most %d decisions", MaxDecisions)
	}
	decisions := make([]Decision, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSuffix(lines[i], "\r")
		if line == "" {
			continue
		}
		id, answer, found := strings.Cut(line, "\t")
		if !found || id == "" {
			return nil, fmt.Errorf("slot: decisions line %d is not an id, a tab and an answer", i+1)
		}
		decisions = append(decisions, Decision{ID: id, Answer: answer})
	}
	return decisions, nil
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
		// A clone gets a branch of its own for the branch the repository is on. When that is
		// the unit's branch, it exists here already, at the repository's commit and not at
		// the base: the branch is made anew in every case.
		{"switch", "--quiet", "--force-create", ws.Branch, ws.Base},
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

// git is how this package runs git; a test replaces it to make one step fail.
var git = runGit

// runGit runs one git command, in dir when dir is set, and returns its trimmed output. It never
// prompts and reads no configuration but the repository's own: the operator's global
// settings, hooks path and credential helpers are not for a slot's clone.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	return gitexec.Run(ctx, dir, gitexec.Env(os.Environ()), nil, args...)
}
