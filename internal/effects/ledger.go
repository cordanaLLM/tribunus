package effects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/golusoris/golusoris/core/clock"
	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
	"github.com/jonboulle/clockwork"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/tekmora/epitrop/internal/eventlog"
	"github.com/tekmora/epitrop/internal/telemetry"
)

var (
	// ErrUnresolved says the key has an open claim: an attempt started and did not end as
	// done. The effect may have happened, so nothing runs it again until someone resolves
	// the claim with a recorded act.
	ErrUnresolved = errors.New("effects: the key has an open claim")
	// ErrClaimedElsewhere says another writer claimed the key first. This delivery did not
	// run the effect.
	ErrClaimedElsewhere = errors.New("effects: another writer holds the claim")
)

// Result says what a delivery did.
type Result struct {
	// Executed is true when this delivery ran the effect.
	Executed bool
	// Skipped is true when the effect was done before and this delivery only recorded that
	// it came again.
	Skipped bool
}

// Ledger records outbound side effects in the event log and runs each key at most once.
type Ledger struct {
	dir    string
	signer *corereceipt.Signer
	limits eventlog.Limits
}

// NewLedger returns the ledger for the log in dir.
func NewLedger(dir string, signer *corereceipt.Signer, clk clock.Clock, limits eventlog.Limits) (*Ledger, error) {
	if dir == "" {
		return nil, errors.New("effects: dir is required")
	}
	if signer == nil {
		return nil, errors.New("effects: signer is required")
	}
	if clk == nil {
		clk = clockwork.NewRealClock()
	}
	limits.Clock = clk
	return &Ledger{dir: dir, signer: signer, limits: limits}, nil
}

// Replay rebuilds the state the log holds. A log directory without events yet is an empty
// state, not an error.
func (l *Ledger) Replay(ctx context.Context) (eventlog.State, error) {
	if err := readyContext(ctx, "replay"); err != nil {
		return eventlog.State{}, err
	}
	events := filepath.Join(l.dir, "events")
	if _, err := os.Stat(events); os.IsNotExist(err) {
		return eventlog.State{}, nil
	} else if err != nil {
		return eventlog.State{}, fmt.Errorf("effects: stat %s: %w", events, err)
	}
	return eventlog.ReplayStable(ctx, l.dir, l.signer.PublicKey(), l.limits, Reducer)
}

// Do runs the side effect of key at most once.
//
// A key that is done is not run again: the delivery is recorded as skipped. A key with an
// open claim, one that was claimed and never ended or that ended as failed, returns
// ErrUnresolved and appends nothing: whether the effect happened is not known, and only a
// recorded resolution may say so. Otherwise Do claims the key, checks that its claim is the
// one the log holds, runs fn and records the end.
//
// The claim is checked after it is appended, by a second replay. Two writers that both
// found the key free both append a claim; the log orders them, the first one holds, and the
// second gets ErrClaimedElsewhere without running fn.
//
// When fn has run and its end cannot be recorded, the claim stays open. That is the same
// state as a crash between the claim and the end, and it is resolved the same way.
func (l *Ledger) Do(ctx context.Context, key Key, fn func(context.Context) error) (Result, error) {
	if err := readyContext(ctx, "do"); err != nil {
		return Result{}, err
	}
	if !key.Valid() {
		return Result{}, fmt.Errorf("effects: invalid key %q", key.String())
	}
	if fn == nil {
		return Result{}, errors.New("effects: do: nil effect")
	}
	ctx, span := telemetry.Default().StartSpan(ctx, "effects.do", attribute.String("epitrop.effect.key", key.String()))
	defer span.End()

	st, err := l.Replay(ctx)
	if err != nil {
		return l.recordOutcome(ctx, span, Result{}, "failed", err)
	}
	switch st.Effects[key.String()].State {
	case StateDone:
		_, err = l.append(ctx, Skipped, key, payload{})
		outcome := "skipped"
		if err != nil {
			outcome = "failed"
		}
		return l.recordOutcome(ctx, span, Result{Skipped: err == nil}, outcome, err)
	case StateClaimed, StateFailed:
		return l.recordOutcome(ctx, span, Result{}, "refused", unresolved(st.Effects[key.String()]))
	}
	res, outcome, err := l.claimAndRun(ctx, key, fn)
	return l.recordOutcome(ctx, span, res, outcome, err)
}

