package units

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/cordanaLLM/tribunus/internal/eventlog"
	"github.com/golusoris/golusoris/core/clock"
	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
	"github.com/jonboulle/clockwork"
)

type Service struct {
	dir    string
	signer *corereceipt.Signer
	limits eventlog.Limits
}

func NewService(dir string, signer *corereceipt.Signer, clk clock.Clock, limits eventlog.Limits) (*Service, error) {
	if dir == "" {
		return nil, errors.New("units: dir is required")
	}
	if signer == nil {
		return nil, errors.New("units: signer is required")
	}
	if clk == nil {
		clk = clockwork.NewRealClock()
	}
	limits.Clock = clk
	return &Service{dir: dir, signer: signer, limits: limits}, nil
}

// Replay rebuilds the run record. A log directory that holds no events yet is an empty
// record, not an error.
func (s *Service) Replay(ctx context.Context) (eventlog.State, error) {
	if err := readyContext(ctx, "replay"); err != nil {
		return eventlog.State{}, err
	}
	events := filepath.Join(s.dir, "events")
	if _, err := os.Stat(events); os.IsNotExist(err) {
		return eventlog.State{Units: map[string]eventlog.Unit{}}, nil
	} else if err != nil {
		return eventlog.State{}, fmt.Errorf("units: stat %s: %w", events, err)
	}
	return eventlog.ReplayStable(ctx, s.dir, s.signer.PublicKey(), s.limits, UnitReducer)
}

// Record appends one stage record and returns the unit as it stands after it.
func (s *Service) Record(ctx context.Context, id string, payload RecordPayload) (eventlog.Unit, error) {
	if err := readyContext(ctx, "record"); err != nil {
		return eventlog.Unit{}, err
	}
	if !IsValidID(id) {
		return eventlog.Unit{}, fmt.Errorf("units: invalid unit id %q", id)
	}
	if err := validateRecordPayload(payload); err != nil {
		return eventlog.Unit{}, err
	}
	st, err := s.Replay(ctx)
	if err != nil {
		return eventlog.Unit{}, err
	}
	if err = validateRecordState(st, id, payload.Stage, payload.Reopen); err != nil {
		return eventlog.Unit{}, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return eventlog.Unit{}, fmt.Errorf("units: marshal payload: %w", err)
	}
	updated, err := s.appendAndReduce(ctx, st, eventlog.Event{Type: "unit.recorded", TaskID: id, Payload: body})
	if err != nil {
		return eventlog.Unit{}, err
	}
	return updated.Units[id], nil
}

func (s *Service) appendAndReduce(ctx context.Context, st eventlog.State, ev eventlog.Event) (eventlog.State, error) {
	writer, err := eventlog.Open(s.dir, s.signer, s.limits)
	if err != nil {
		return eventlog.State{}, err
	}
	rec, err := writer.Append(ctx, ev)
	if err != nil {
		return eventlog.State{}, err
	}
	return UnitReducer(st, rec)
}

func validateRecordState(st eventlog.State, id string, stage string, reopen bool) error {
	existing, exists := st.Units[id]
	if !exists && len(st.Units) >= MaxUnits {
		return fmt.Errorf("units: the run record already holds %d units", MaxUnits)
	}
	if exists && IsTerminalStage(existing.Stage) && !IsTerminalStage(stage) && !reopen {
		return fmt.Errorf("units: %q is %s; pass --reopen to move it again", id, existing.Stage)
	}
	return nil
}

