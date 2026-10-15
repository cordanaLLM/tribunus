package slot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/tekmora/epitrop/internal/eventlog"
	"github.com/tekmora/epitrop/internal/graph"
	"github.com/tekmora/epitrop/internal/telemetry"
	"github.com/tekmora/epitrop/internal/units"
)

// The outcomes the runner gives a run that left no result it can take. A run's own outcomes
// are OutcomeDone, OutcomeBlocked and OutcomeFailed.
const (
	// OutcomeRefused says the supervisor did not start the run's job.
	OutcomeRefused = "refused"
	// OutcomeNotTaken says the run ended and the runner did not take its result: there was
	// none, it was not valid, or the repository did not take the branch. The reason says which.
	OutcomeNotTaken = "not-taken"
	// OutcomePreempted says the unit was preempted before start or after wait.
	OutcomePreempted = "preempted"
)

// The files of a run's output directory, which lies outside the workspace.
const (
	// StartFile is the runner's record of the run before its job starts.
	StartFile = "start.json"
	// ReportFile is the runner's own report of the run. The unit's evidence pointer names it.
	ReportFile = "run.json"
	// ResultCopy is the run's result as it was checked.
	ResultCopy = "result.json"
	// EvidenceCopies holds the evidence files as they were checked.
	EvidenceCopies = "evidence"
)

// Start is the record of one prepared run before its job starts.
type Start struct {
	Unit       string `json:"unit"`
	Workspace  string `json:"workspace"`
	Branch     string `json:"branch"`
	Base       string `json:"base"`
	Job        string `json:"job"`
	PreparedAt string `json:"prepared_at"`
}

// ErrNotStarted is what a Job's Start wraps when the run's job did not start and no job of
// the slot was started by this call. The runner then records the unit as blocked. Any other
// error of Start leaves open whether a job runs, and the runner records nothing.
var ErrNotStarted = errors.New("slot: the run's job did not start")

// JobEnd is how the job of a run ended, as the event log tells it.
type JobEnd struct {
	// Recorded is false when the job stopped running without its exit in the event log.
	Recorded bool `json:"recorded"`
	// Code is the job's exit code, when it has one.
	Code *int `json:"code,omitempty"`
	// Reason is the supervisor's reason for the end, "deadline" for one.
	Reason string `json:"reason,omitempty"`
}

// Job is the supervised, sandboxed job of one run.
type Job interface {
	// Name returns the job's name.
	Name() string
	// Preflight reports why the job cannot be started now, without starting anything.
	Preflight(ctx context.Context) error
	// Start starts the job and returns when its start is recorded.
	Start(ctx context.Context) error
	// Wait returns when the job has ended.
	Wait(ctx context.Context) (JobEnd, error)
}

// Adopter is what a job that can be adopted by a takeover implements.
type Adopter interface {
	Adopt(ctx context.Context, preparedAt string) error
}

// Recorder is the run record.
type Recorder interface {
	Record(ctx context.Context, id string, payload units.RecordPayload) (eventlog.Unit, error)
}

// UnitRecorder is the run record and its getter.
type UnitRecorder interface {
	Recorder
	Show(ctx context.Context, id string) (eventlog.Unit, error)
}

// TakeUpRequest names one takeover of an interrupted run.
type TakeUpRequest struct {
	Repo string
	Out  string
	Open func(ctx context.Context, out string) (Job, UnitRecorder, error)
}

// RunRequest names one run.
type RunRequest struct {
	Request
	// Out is the run's output directory. It must not exist, and it lies outside the
	// workspace: the run cannot write it. The runner puts its report there, and the result
	// and the evidence as it checked them.
	Out string
	// Open opens what the run takes from its configuration: its job and the run record.
	// It is called when the workspace and the output directory exist, because the run's
	// configuration names both.
	Open func(ctx context.Context, ws Workspace, out string) (Job, UnitRecorder, error)
}