func (l *Ledger) claimAndRun(ctx context.Context, key Key, fn func(context.Context) error) (Result, string, error) {
	claimSeq, err := l.claim(ctx, key)
	if err != nil {
		if errors.Is(err, ErrClaimedElsewhere) {
			return Result{}, "refused", err
		}
		return Result{}, "failed", err
	}
	res, err := l.run(ctx, key, claimSeq, fn)
	if err != nil {
		return res, "failed", err
	}
	return res, "executed", nil
}

func (l *Ledger) recordOutcome(ctx context.Context, span trace.Span, res Result, outcome string, err error) (Result, error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	telemetry.Default().EffectRun(ctx, outcome)
	return res, err
}

// claim appends a claim of key and returns its sequence number when the log holds it as the
// key's claim.
func (l *Ledger) claim(ctx context.Context, key Key) (uint64, error) {
	rec, err := l.append(ctx, Claimed, key, payload{})
	if err != nil {
		return 0, err
	}
	st, err := l.Replay(ctx)
	if err != nil {
		return 0, fmt.Errorf("effects: claim of %q was appended but could not be checked, it stays open: %w", key.String(), err)
	}
	held := st.Effects[key.String()]
	if held.State != StateClaimed || held.ClaimSeq != rec.Seq {
		return 0, fmt.Errorf("%w: %q", ErrClaimedElsewhere, key.String())
	}
	return rec.Seq, nil
}

// run executes fn under the claim and records how it ended.
func (l *Ledger) run(ctx context.Context, key Key, claimSeq uint64, fn func(context.Context) error) (Result, error) {
	if runErr := fn(ctx); runErr != nil {
		reason := runErr.Error()
		if len(reason) > MaxReasonBytes {
			reason = reason[:MaxReasonBytes]
		}
		if _, err := l.append(ctx, Failed, key, payload{ClaimSeq: claimSeq, Reason: reason}); err != nil {
			return Result{Executed: true}, errors.Join(runErr, fmt.Errorf("effects: the failure of %q could not be recorded, its claim stays open: %w", key.String(), err))
		}
		return Result{Executed: true}, runErr
	}
	if _, err := l.append(ctx, Done, key, payload{ClaimSeq: claimSeq}); err != nil {
		return Result{Executed: true}, fmt.Errorf("effects: %q ran but its end could not be recorded, its claim stays open: %w", key.String(), err)
	}
	return Result{Executed: true}, nil
}

// Status returns every effect the log holds, in key order, and the number of effect records
// the replay ignored.
func (l *Ledger) Status(ctx context.Context) ([]eventlog.Effect, int, error) {
	st, err := l.Replay(ctx)
	if err != nil {
		return nil, 0, err
	}
	all := make([]eventlog.Effect, 0, len(st.Effects))
	for _, effect := range st.Effects {
		all = append(all, effect)
	}
	sort.Slice(all, func(i, j int) bool {
		return Key{all[i].TaskID, all[i].ActionType}.String() < Key{all[j].TaskID, all[j].ActionType}.String()
	})
	return all, st.EffectRecordsIgnored, nil
}

// Open returns the effects with an open claim, in key order. They block whatever delivers
// their key until Resolve records what became of them; they do not age out.
func (l *Ledger) Open(ctx context.Context) ([]eventlog.Effect, error) {
	all, _, err := l.Status(ctx)
	if err != nil {
		return nil, err
	}
	open := make([]eventlog.Effect, 0, len(all))
	for i := 0; i < len(all); i++ {
		if IsOpen(all[i]) {
			open = append(open, all[i])
		}
	}
	return open, nil
}

