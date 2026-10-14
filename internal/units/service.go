package units

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
	"github.com/tekmora/epitrop/internal/eventlog"
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

// appendChecked opens an eventlog writer and appends the event returned by build.
// The check runs under the log's lock (#108).
func (s *Service) appendChecked(ctx context.Context, build func(eventlog.State) (eventlog.Event, error)) (eventlog.State, error) {
	writer, err := eventlog.Open(s.dir, s.signer, s.limits)
	if err != nil {
		return eventlog.State{}, err
	}
	var capturedSt eventlog.State
	rec, err := writer.AppendChecked(ctx, func(cctx context.Context) (eventlog.Event, error) {
		st, replayErr := s.Replay(cctx)
		if replayErr != nil {
			return eventlog.Event{}, replayErr
		}
		ev, buildErr := build(st)
		if buildErr != nil {
			return eventlog.Event{}, buildErr
		}
		capturedSt = st
		return ev, nil
	})
	if err != nil {
		return eventlog.State{}, err
	}
	return UnitReducer(capturedSt, rec)
}

// Record appends one status record and returns the unit as it stands after it.
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
	body, err := json.Marshal(payload)
	if err != nil {
		return eventlog.Unit{}, fmt.Errorf("units: marshal payload: %w", err)
	}
	st, err := s.appendChecked(ctx, func(st eventlog.State) (eventlog.Event, error) {
		if valErr := validateRecordState(st, id, payload.Status, payload.Reopen); valErr != nil {
			return eventlog.Event{}, valErr
		}
		return eventlog.Event{Type: "unit.recorded", TaskID: id, Payload: body}, nil
	})
	if err != nil {
		return eventlog.Unit{}, err
	}
	return st.Units[id], nil
}

func validateRecordState(st eventlog.State, id string, status string, reopen bool) error {
	existing, exists := st.Units[id]
	if !exists {
		if len(st.Units) >= MaxUnits {
			return fmt.Errorf("units: the run record already holds %d units", MaxUnits)
		}
		return nil
	}
	if err := transitionRefusal(existing.Status, status, reopen); err != nil {
		if IsTerminalStatus(existing.Status) {
			return fmt.Errorf("units: %q is %s; pass --reopen to move it again", id, existing.Status)
		}
		return fmt.Errorf("units: %q: %w", id, err)
	}
	return nil
}

func validateNoteState(st eventlog.State, id string) error {
	unit, exists := st.Units[id]
	if !exists {
		return fmt.Errorf("units: unit %q does not exist", id)
	}
	if len(UnreadNotes(unit)) >= MaxUnreadNotes {
		return fmt.Errorf("units: %q already has %d unread notes", id, MaxUnreadNotes)
	}
	return nil
}

// Note appends a note to a unit's inbox. The unit reads it at its next status record.
func (s *Service) Note(ctx context.Context, id string, from string, text string) (eventlog.UnitNote, error) {
	if err := readyContext(ctx, "note"); err != nil {
		return eventlog.UnitNote{}, err
	}
	if err := validateNoteInput(id, from, text); err != nil {
		return eventlog.UnitNote{}, err
	}
	body, err := json.Marshal(NotePayload{From: from, Text: text})
	if err != nil {
		return eventlog.UnitNote{}, fmt.Errorf("units: marshal note payload: %w", err)
	}
	st, err := s.appendChecked(ctx, func(st eventlog.State) (eventlog.Event, error) {
		if validateErr := validateNoteState(st, id); validateErr != nil {
			return eventlog.Event{}, validateErr
		}
		return eventlog.Event{Type: "unit.note", TaskID: id, Payload: body}, nil
	})
	if err != nil {
		return eventlog.UnitNote{}, err
	}
	notes := st.Units[id].Notes
	if len(notes) == 0 {
		return eventlog.UnitNote{}, fmt.Errorf("units: note for %q was not recorded", id)
	}
	return notes[len(notes)-1], nil
}

var errNothingUnread = errors.New("units: nothing unread")

// Inbox returns the unread notes of a unit and marks them read. Nothing unread appends
// nothing.
func (s *Service) Inbox(ctx context.Context, id string) ([]eventlog.UnitNote, error) {
	if err := readyContext(ctx, "inbox"); err != nil {
		return nil, err
	}
	if !IsValidID(id) {
		return nil, fmt.Errorf("units: invalid unit id %q", id)
	}
	var unread []eventlog.UnitNote
	_, err := s.appendChecked(ctx, func(st eventlog.State) (eventlog.Event, error) {
		unit, exists := st.Units[id]
		if !exists {
			return eventlog.Event{}, fmt.Errorf("units: unit %q does not exist", id)
		}
		notes := UnreadNotes(unit)
		if len(notes) == 0 {
			return eventlog.Event{}, errNothingUnread
		}
		body, marshalErr := json.Marshal(NoteReadPayload{UptoSeq: notes[len(notes)-1].Seq})
		if marshalErr != nil {
			return eventlog.Event{}, fmt.Errorf("units: marshal note read payload: %w", marshalErr)
		}
		unread = notes
		return eventlog.Event{Type: "unit.note_read", TaskID: id, Payload: body}, nil
	})
	if errors.Is(err, errNothingUnread) {
		return nil, nil
	}
	if err != nil {
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
		if !IsTerminalStatus(u.Status) {
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
		evidence := ""
		if u.Evidence != nil {
			evidence = u.Evidence.Path
		}
		rows = append(rows, ResumeRow{ID: u.ID, Status: u.Status, UpdatedAt: u.UpdatedAt, Worktree: u.Worktree,
			Branch: u.Branch, PR: u.PR, Lane: u.Lane, Relaunch: relaunch, Evidence: evidence})
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