// Report is the runner's account of one run. The run's own claims, its summary and its
// questions, are carried as the run wrote them; everything else the runner established.
type Report struct {
	Unit string `json:"unit"`
	// Outcome is the run's outcome, or OutcomeRefused or OutcomeNotTaken.
	Outcome string `json:"outcome"`
	// Status is the status the unit was recorded with.
	Status string `json:"status"`
	// Reason says why a run was refused or its result not taken.
	Reason    string  `json:"reason,omitempty"`
	Job       *JobEnd `json:"job,omitempty"`
	Workspace string  `json:"workspace"`
	Branch    string  `json:"branch"`
	Base      string  `json:"base"`
	// Head is the head of the unit's branch as it was fetched from the workspace.
	Head string `json:"head,omitempty"`
	// Published says the branch was brought into the repository. A branch without a commit
	// of its own is not.
	Published bool   `json:"published"`
	Summary   string `json:"summary,omitempty"`
	// Result and Evidence name the copies in the output directory, by path relative to it.
	Result    *Evidence  `json:"result,omitempty"`
	Evidence  []Evidence `json:"evidence,omitempty"`
	Questions []string   `json:"questions,omitempty"`
	// Binds are what the job's sandbox made visible to the job, as its configuration names
	// them; none for a job that does not tell.
	Binds []Bind `json:"binds,omitempty"`
}

// Taken reports whether the run left a result the runner took.
func (r Report) Taken() bool {
	return r.Outcome == OutcomeDone || r.Outcome == OutcomeBlocked || r.Outcome == OutcomeFailed
}

// Run runs one unit in a slot and records what became of it.
//
// It prepares the workspace, records the unit as implementing, starts the job and waits for
// its end. Then it takes what the run left: the result is checked (ReadResult), the unit's
// branch is fetched from the workspace and compared with the head the result names, the
// result and the evidence are copied out, and the branch is brought into the repository. A
// run that reports done is recorded as review; every other end, a refused start and a result
// that cannot be taken among them, is recorded as blocked. The unit's evidence pointer names
// the runner's report.
//
// The returned error is nil when the run was recorded, whatever its outcome. An error after
// the job was started leaves the unit as implementing: the runner does not know then what
// became of the job.
func Run(ctx context.Context, req RunRequest) (rep Report, err error) {
	ctx, span := telemetry.Default().StartSpan(ctx, "slot.run", attribute.String("epitrop.unit", req.Unit))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()
	if err = checkRun(ctx, req); err != nil {
		return Report{}, err
	}
	ws, err := Prepare(ctx, req.Request)
	if err != nil {
		return Report{}, err
	}
	report := Report{Unit: req.Unit, Workspace: ws.Dir, Branch: ws.Branch, Base: ws.Base}
	job, rec, err := begin(ctx, req, ws)
	if err != nil {
		return Report{}, errors.Join(err, removeWorkspace(ws.Dir), removeOut(req.Out))
	}
	report.Binds = jobBinds(job)
	u, err := rec.Show(ctx, req.Unit)
	if err != nil {
		return report, fmt.Errorf("slot: read unit %s before start: %w", req.Unit, err)
	}
	if u.Preempted != nil {
		report.Outcome, report.Reason = OutcomePreempted, u.Preempted.Reason
		return finish(ctx, rec, req, report)
	}
	if err = job.Start(ctx); errors.Is(err, ErrNotStarted) {
		report.Outcome, report.Reason = OutcomeRefused, err.Error()
		return finish(ctx, rec, req, report)
	}
	if err != nil {
		return report, fmt.Errorf("slot: start the job of %s; the unit stays %s: %w", req.Unit, graph.StatusImplementing, err)
	}
	end, err := job.Wait(ctx)
	if err != nil {
		return report, fmt.Errorf("slot: wait for the job of %s; the unit stays %s: %w", req.Unit, graph.StatusImplementing, err)
	}
	return takeAndFinish(ctx, rec, req, ws, end, report)
}

func checkRun(ctx context.Context, req RunRequest) error {
	if ctx == nil {
		return errors.New("slot: run: nil context")
	}
	if req.Open == nil {
		return errors.New("slot: run needs a way to open its job and the run record")
	}
	if !filepath.IsAbs(req.Out) || !filepath.IsAbs(req.Dir) {
		return errors.New("slot: workspace and output directory are absolute paths")
	}
	out, dir := filepath.Clean(req.Out), filepath.Clean(req.Dir)
	sep := string(filepath.Separator)
	if out == dir || strings.HasPrefix(out+sep, dir+sep) || strings.HasPrefix(dir+sep, out+sep) {
		return fmt.Errorf("slot: output directory %s and workspace %s lie inside one another; the run could write its own report", req.Out, req.Dir)
	}
	if _, err := os.Lstat(req.Out); err == nil {
		return fmt.Errorf("slot: output directory %s exists", req.Out)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("slot: output directory %s: %w", req.Out, err)
	}
	return nil
}

