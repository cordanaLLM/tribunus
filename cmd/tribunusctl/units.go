package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/cordanaLLM/tribunus/internal/config"
	"github.com/cordanaLLM/tribunus/internal/eventlog"
	"github.com/cordanaLLM/tribunus/internal/units"
	"github.com/jonboulle/clockwork"
)

var newUnitsService = func(cfg config.Config) (*units.Service, error) {
	clk := clockwork.NewRealClock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	signer, err := eventlog.NewSignerFromKeyFile(ctx, cfg.EventLog.SigningKeyPath, cfg.EventLog.Dir, clk)
	if err != nil {
		return nil, err
	}
	limits := eventlog.Limits{Clock: clk, MaxReplayRecords: cfg.EventLog.MaxReplayRecords}
	if cfg.EventLog.LockTimeoutSeconds > 0 {
		limits.LockTimeout = time.Duration(cfg.EventLog.LockTimeoutSeconds) * time.Second
	}
	return units.NewService(cfg.EventLog.Dir, signer, clk, limits)
}

// unitsStdin is the reader "--identity=-" takes the identity object from.
var unitsStdin io.Reader = os.Stdin

func runUnits(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("units command required: set, resume, show, note, or inbox")
	}
	switch args[0] {
	case "set":
		return runUnitsSet(args[1:])
	case "resume":
		return runUnitsResume(args[1:])
	case "show":
		return runUnitsShow(args[1:])
	case "note":
		return runUnitsNote(args[1:])
	case "inbox":
		return runUnitsInbox(args[1:])
	}
	return fmt.Errorf("unknown units command: %s", args[0])
}

// unitsCall parses the flags of one action, loads the config and opens the service. need
// names the positional arguments the action requires.
func unitsCall(fs *flag.FlagSet, args []string, need ...string) (*units.Service, []string, context.Context, context.CancelFunc, error) {
	configPath := fs.String("config", "", "configuration file")
	pos, err := parseInterspersedFlags(fs, args)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if len(pos) < len(need) {
		return nil, nil, nil, nil, fmt.Errorf("%s: %s is required", fs.Name(), need[len(pos)])
	}
	if *configPath == "" {
		return nil, nil, nil, nil, fmt.Errorf("%s: --config is required", fs.Name())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cfg, err := config.Load(ctx, *configPath)
	if err != nil {
		cancel()
		return nil, nil, nil, nil, err
	}
	svc, err := newUnitsService(cfg)
	if err != nil {
		cancel()
		return nil, nil, nil, nil, err
	}
	return svc, pos, ctx, cancel, nil
}

type unitsSetFlags struct {
	status, worktree, branch, lane, by, target, resolvedModel, identity, identityKey *string
	pr                                                                               *int
	reopen                                                                           *bool
}

func runUnitsSet(args []string) error {
	fs := flag.NewFlagSet("units set", flag.ContinueOnError)
	f := unitsSetFlags{
		status:        fs.String("status", "", "status from the task graph vocabulary"),
		worktree:      fs.String("worktree", "", "worktree directory"),
		branch:        fs.String("branch", "", "branch name"),
		pr:            fs.Int("pr", 0, "pull request number"),
		lane:          fs.String("lane", "", "lane name"),
		by:            fs.String("by", "", "actor name"),
		target:        fs.String("target", "", "alias or model that was asked for"),
		resolvedModel: fs.String("resolved-model", "", "model the alias resolved to"),
		identity:      fs.String("identity", "", "file holding the run identity object, or - for stdin"),
		identityKey:   fs.String("identity-key", "", "identity key of that identity"),
		reopen:        fs.Bool("reopen", false, "move a landed or abandoned unit again"),
	}
	svc, pos, ctx, cancel, err := unitsCall(fs, args, "id")
	if err != nil {
		return err
	}
	defer cancel()
	if *f.status == "" {
		return fmt.Errorf("units set: --status is required")
	}
	payload, err := buildRecordPayload(fs, f)
	if err != nil {
		return err
	}
	unit, err := svc.Record(ctx, pos[0], payload)
	if err != nil {
		return err
	}
	printNotes(units.UnreadNotes(unit))
	return nil
}

// buildRecordPayload sets only the fields whose flag was given, so the others stay sticky.
func buildRecordPayload(fs *flag.FlagSet, f unitsSetFlags) (units.RecordPayload, error) {
	payload := units.RecordPayload{Status: *f.status, Reopen: *f.reopen}
	given := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { given[fl.Name] = true })
	text := []struct {
		name string
		from *string
		to   **string
	}{
		{"worktree", f.worktree, &payload.Worktree}, {"branch", f.branch, &payload.Branch},
		{"lane", f.lane, &payload.Lane}, {"by", f.by, &payload.By}, {"target", f.target, &payload.Target},
		{"resolved-model", f.resolvedModel, &payload.ResolvedModel}, {"identity-key", f.identityKey, &payload.IdentityKey},
	}
	for i := 0; i < len(text); i++ {
		if given[text[i].name] {
			*text[i].to = text[i].from
		}
	}
	if given["pr"] {
		payload.PR = f.pr
	}
	if !given["identity"] {
		return payload, nil
	}
	identity, err := readIdentity(*f.identity)
	if err != nil {
		return units.RecordPayload{}, err
	}
	payload.Identity = identity
	return payload, nil
}

