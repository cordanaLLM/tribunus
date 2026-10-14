package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/tekmora/epitrop/internal/config"
	"github.com/tekmora/epitrop/internal/slot"
	"github.com/tekmora/epitrop/internal/supervisor"
)

// slotRunTimeout bounds one `slot run`: the longest deadline a slot's job may have, and time
// for the clone before it and for taking the result after it.
const slotRunTimeout = (slot.MaxRunSeconds + 15*60) * time.Second

var (
	// slotOut is where `slot run` prints.
	slotOut io.Writer = os.Stdout
	// slotWaitInterval is how often `slot run` looks whether its job still runs.
	slotWaitInterval = slot.WaitInterval
)

func runSlot(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("slot command required: run or resume")
	}
	switch args[0] {
	case "run":
		return runSlotRun(args[1:])
	case "resume":
		return runSlotResume(args[1:])
	default:
		return fmt.Errorf("unknown slot command: %s", args[0])
	}
}

type slotRunFlags struct {
	config, repo, base, brief, resume, decisions, workspace, out *string
}

// runSlotRun runs one unit in a slot and prints what became of it: the unit with its status
// and outcome, the branch with its head, the runner's report, and the reason or the run's
// questions when there are any. It fails when the run's result was not taken.
func runSlotRun(args []string) error {
	fs := flag.NewFlagSet("slot run", flag.ContinueOnError)
	f := slotRunFlags{
		config:    fs.String("config", "", "the slot's template: a configuration with one sandboxed job"),
		repo:      fs.String("repo", ".", "the repository the unit belongs to"),
		base:      fs.String("base", "", "the commit the unit starts from"),
		brief:     fs.String("brief", "", "file with the unit's brief"),
		resume:    fs.String("resume", "", "file with the state the run starts from"),
		decisions: fs.String("decisions", "", "file with the answered decisions: id, tab, answer per line"),
		workspace: fs.String("workspace", "", "directory to make the run's workspace in; it must not exist"),
		out:       fs.String("out", "", "directory for the run's report, result and evidence; it must not exist"),
	}
	pos, err := parseInterspersedFlags(fs, args)
	if err != nil {
		return err
	}
	req, err := slotRunRequest(f, pos)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), slotRunTimeout)
	defer cancel()
	report, err := slot.Run(ctx, req)
	if err != nil {
		return err
	}
	printSlotReport(report, req.Out)
	if !report.Taken() {
		return fmt.Errorf("slot run: %s is %s: %s: %s", report.Unit, report.Status, report.Outcome, report.Reason)
	}
	return nil
}

func slotRunRequest(f slotRunFlags, pos []string) (slot.RunRequest, error) {
	if len(pos) != 1 {
		return slot.RunRequest{}, fmt.Errorf("slot run: exactly one unit id is required")
	}
	need := []struct{ name, value string }{{"config", *f.config}, {"base", *f.base}, {"brief", *f.brief}, {"workspace", *f.workspace}, {"out", *f.out}}
	for i := 0; i < len(need); i++ {
		if need[i].value == "" {
			return slot.RunRequest{}, fmt.Errorf("slot run: --%s is required", need[i].name)
		}
	}
	in, err := slotInputs(*f.brief, *f.resume, *f.decisions)
	if err != nil {
		return slot.RunRequest{}, err
	}
	paths := []*string{f.repo, f.workspace, f.out}
	for i := 0; i < len(paths); i++ {
		abs, absErr := filepath.Abs(*paths[i])
		if absErr != nil {
			return slot.RunRequest{}, fmt.Errorf("slot run: %w", absErr)
		}
		*paths[i] = abs
	}
	return slot.RunRequest{
		Request: slot.Request{Repo: *f.repo, Base: *f.base, Unit: pos[0], Dir: *f.workspace, In: in},
		Out:     *f.out,
		Open:    openSlotRun(*f.config),
	}, nil
}

// slotInputs reads what the run is given. The brief is required; the other two files may be
// left out.
func slotInputs(brief string, resume string, decisions string) (slot.Inputs, error) {
	var in slot.Inputs
	var err error
	if in.Brief, err = readSlotInput(brief); err != nil {
		return slot.Inputs{}, err
	}
	if resume != "" {
		if in.Resume, err = readSlotInput(resume); err != nil {
			return slot.Inputs{}, err
		}
	}
	if decisions == "" {
		return in, nil
	}
	body, err := readSlotInput(decisions)
	if err != nil {
		return slot.Inputs{}, err
	}
	if in.Decisions, err = slot.ParseDecisions(body); err != nil {
		return slot.Inputs{}, err
	}
	return in, nil
}

// readSlotInput reads one input file, at most one byte beyond the bound of an input: the
// refusal of a file that is too large is the workspace's, with the bound in its text.
func readSlotInput(path string) ([]byte, error) {
	// #nosec G304 -- the operator names the file on the command line.
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("slot run: %w", err)
	}
	body, err := io.ReadAll(io.LimitReader(f, slot.MaxInputBytes+1))
	if closeErr := f.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, fmt.Errorf("slot run: read %s: %w", path, err)
	}
	return body, nil
}