// begin does what has to hold before the job starts: the output directory exists, the job
// can be started, and the unit is recorded as implementing. The record comes last and before
// the start, so that a unit that may not move to implementing starts no job.
func begin(ctx context.Context, req RunRequest, ws Workspace) (Job, UnitRecorder, error) {
	if err := os.Mkdir(req.Out, 0o750); err != nil {
		return nil, nil, fmt.Errorf("slot: %w", err)
	}
	job, rec, err := req.Open(ctx, ws, req.Out)
	if err != nil {
		return nil, nil, err
	}
	if job == nil || rec == nil {
		return nil, nil, errors.New("slot: the run has no job or no run record")
	}
	start := Start{
		Unit:       req.Unit,
		Workspace:  ws.Dir,
		Branch:     ws.Branch,
		Base:       ws.Base,
		Job:        job.Name(),
		PreparedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	pointer, err := writeStart(req.Out, start)
	if err != nil {
		return nil, nil, err
	}
	if err = job.Preflight(ctx); err != nil {
		return nil, nil, fmt.Errorf("slot: the job of %s cannot start: %w", req.Unit, err)
	}
	payload := units.RecordPayload{
		Status:   string(graph.StatusImplementing),
		Worktree: &ws.Dir,
		Branch:   &ws.Branch,
		Evidence: pointer,
	}
	if _, err = rec.Record(ctx, req.Unit, payload); err != nil {
		return nil, nil, err
	}
	return job, rec, nil
}

func writeStart(out string, start Start) (*eventlog.UnitEvidence, error) {
	body, err := json.MarshalIndent(start, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("slot: marshal %s: %w", StartFile, err)
	}
	body = append(body, '\n')
	path := filepath.Join(out, StartFile)
	if err = writeNew(path, body); err != nil {
		return nil, err
	}
	return &eventlog.UnitEvidence{Path: path, SHA256: digestOf(body), Lines: bytes.Count(body, []byte("\n"))}, nil
}

// ReadStart reads and validates the start file of a run.
func ReadStart(out string) (Start, error) {
	path := filepath.Join(out, StartFile)
	body, err := readRegular(path, MaxInputBytes)
	if err != nil {
		return Start{}, fmt.Errorf("slot: read %s: %w", StartFile, err)
	}
	var s Start
	if err = json.Unmarshal(body, &s); err != nil {
		return Start{}, fmt.Errorf("slot: %s is not valid: %w", StartFile, err)
	}
	if err = validateStart(s); err != nil {
		return Start{}, fmt.Errorf("slot: %s: %w", StartFile, err)
	}
	return s, nil
}

func validateStart(s Start) error {
	if s.Unit == "" || !units.IsValidID(s.Unit) {
		return errors.New("unit id is missing or invalid")
	}
	if s.Workspace == "" || s.Branch == "" || s.Base == "" || s.Job == "" {
		return errors.New("workspace, branch, base, or job is missing")
	}
	if s.PreparedAt == "" {
		return errors.New("prepared_at is missing")
	}
	if _, err := time.Parse(time.RFC3339Nano, s.PreparedAt); err != nil {
		return fmt.Errorf("prepared_at: %w", err)
	}
	return nil
}

// TakeUp takes up an interrupted run whose job started and records what became of it.
func TakeUp(ctx context.Context, req TakeUpRequest) (rep Report, err error) {
	ctx, span := telemetry.Default().StartSpan(ctx, "slot.run")
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()
	start, err := checkTakeUp(ctx, req)
	if err != nil {
		return Report{}, err
	}
	span.SetAttributes(attribute.String("epitrop.unit", start.Unit))
	job, rec, err := req.Open(ctx, req.Out)
	if err != nil {
		return Report{}, err
	}
	if job == nil || rec == nil {
		return Report{}, errors.New("slot: the run has no job or no run record")
	}
	if err = checkTakeUpUnit(ctx, rec, start, req.Out); err != nil {
		return Report{}, err
	}
	if adopter, ok := job.(Adopter); ok {
		if err = adopter.Adopt(ctx, start.PreparedAt); err != nil {
			return Report{}, fmt.Errorf("slot: adopt the job of %s: %w", start.Unit, err)
		}
	}
	ws := Workspace{Dir: start.Workspace, Branch: start.Branch, Base: start.Base}
	report := Report{Unit: start.Unit, Workspace: ws.Dir, Branch: ws.Branch, Base: ws.Base, Binds: jobBinds(job)}
	end, err := job.Wait(ctx)
	if err != nil {
		return report, fmt.Errorf("slot: wait for the job of %s; the unit stays %s: %w", start.Unit, graph.StatusImplementing, err)
	}
	runReq := RunRequest{Request: Request{Repo: req.Repo, Dir: ws.Dir, Base: ws.Base, Unit: start.Unit}, Out: req.Out}
	return takeAndFinish(ctx, rec, runReq, ws, end, report)
}

func takeAndFinish(ctx context.Context, rec UnitRecorder, req RunRequest, ws Workspace, end JobEnd, report Report) (Report, error) {
	report.Job = &end
	u, err := rec.Show(ctx, req.Unit)
	if err != nil {
		return report, fmt.Errorf("slot: read unit %s after wait: %w", req.Unit, err)
	}
	if u.Preempted != nil {
		report.Outcome, report.Reason = OutcomePreempted, u.Preempted.Reason
		return finish(ctx, rec, req, report)
	}
	if err = take(ctx, req, ws, &report); err != nil {
		report = Report{Unit: req.Unit, Workspace: ws.Dir, Branch: ws.Branch, Base: ws.Base, Job: &end, Binds: report.Binds}
		report.Outcome, report.Reason = OutcomeNotTaken, err.Error()
	}
	return finish(ctx, rec, req, report)
}

// checkTakeUp reads the start file and refuses a run that was recorded already. The take
// runs git in the repository, so it needs a deadline as a prepared run does.
func checkTakeUp(ctx context.Context, req TakeUpRequest) (Start, error) {
	if ctx == nil {
		return Start{}, errors.New("slot: take up: nil context")
	}
	if _, ok := ctx.Deadline(); !ok {
		return Start{}, errors.New("slot: take up requires a context deadline")
	}
	if req.Open == nil {
		return Start{}, errors.New("slot: take up needs a way to open its job and the run record")
	}
	if !filepath.IsAbs(req.Repo) || !filepath.IsAbs(req.Out) {
		return Start{}, errors.New("slot: repository and output directory are absolute paths")
	}
	start, err := ReadStart(req.Out)
	if err != nil {
		return Start{}, err
	}
	report := filepath.Join(req.Out, ReportFile)
	if _, err = os.Lstat(report); err == nil {
		return Start{}, errors.New("slot: the run was recorded already; nothing to take up")
	} else if !os.IsNotExist(err) {
		return Start{}, fmt.Errorf("slot: %s: %w", report, err)
	}
	return start, nil
}

func checkTakeUpUnit(ctx context.Context, rec UnitRecorder, start Start, out string) error {
	unit, err := rec.Show(ctx, start.Unit)
	if err != nil {
		return fmt.Errorf("slot: %w", err)
	}
	if unit.Preempted != nil {
		return fmt.Errorf("slot: cannot take up unit %s: unit was preempted: %s", start.Unit, unit.Preempted.Reason)
	}
	startPath := filepath.Join(out, StartFile)
	if unit.Status != string(graph.StatusImplementing) || unit.Worktree != start.Workspace || unit.Evidence == nil || unit.Evidence.Path != startPath {
		return fmt.Errorf("slot: cannot take up unit %s with status %q and worktree %q", start.Unit, unit.Status, unit.Worktree)
	}
	return nil
}

func removeOut(out string) error {
	if err := os.RemoveAll(out); err != nil {
		return fmt.Errorf("slot: remove the unused output directory: %w", err)
	}
	return nil
}

// take takes what the run left. The order keeps a refusal free of visible effects: the
// branch is first fetched into a ref of the runner's own and checked there, then the result
// and the evidence are copied, and the branch appears in the repository last.
func take(ctx context.Context, req RunRequest, ws Workspace, report *Report) error {
	result, body, err := readResult(ws.Dir, req.Unit)
	if err != nil {
		return err
	}
	head, err := stageBranch(ctx, req.Repo, ws, result.Head, readOnlyFiles(report.Binds))
	if err != nil {
		return errors.Join(fmt.Errorf("%w: %w", ErrResult, err), dropStaged(ctx, req.Repo, ws))
	}
	if err = copyOut(ws.Dir, req.Out, result, body, report); err != nil {
		return errors.Join(err, dropStaged(ctx, req.Repo, ws))
	}
	if head != ws.Base {
		if err = publishBranch(ctx, req.Repo, ws); err != nil {
			return errors.Join(err, dropStaged(ctx, req.Repo, ws))
		}
		report.Published = true
	}
	report.Outcome, report.Head, report.Summary, report.Questions = result.Outcome, head, result.Summary, result.Questions
	return dropStaged(ctx, req.Repo, ws)
}

// copyOut writes the result and the evidence into the output directory, each as the bytes
// that were checked: the result's are the ones ReadResult validated, and an evidence file is
// read again and has to give the digest the result names.
func copyOut(workspace string, out string, result Result, body []byte, report *Report) error {
	if err := writeNew(filepath.Join(out, ResultCopy), body); err != nil {
		return err
	}
	report.Result = &Evidence{Path: ResultCopy, SHA256: digestOf(body), Lines: bytes.Count(body, []byte("\n"))}
	copies := make([]Evidence, 0, len(result.Evidence))
	for i := 0; i < len(result.Evidence); i++ {
		entry := result.Evidence[i]
		file, err := readRegular(filepath.Join(workspace, filepath.FromSlash(entry.Path)), MaxEvidenceBytes)
		if err != nil {
			return fmt.Errorf("%w: evidence %q: %w", ErrResult, entry.Path, err)
		}
		if digestOf(file) != entry.SHA256 {
			return fmt.Errorf("%w: evidence %q changed after it was checked", ErrResult, entry.Path)
		}
		rel := EvidenceCopies + strings.TrimPrefix(entry.Path, EvidenceDir)
		target := filepath.Join(out, filepath.FromSlash(rel))
		if err = os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return fmt.Errorf("slot: %w", err)
		}
		if err = writeNew(target, file); err != nil {
			return err
		}
		copies = append(copies, Evidence{Path: rel, SHA256: entry.SHA256, Lines: entry.Lines})
	}
	report.Evidence = copies
	return nil
}

