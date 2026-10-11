// Package effects is the side-effect ledger of RT-02: every outbound side effect has an
// idempotency key of task and action type, recorded in the event log, so that a second
// delivery of the same key is skipped and recorded and never runs the effect twice.
package effects

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/tekmora/epitrop/internal/eventlog"
)

// The record types of the ledger. The record's task id is the key's task; the payload names
// the action type.
const (
	Claimed  = "effect.claimed"
	Done     = "effect.done"
	Failed   = "effect.failed"
	Skipped  = "effect.skipped"
	Resolved = "effect.resolved"
)

// The states of an effect. Claimed and failed are open.
const (
	StateClaimed  = "claimed"
	StateDone     = "done"
	StateFailed   = "failed"
	StateReleased = "released"
)

// The outcomes a resolution may record for an open claim: the effect did happen, or it may
// be attempted once more.
const (
	OutcomeDone  = "done"
	OutcomeRetry = "retry"
)

const (
	// MaxEffects bounds the keys one log may hold.
	MaxEffects = 100000
	// MaxReasonBytes bounds the error text of a failed attempt and the note of a resolution.
	MaxReasonBytes = 2048
)

var keyPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Key is the idempotency key of one side effect.
type Key struct {
	TaskID     string
	ActionType string
}

// Valid reports whether both parts are names the ledger takes: letters, digits, dot,
// underscore and hyphen, at most 128 bytes, starting with a letter or digit.
func (k Key) Valid() bool {
	return keyPart.MatchString(k.TaskID) && keyPart.MatchString(k.ActionType)
}

// String is the key as the replayed state indexes it. Neither part can hold a space.
func (k Key) String() string {
	return k.TaskID + " " + k.ActionType
}

// payload is the shape of every ledger record; each type uses the fields it needs.
type payload struct {
	ActionType string `json:"action_type"`
	ClaimSeq   uint64 `json:"claim_seq,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
	By         string `json:"by,omitempty"`
	Note       string `json:"note,omitempty"`
}

// Reducer rebuilds the ledger from the shared log. It skips the other known domains and
// fails on a record no writer of this version could have produced: an unknown type, a bad
// key, a payload that does not parse, an unknown outcome, a resolution without a name.
//
// It never fails on a record two honest writers can produce by racing, such as a second
// claim of a key or the end of a claim that is no longer the current one. Such a record is
// not applied and is counted in State.EffectRecordsIgnored, the same way on every replay.
// The first claim of a key in log order is the one that holds: that is what lets a writer
// find out, by replaying after its own append, whether it owns the claim.
func Reducer(st eventlog.State, rec eventlog.Record) (eventlog.State, error) {
	if !strings.HasPrefix(rec.Type, "effect.") {
		if eventlog.KnownDomain(rec.Type) {
			return st, nil
		}
		return eventlog.State{}, fmt.Errorf("eventlog: seq %d: unknown event type %q", rec.Seq, rec.Type)
	}
	body, key, err := readRecord(rec)
	if err != nil {
		return eventlog.State{}, err
	}
	if st.Effects == nil {
		st.Effects = map[string]eventlog.Effect{}
	}
	current, exists := st.Effects[key.String()]
	next, applied := apply(current, exists, len(st.Effects), key, rec, body)
	if !applied {
		st.EffectRecordsIgnored++
		return st, nil
	}
	st.Effects[key.String()] = next
	return st, nil
}

// readRecord parses a ledger record and refuses what no writer produces.
func readRecord(rec eventlog.Record) (payload, Key, error) {
	var body payload
	dec := json.NewDecoder(bytes.NewReader(rec.Payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		return payload{}, Key{}, fmt.Errorf("eventlog: seq %d: payload: %w", rec.Seq, err)
	}
	key := Key{TaskID: rec.TaskID, ActionType: body.ActionType}
	if !key.Valid() {
		return payload{}, Key{}, fmt.Errorf("eventlog: seq %d: invalid effect key %q", rec.Seq, key.String())
	}
	switch rec.Type {
	case Claimed, Done, Failed, Skipped:
		return body, key, nil
	case Resolved:
		if body.Outcome != OutcomeDone && body.Outcome != OutcomeRetry {
			return payload{}, Key{}, fmt.Errorf("eventlog: seq %d: unknown outcome %q", rec.Seq, body.Outcome)
		}
		if body.By == "" {
			return payload{}, Key{}, fmt.Errorf("eventlog: seq %d: a resolution names who made it", rec.Seq)
		}
		return body, key, nil
	}
	return payload{}, Key{}, fmt.Errorf("eventlog: seq %d: unknown event type %q", rec.Seq, rec.Type)
}

// apply returns the effect after the record, and false when the record does not apply to
// the effect as it stands.
func apply(current eventlog.Effect, exists bool, count int, key Key, rec eventlog.Record, body payload) (eventlog.Effect, bool) {
	switch rec.Type {
	case Claimed:
		return applyClaim(current, exists, count, key, rec)
	case Done, Failed:
		return applyEnd(current, rec, body)
	case Skipped:
		if current.State != StateDone {
			return current, false
		}
		current.Skipped++
		return current, true
	}
	return applyResolution(current, body)
}

// applyClaim takes a claim for a key that is new or was released for a retry. A claim of a
// key that is claimed, failed or done loses.
func applyClaim(current eventlog.Effect, exists bool, count int, key Key, rec eventlog.Record) (eventlog.Effect, bool) {
	if exists && current.State != StateReleased {
		return current, false
	}
	if !exists && count >= MaxEffects {
		return current, false
	}
	current.TaskID, current.ActionType = key.TaskID, key.ActionType
	current.State = StateClaimed
	current.ClaimSeq = rec.Seq
	current.Attempt++
	current.Reason, current.ResolvedBy, current.ResolvedNote = "", "", ""
	return current, true
}

// applyEnd ends the current claim. The end of any other claim, or of an effect that is not
// claimed, does not apply.
func applyEnd(current eventlog.Effect, rec eventlog.Record, body payload) (eventlog.Effect, bool) {
	if current.State != StateClaimed || body.ClaimSeq != current.ClaimSeq {
		return current, false
	}
	if rec.Type == Done {
		current.State = StateDone
		return current, true
	}
	current.State = StateFailed
	current.Reason = body.Reason
	return current, true
}

// applyResolution resolves an open claim: the effect did happen, or it may be claimed once
// more. An effect that is not open has nothing to resolve.
func applyResolution(current eventlog.Effect, body payload) (eventlog.Effect, bool) {
	if current.State != StateClaimed && current.State != StateFailed {
		return current, false
	}
	current.State = StateDone
	if body.Outcome == OutcomeRetry {
		current.State = StateReleased
	}
	current.ResolvedBy, current.ResolvedNote = body.By, body.Note
	return current, true
}