// Note appends a note to a unit's inbox. The unit reads it at its next stage boundary.
func (s *Service) Note(ctx context.Context, id string, from string, text string) (eventlog.UnitNote, error) {
	if err := readyContext(ctx, "note"); err != nil {
		return eventlog.UnitNote{}, err
	}
	if err := validateNoteInput(id, from, text); err != nil {
		return eventlog.UnitNote{}, err
	}
	st, err := s.Replay(ctx)
	if err != nil {
		return eventlog.UnitNote{}, err
	}
	unit, exists := st.Units[id]
	if !exists {
		return eventlog.UnitNote{}, fmt.Errorf("units: unit %q does not exist", id)
	}
	if len(UnreadNotes(unit)) >= MaxUnreadNotes {
		return eventlog.UnitNote{}, fmt.Errorf("units: %q already has %d unread notes", id, MaxUnreadNotes)
	}
	body, err := json.Marshal(NotePayload{From: from, Text: text})
	if err != nil {
		return eventlog.UnitNote{}, fmt.Errorf("units: marshal note payload: %w", err)
	}
	updated, err := s.appendAndReduce(ctx, st, eventlog.Event{Type: "unit.note", TaskID: id, Payload: body})
	if err != nil {
		return eventlog.UnitNote{}, err
	}
	notes := updated.Units[id].Notes
	if len(notes) == 0 {
		return eventlog.UnitNote{}, fmt.Errorf("units: note for %q was not recorded", id)
	}
	return notes[len(notes)-1], nil
}

// Inbox returns the unread notes of a unit and marks them read. Nothing unread appends
// nothing.
func (s *Service) Inbox(ctx context.Context, id string) ([]eventlog.UnitNote, error) {
	if err := readyContext(ctx, "inbox"); err != nil {
		return nil, err
	}
	unit, err := s.Show(ctx, id)
	if err != nil {
		return nil, err
	}
	unread := UnreadNotes(unit)
	if len(unread) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(NoteReadPayload{UptoSeq: unread[len(unread)-1].Seq})
	if err != nil {
		return nil, fmt.Errorf("units: marshal note read payload: %w", err)
	}
	writer, err := eventlog.Open(s.dir, s.signer, s.limits)
	if err != nil {
		return nil, err
	}
	if _, err = writer.Append(ctx, eventlog.Event{Type: "unit.note_read", TaskID: id, Payload: body}); err != nil {
		return nil, err
	}
	return unread, nil
}

// Show returns one unit as the log records it.
func (s *Service) Show(ctx context.Context, id string) (eventlog.Unit, error) {
	if err := readyContext(ctx, "show"); err != nil {
		return eventlog.Unit{}, err
	}
	if !IsValidID(id) {
		return eventlog.Unit{}, fmt.Errorf("units: invalid unit id %q", id)
	}
	st, err := s.Replay(ctx)
	if err != nil {
		return eventlog.Unit{}, err
	}
	unit, exists := st.Units[id]
	if !exists {
		return eventlog.Unit{}, fmt.Errorf("units: unit %q does not exist", id)
	}
	return unit, nil
}

// Resume lists the open units, sorted by id, each with whether it is safe to relaunch,
// and the number of unit records replay did not apply.
func (s *Service) Resume(ctx context.Context) ([]ResumeRow, int, error) {
	if err := readyContext(ctx, "resume"); err != nil {
		return nil, 0, err
	}
	st, err := s.Replay(ctx)
	if err != nil {
		return nil, 0, err
	}
	openIDs := make([]string, 0, len(st.Units))
	for id, u := range st.Units {
		if !IsTerminalStage(u.Stage) {
			openIDs = append(openIDs, id)
		}
	}
	sort.Strings(openIDs)
	rows := make([]ResumeRow, 0, len(openIDs))
	for i := 0; i < len(openIDs); i++ {
		u := st.Units[openIDs[i]]
		relaunch := "safe"
		if safe, reason := RelaunchRule(u); !safe {
			relaunch = "no: " + reason
		}
		rows = append(rows, ResumeRow{ID: u.ID, Stage: u.Stage, UpdatedAt: u.UpdatedAt, Worktree: u.Worktree,
			Branch: u.Branch, PR: u.PR, Lane: u.Lane, Relaunch: relaunch})
	}
	return rows, st.UnitRecordsIgnored, nil
}

func readyContext(ctx context.Context, op string) error {
	if ctx == nil {
		return fmt.Errorf("units: %s: nil context", op)
	}
	if _, ok := ctx.Deadline(); !ok {
		return fmt.Errorf("units: %s requires a context deadline", op)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("units: %s context: %w", op, err)
	}
	return nil
}