// IsOpen reports whether the effect has a claim that did not end as done: it was claimed and
// never ended, or it failed.
func IsOpen(effect eventlog.Effect) bool {
	return effect.State == StateClaimed || effect.State == StateFailed
}

func validateResolveParams(ctx context.Context, key Key, outcome string, by string, note string) error {
	if err := readyContext(ctx, "resolve"); err != nil {
		return err
	}
	if !key.Valid() {
		return fmt.Errorf("effects: invalid key %q", key.String())
	}
	if outcome != OutcomeDone && outcome != OutcomeRetry {
		return fmt.Errorf("effects: unknown outcome %q, want %s or %s", outcome, OutcomeDone, OutcomeRetry)
	}
	if by == "" {
		return errors.New("effects: a resolution names who made it")
	}
	if len(note) > MaxReasonBytes {
		return fmt.Errorf("effects: the note is longer than %d bytes", MaxReasonBytes)
	}
	return nil
}

// Resolve records what became of an open claim: OutcomeDone when the effect did happen,
// OutcomeRetry when it did not and may be attempted once more. by names who decided.
func (l *Ledger) Resolve(ctx context.Context, key Key, outcome string, by string, note string) error {
	if err := validateResolveParams(ctx, key, outcome, by, note); err != nil {
		return err
	}
	raw, err := json.Marshal(payload{Outcome: outcome, By: by, Note: note, ActionType: key.ActionType})
	if err != nil {
		return fmt.Errorf("effects: marshal %s: %w", Resolved, err)
	}
	_, err = l.appendChecked(ctx, func(cctx context.Context) (eventlog.Event, error) {
		st, replayErr := l.Replay(cctx)
		if replayErr != nil {
			return eventlog.Event{}, replayErr
		}
		state := st.Effects[key.String()].State
		if state != StateClaimed && state != StateFailed {
			return eventlog.Event{}, fmt.Errorf("effects: %q has no open claim to resolve", key.String())
		}
		return eventlog.Event{Type: Resolved, TaskID: key.TaskID, Payload: raw}, nil
	})
	return err
}

func (l *Ledger) append(ctx context.Context, typ string, key Key, body payload) (eventlog.Record, error) {
	body.ActionType = key.ActionType
	raw, err := json.Marshal(body)
	if err != nil {
		return eventlog.Record{}, fmt.Errorf("effects: marshal %s: %w", typ, err)
	}
	return l.appendChecked(ctx, func(context.Context) (eventlog.Event, error) {
		return eventlog.Event{Type: typ, TaskID: key.TaskID, Payload: raw}, nil
	})
}

func (l *Ledger) appendChecked(ctx context.Context, check func(context.Context) (eventlog.Event, error)) (eventlog.Record, error) {
	writer, err := eventlog.Open(l.dir, l.signer, l.limits)
	if err != nil {
		return eventlog.Record{}, err
	}
	return writer.AppendChecked(ctx, check)
}

func unresolved(effect eventlog.Effect) error {
	key := Key{TaskID: effect.TaskID, ActionType: effect.ActionType}
	if effect.State == StateFailed {
		return fmt.Errorf("%w: %q failed in attempt %d: %s", ErrUnresolved, key.String(), effect.Attempt, effect.Reason)
	}
	return fmt.Errorf("%w: %q was claimed in attempt %d and never ended", ErrUnresolved, key.String(), effect.Attempt)
}

func readyContext(ctx context.Context, op string) error {
	if ctx == nil {
		return fmt.Errorf("effects: %s: nil context", op)
	}
	if _, ok := ctx.Deadline(); !ok {
		return fmt.Errorf("effects: %s requires a context deadline", op)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("effects: %s context: %w", op, err)
	}
	return nil
}