// readIdentity reads the identity object, bounded, from a file or stdin.
func readIdentity(path string) (json.RawMessage, error) {
	reader := unitsStdin
	if path != "-" {
		file, err := os.Open(path) // #nosec G304 -- the operator names the identity file on the command line.
		if err != nil {
			return nil, fmt.Errorf("units set: open identity: %w", err)
		}
		defer func() {
			if closeErr := file.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "units set: close identity: %v\n", closeErr)
			}
		}()
		reader = file
	}
	body, err := io.ReadAll(io.LimitReader(reader, units.MaxIdentityBytes+1))
	if err != nil {
		return nil, fmt.Errorf("units set: read identity: %w", err)
	}
	return json.RawMessage(strings.TrimSpace(string(body))), nil
}

func runUnitsResume(args []string) error {
	fs := flag.NewFlagSet("units resume", flag.ContinueOnError)
	svc, _, ctx, cancel, err := unitsCall(fs, args)
	if err != nil {
		return err
	}
	defer cancel()
	rows, ignored, err := svc.Resume(ctx)
	if err != nil {
		return err
	}
	if ignored > 0 {
		fmt.Fprintf(os.Stderr, "units: replay did not apply %d unit records that broke a rule (see units.UnitReducer)\n", ignored)
	}
	fmt.Println("id\tstatus\tupdated_at\tworktree\tbranch\tpr\tlane\trelaunch")
	for i := 0; i < len(rows); i++ {
		r := rows[i]
		fmt.Printf("%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n", r.ID, r.Status, r.UpdatedAt, r.Worktree, r.Branch, r.PR, r.Lane, r.Relaunch)
	}
	return nil
}

func runUnitsShow(args []string) error {
	fs := flag.NewFlagSet("units show", flag.ContinueOnError)
	svc, pos, ctx, cancel, err := unitsCall(fs, args, "id")
	if err != nil {
		return err
	}
	defer cancel()
	unit, err := svc.Show(ctx, pos[0])
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(unit, "", "  ")
	if err != nil {
		return fmt.Errorf("units show: marshal: %w", err)
	}
	fmt.Println(string(body))
	return nil
}

func runUnitsNote(args []string) error {
	fs := flag.NewFlagSet("units note", flag.ContinueOnError)
	from := fs.String("from", "", "sender name")
	svc, pos, ctx, cancel, err := unitsCall(fs, args, "id", "text")
	if err != nil {
		return err
	}
	defer cancel()
	if *from == "" {
		return fmt.Errorf("units note: --from is required")
	}
	_, err = svc.Note(ctx, pos[0], *from, strings.Join(pos[1:], " "))
	return err
}

func runUnitsInbox(args []string) error {
	fs := flag.NewFlagSet("units inbox", flag.ContinueOnError)
	svc, pos, ctx, cancel, err := unitsCall(fs, args, "id")
	if err != nil {
		return err
	}
	defer cancel()
	unread, err := svc.Inbox(ctx, pos[0])
	if err != nil {
		return err
	}
	printNotes(unread)
	return nil
}

// printNotes prints one note per line; the text is quoted so a note cannot forge a second
// line or carry terminal control sequences.
func printNotes(notes []eventlog.UnitNote) {
	for i := 0; i < len(notes); i++ {
		fmt.Printf("note %d from %s: %q\n", notes[i].Seq, notes[i].From, notes[i].Text)
	}
}

func parseInterspersedFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	var flags []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if !strings.Contains(name, "=") {
			f := fs.Lookup(name)
			if f != nil && !isBoolFlag(f) && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		}
	}
	if err := fs.Parse(flags); err != nil {
		return nil, flagErr{err: err}
	}
	return positional, nil
}

func isBoolFlag(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}
