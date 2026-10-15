package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/tekmora/epitrop/internal/slot"
)

type unitsHandoverFlags struct {
	repo, answers, resumeOut *string
}

// runUnitsHandover prints the hand-over of one repository: its open units, the questions of
// their last runs that the answers file does not answer, and the notes nobody read. It
// writes nothing to the run record; --resume-out writes the same as a slot's resume note.
func runUnitsHandover(args []string) error {
	fs := flag.NewFlagSet("units handover", flag.ContinueOnError)
	f := unitsHandoverFlags{
		repo:      fs.String("repo", "", "repository part of the unit ids, the text before # or :"),
		answers:   fs.String("answers", "", "answers file: one id per line, optionally a tab and the answer"),
		resumeOut: fs.String("resume-out", "", "file to write the hand-over to as a slot's resume note"),
	}
	svc, _, ctx, cancel, err := unitsCall(fs, args)
	if err != nil {
		return err
	}
	defer cancel()
	answers := map[string]string{}
	if *f.answers != "" {
		if answers, err = slot.ReadAnswers(*f.answers); err != nil {
			return err
		}
	}
	st, err := svc.Replay(ctx)
	if err != nil {
		return err
	}
	list, err := slot.Handover(st, *f.repo, answers)
	if err != nil {
		return err
	}
	if *f.resumeOut != "" {
		note, noteErr := slot.ResumeNote(*f.repo, list)
		if noteErr != nil {
			return noteErr
		}
		if err = slot.WriteResumeNote(*f.resumeOut, note); err != nil {
			return err
		}
	}
	return printHandover(list)
}

func printHandover(list []slot.HandoverUnit) error {
	if _, err := fmt.Fprintln(slotOut, "id\tstatus\tupdated_at\tworktree\tbranch\tpr\tlane\trelaunch\toutcome\tpending\tunread\tevidence"); err != nil {
		return fmt.Errorf("units handover: print: %w", err)
	}
	for i := 0; i < len(list); i++ {
		h := list[i]
		pending := strings.Join(h.Pending, ",")
		if h.ReportError != "" {
			pending = "unknown: " + h.ReportError
		}
		if _, err := fmt.Fprintf(slotOut, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t%q\t%d\t%s\n", h.ID, h.Status, h.UpdatedAt,
			h.Worktree, h.Branch, h.PR, h.Lane, h.Relaunch, h.Outcome, pending, len(h.Unread), h.Evidence); err != nil {
			return fmt.Errorf("units handover: print: %w", err)
		}
	}
	return nil
}