func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// writeNew writes a file that must not exist yet.
func writeNew(path string, body []byte) error {
	// #nosec G304 -- the path lies in the run's output directory, which the runner made.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("slot: %w", err)
	}
	_, err = f.Write(body)
	if closeErr := f.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("slot: write %s: %w", path, err)
	}
	return nil
}

// finish writes the runner's report and records the unit with a pointer to it.
func finish(ctx context.Context, rec Recorder, req RunRequest, report Report) (Report, error) {
	report.Status = string(graph.StatusBlocked)
	if report.Outcome == OutcomeDone {
		report.Status = string(graph.StatusReview)
	}
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return report, fmt.Errorf("slot: marshal the report: %w", err)
	}
	body = append(body, '\n')
	path := filepath.Join(req.Out, ReportFile)
	if err = writeNew(path, body); err != nil {
		return report, err
	}
	pointer := &eventlog.UnitEvidence{Path: path, SHA256: digestOf(body), Lines: bytes.Count(body, []byte("\n"))}
	if _, err = rec.Record(ctx, req.Unit, units.RecordPayload{Status: report.Status, Evidence: pointer}); err != nil {
		return report, fmt.Errorf("slot: the run of %s ended as %s, and its record failed: %w", req.Unit, report.Outcome, err)
	}
	telemetry.Default().SlotRun(ctx, report.Outcome)
	return report, nil
}
