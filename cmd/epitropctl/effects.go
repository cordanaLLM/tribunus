package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/tekmora/epitrop/internal/config"
	"github.com/tekmora/epitrop/internal/effects"
	"github.com/tekmora/epitrop/internal/eventlog"
)

var newEffectsLedger = func(cfg config.Config) (*effects.Ledger, error) {
	signer, clk, err := eventLogSigner(cfg)
	if err != nil {
		return nil, err
	}
	return effects.NewLedger(cfg.EventLog.Dir, signer, clk, eventLogLimits(cfg, clk))
}

// effectsOut and effectsErr are where the commands write.
var (
	effectsOut io.Writer = os.Stdout
	effectsErr io.Writer = os.Stderr
)

func runEffects(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("effects command required: list or resolve")
	}
	switch args[0] {
	case "list":
		return runEffectsList(args[1:])
	case "resolve":
		return runEffectsResolve(args[1:])
	}
	return fmt.Errorf("unknown effects command: %s", args[0])
}

// effectsCall parses the flags of one action, loads the config and opens the ledger.
func effectsCall(fs *flag.FlagSet, args []string, need ...string) (*effects.Ledger, []string, context.Context, context.CancelFunc, error) {
	cfg, pos, ctx, cancel, err := commandConfig(fs, args, need...)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	ledger, err := newEffectsLedger(cfg)
	if err != nil {
		cancel()
		return nil, nil, nil, nil, err
	}
	return ledger, pos, ctx, cancel, nil
}

// runEffectsList prints the side-effect ledger, one effect per line: task, action type,
// state, attempt, skipped deliveries and, for an open or resolved effect, why. Open claims
// are counted on stderr: each blocks its key until it is resolved.
func runEffectsList(args []string) error {
	fs := flag.NewFlagSet("effects list", flag.ContinueOnError)
	ledger, _, ctx, cancel, err := effectsCall(fs, args)
	if err != nil {
		return err
	}
	defer cancel()
	all, ignored, err := ledger.Status(ctx)
	if err != nil {
		return err
	}
	open := 0
	for i := 0; i < len(all); i++ {
		if effects.IsOpen(all[i]) {
			open++
		}
		if _, err = fmt.Fprintf(effectsOut, "%s\t%s\t%s\t%d\t%d\t%s\n", all[i].TaskID, all[i].ActionType, all[i].State, all[i].Attempt, all[i].Skipped, effectDetail(all[i])); err != nil {
			return fmt.Errorf("effects list: write: %w", err)
		}
	}
	return reportEffects(open, ignored)
}

// effectDetail is what the last column says: the failure of a failed attempt, or who
// resolved an open claim and why.
func effectDetail(effect eventlog.Effect) string {
	if effect.State == effects.StateFailed {
		return fmt.Sprintf("%q", effect.Reason)
	}
	if effect.ResolvedBy != "" {
		return fmt.Sprintf("resolved by %s: %q", effect.ResolvedBy, effect.ResolvedNote)
	}
	return ""
}

func reportEffects(open int, ignored int) error {
	if open > 0 {
		if _, err := fmt.Fprintf(effectsErr, "effects: %d open claim(s): each blocks its key until `effects resolve` records what became of it\n", open); err != nil {
			return fmt.Errorf("effects list: write: %w", err)
		}
	}
	if ignored > 0 {
		if _, err := fmt.Fprintf(effectsErr, "effects: replay did not apply %d effect record(s) that lost a race\n", ignored); err != nil {
			return fmt.Errorf("effects list: write: %w", err)
		}
	}
	return nil
}

// runEffectsResolve records what became of an open claim: done when the effect did happen,
// retry when it did not and may be attempted once more.
func runEffectsResolve(args []string) error {
	fs := flag.NewFlagSet("effects resolve", flag.ContinueOnError)
	outcome := fs.String("outcome", "", "done (the effect did happen) or retry (it did not; it may be attempted once more)")
	by := fs.String("by", "", "who decided")
	note := fs.String("note", "", "how it was checked")
	ledger, pos, ctx, cancel, err := effectsCall(fs, args, "task id", "action type")
	if err != nil {
		return err
	}
	defer cancel()
	if *outcome == "" {
		return fmt.Errorf("effects resolve: --outcome is required")
	}
	return ledger.Resolve(ctx, effects.Key{TaskID: pos[0], ActionType: pos[1]}, *outcome, *by, *note)
}
