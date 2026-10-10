package units

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cordanaLLM/tribunus/internal/eventlog"
)

// UnitReducer rebuilds the run record from the shared log. It skips the other known
// domains and fails on a record no writer of this version could have produced: an
// unknown type, a bad id, a payload that does not parse, an unknown status.
//
// It never fails on a rule two honest writers can break by racing, because the log is
// append-only: one such record would stop every later replay for good. The API checks
// those rules before appending, but outside the log's lock, so the reducer settles them
// the same way on every replay: the record is not applied and is counted in
// State.UnitRecordsIgnored.
func UnitReducer(st eventlog.State, rec eventlog.Record) (eventlog.State, error) {
	if st.Units == nil {
		st.Units = map[string]eventlog.Unit{}
	}
	if !strings.HasPrefix(rec.Type, "unit.") {
		if eventlog.KnownDomain(rec.Type) {
			return st, nil
		}
		return eventlog.State{}, fmt.Errorf("eventlog: seq %d: unknown event type %q", rec.Seq, rec.Type)
	}
	if !IsValidID(rec.TaskID) {
		return eventlog.State{}, fmt.Errorf("eventlog: seq %d: invalid unit id %q", rec.Seq, rec.TaskID)
	}
	switch rec.Type {
	case "unit.recorded":
		return reduceUnitRecorded(st, rec)
	case "unit.note":
		return reduceUnitNote(st, rec)
	case "unit.note_read":
		return reduceUnitNoteRead(st, rec)
	}
	return eventlog.State{}, fmt.Errorf("eventlog: seq %d: unknown event type %q", rec.Seq, rec.Type)
}

func reduceUnitRecorded(st eventlog.State, rec eventlog.Record) (eventlog.State, error) {
	var payload RecordPayload
	if err := json.Unmarshal(rec.Payload, &payload); err != nil {
		return eventlog.State{}, fmt.Errorf("eventlog: seq %d: payload: %w", rec.Seq, err)
	}
	if !IsValidStatus(payload.Status) {
		return eventlog.State{}, fmt.Errorf("eventlog: seq %d: unknown status %q", rec.Seq, payload.Status)
	}
	unit, exists := st.Units[rec.TaskID]
	if !exists && len(st.Units) >= MaxUnits {
		st.UnitRecordsIgnored++
		return st, nil
	}
	if exists && !moveAllowed(unit.Status, payload.Status, payload.Reopen) {
		st.UnitRecordsIgnored++
		return st, nil
	}
	unit.ID = rec.TaskID
	unit.Status = payload.Status
	unit.UpdatedAt = rec.Time
	applyStickyFields(&unit, payload)
	st.Units[rec.TaskID] = unit
	return st, nil
}

func applyStickyFields(unit *eventlog.Unit, payload RecordPayload) {
	targets := []struct {
		from *string
		to   *string
	}{
		{payload.Worktree, &unit.Worktree}, {payload.Branch, &unit.Branch}, {payload.Lane, &unit.Lane},
		{payload.By, &unit.By}, {payload.Target, &unit.Target}, {payload.ResolvedModel, &unit.ResolvedModel},
		{payload.IdentityKey, &unit.IdentityKey},
	}
	for i := 0; i < len(targets); i++ {
		if targets[i].from != nil {
			*targets[i].to = *targets[i].from
		}
	}
	if payload.PR != nil {
		unit.PR = *payload.PR
	}
	if payload.Evidence != nil {
		// The payload was decoded from this record alone, so its pointer is the unit's own.
		unit.Evidence = payload.Evidence
	}
	if len(payload.Identity) > 0 {
		unit.Identity = payload.Identity
		if payload.IdentityKey == nil {
			// A new identity without its key must not keep the previous identity's key.
			unit.IdentityKey = ""
		}
	}
}

// reduceUnitNote keeps a note under the sequence number of its own record: unique,
// ordered, and still valid after read notes were dropped from the state.
func reduceUnitNote(st eventlog.State, rec eventlog.Record) (eventlog.State, error) {
	var payload NotePayload
	if err := json.Unmarshal(rec.Payload, &payload); err != nil {
		return eventlog.State{}, fmt.Errorf("eventlog: seq %d: payload: %w", rec.Seq, err)
	}
	unit, exists := st.Units[rec.TaskID]
	if !exists {
		st.UnitRecordsIgnored++
		return st, nil
	}
	unit.Notes = append(unit.Notes, eventlog.UnitNote{Seq: rec.Seq, From: payload.From, Text: payload.Text, At: rec.Time})
	st.Units[rec.TaskID] = unit
	return st, nil
}

// reduceUnitNoteRead marks notes read and drops them from the state. A marker covers
// only notes that existed when it was written: it is capped at its own record's sequence
// number, so a wrong marker can never swallow a later note.
func reduceUnitNoteRead(st eventlog.State, rec eventlog.Record) (eventlog.State, error) {
	var payload NoteReadPayload
	if err := json.Unmarshal(rec.Payload, &payload); err != nil {
		return eventlog.State{}, fmt.Errorf("eventlog: seq %d: payload: %w", rec.Seq, err)
	}
	unit, exists := st.Units[rec.TaskID]
	if !exists {
		st.UnitRecordsIgnored++
		return st, nil
	}
	upto := min(payload.UptoSeq, rec.Seq)
	if upto <= unit.ReadUpto {
		return st, nil
	}
	unit.ReadUpto = upto
	unit.Notes = UnreadNotes(unit)
	st.Units[rec.TaskID] = unit
	return st, nil
}

// UnreadNotes returns the notes after the unit's read marker, oldest first.
func UnreadNotes(u eventlog.Unit) []eventlog.UnitNote {
	var unread []eventlog.UnitNote
	for i := 0; i < len(u.Notes); i++ {
		if u.Notes[i].Seq > u.ReadUpto {
			unread = append(unread, u.Notes[i])
		}
	}
	return unread
}