// openSlotRun opens a run under the supervisor: the run's configuration is derived from the
// slot's template, and the job and the run record are the ones of that configuration.
func openSlotRun(template string) func(context.Context, slot.Workspace, string) (slot.Job, slot.Recorder, error) {
	return func(ctx context.Context, ws slot.Workspace, out string) (slot.Job, slot.Recorder, error) {
		cfg, _, err := slot.RunConfig(ctx, template, ws, out)
		if err != nil {
			return nil, nil, err
		}
		sup, err := newJobsSupervisor(cfg, supervisor.Options{})
		if err != nil {
			return nil, nil, err
		}
		record, err := newUnitsService(cfg)
		if err != nil {
			return nil, nil, err
		}
		return slot.NewSupervised(sup, cfg.Jobs[0], slotWaitInterval), record, nil
	}
}

func printSlotReport(report slot.Report, out string) {
	published := "not published"
	if report.Published {
		published = "published"
	}
	lines := []string{
		fmt.Sprintf("unit\t%s\t%s\t%s", report.Unit, report.Status, report.Outcome),
		fmt.Sprintf("branch\t%s\t%s\t%s", report.Branch, report.Head, published),
		fmt.Sprintf("report\t%s", filepath.Join(out, slot.ReportFile)),
	}
	if report.Reason != "" {
		lines = append(lines, "reason\t"+report.Reason)
	}
	for i := 0; i < len(report.Questions); i++ {
		lines = append(lines, "question\t"+report.Questions[i])
	}
	for i := 0; i < len(lines); i++ {
		if _, err := fmt.Fprintln(slotOut, lines[i]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: slot run: print: %v\n", err)
			return
		}
	}
}

type slotResumeFlags struct {
	repo, out *string
}

// runSlotResume takes up an interrupted run and prints what became of it: the unit with its
// status and outcome, the branch with its head, the runner's report, and the reason or the
// run's questions when there are any. It fails when the run's result was not taken.
func runSlotResume(args []string) error {
	fs := flag.NewFlagSet("slot resume", flag.ContinueOnError)
	f := slotResumeFlags{
		repo: fs.String("repo", ".", "the repository the unit belongs to"),
		out:  fs.String("out", "", "directory for the run's report, result and evidence"),
	}
	pos, err := parseInterspersedFlags(fs, args)
	if err != nil {
		return err
	}
	req, posUnit, err := slotResumeRequest(f, pos)
	if err != nil {
		return err
	}
	if posUnit != "" {
		start, readErr := slot.ReadStart(req.Out)
		if readErr != nil {
			return readErr
		}
		if posUnit != start.Unit {
			return fmt.Errorf("slot resume: unit %s does not match %s in %s", posUnit, start.Unit, slot.StartFile)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), slotRunTimeout)
	defer cancel()
	report, err := slot.TakeUp(ctx, req)
	if err != nil {
		return err
	}
	printSlotReport(report, req.Out)
	if !report.Taken() {
		return fmt.Errorf("slot resume: %s is %s: %s: %s", report.Unit, report.Status, report.Outcome, report.Reason)
	}
	return nil
}

func slotResumeRequest(f slotResumeFlags, pos []string) (slot.TakeUpRequest, string, error) {
	if len(pos) > 1 {
		return slot.TakeUpRequest{}, "", fmt.Errorf("slot resume: at most one unit id is allowed")
	}
	if *f.out == "" {
		return slot.TakeUpRequest{}, "", fmt.Errorf("slot resume: --out is required")
	}
	posUnit := ""
	if len(pos) == 1 {
		posUnit = pos[0]
	}
	paths := []*string{f.repo, f.out}
	for i := 0; i < len(paths); i++ {
		abs, absErr := filepath.Abs(*paths[i])
		if absErr != nil {
			return slot.TakeUpRequest{}, "", fmt.Errorf("slot resume: %w", absErr)
		}
		*paths[i] = abs
	}
	return slot.TakeUpRequest{
		Repo: *f.repo,
		Out:  *f.out,
		Open: openSlotResume,
	}, posUnit, nil
}

// openSlotResume opens a takeover under the supervisor from the run's existing configuration.
func openSlotResume(ctx context.Context, out string) (slot.Job, slot.UnitRecorder, error) {
	cfgPath := filepath.Join(out, slot.ConfigFile)
	cfg, err := config.Load(ctx, cfgPath)
	if err != nil {
		return nil, nil, fmt.Errorf("slot resume: %w", err)
	}
	if len(cfg.Jobs) != 1 {
		return nil, nil, fmt.Errorf("slot resume: %s must hold exactly one job", slot.ConfigFile)
	}
	if err = slot.CheckRunJob(cfg.Jobs[0]); err != nil {
		return nil, nil, fmt.Errorf("slot resume: %s: %w", slot.ConfigFile, err)
	}
	sup, err := newJobsSupervisor(cfg, supervisor.Options{})
	if err != nil {
		return nil, nil, err
	}
	record, err := newUnitsService(cfg)
	if err != nil {
		return nil, nil, err
	}
	return slot.NewSupervised(sup, cfg.Jobs[0], slotWaitInterval), record, nil
}
