package eventlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golusoris/golusoris/core/clock"
	corereceipt "github.com/golusoris/golusoris/core/crypto/receipt"
)

const (
	AppendCommand  = "tribunus event-log append"
	MaxRecordBytes = 64 * 1024

	defaultLockTimeout = 5 * time.Second
	defaultReplayCap   = 100000
	zeroHash           = "0000000000000000000000000000000000000000000000000000000000000000"
)

type Limits struct {
	Clock            clock.Clock
	LockTimeout      time.Duration
	MaxReplayRecords int
	MaxRecordBytes   int
}

type Event struct {
	Type    string          `json:"type"`
	TaskID  string          `json:"task_id"`
	Payload json.RawMessage `json:"payload"`
}

type Record struct {
	Seq     uint64               `json:"seq"`
	Prev    string               `json:"prev"`
	Time    string               `json:"time"`
	Type    string               `json:"type"`
	TaskID  string               `json:"task_id"`
	Payload json.RawMessage      `json:"payload"`
	Receipt *corereceipt.Receipt `json:"receipt"`
}

type State struct {
	Tasks map[string]Task `json:"tasks"`
	Jobs  map[string]Job  `json:"jobs,omitempty"`
	Units map[string]Unit `json:"units,omitempty"`
	// UnitRecordsIgnored counts well-formed unit records the reducer did not apply because
	// they broke a rule a racing writer can break, such as moving a landed unit.
	UnitRecordsIgnored int `json:"unit_records_ignored,omitempty"`
}

type Task struct {
	ID      string          `json:"id"`
	State   string          `json:"state"`
	Payload json.RawMessage `json:"payload"`
}

type Job struct {
	Name         string `json:"name"`
	State        string `json:"state"`
	PID          int    `json:"pid,omitempty"`
	ShimPID      int    `json:"shim_pid,omitempty"`
	Since        string `json:"since,omitempty"`
	Restarts     int    `json:"restarts"`
	LastExitCode *int   `json:"last_exit_code,omitempty"`
	LastReason   string `json:"last_reason,omitempty"`
	LastEvent    string `json:"last_event"`
	Sandbox      string `json:"sandbox,omitempty"`
}

// UnitNote is one unread inbox note. Seq is the log sequence number of its record.
type UnitNote struct {
	Seq  uint64 `json:"seq"`
	From string `json:"from"`
	Text string `json:"text"`
	At   string `json:"at"`
}

// Unit is one work unit of the run record. Lane, Target, ResolvedModel, Branch, Identity
// and IdentityKey carry the names and meanings of Praetor's run identity, so both records
// join without a mapping; Identity is stored as given and IdentityKey is never recomputed.
type Unit struct {
	ID            string          `json:"id"`
	Stage         string          `json:"stage"`
	Worktree      string          `json:"worktree,omitempty"`
	Branch        string          `json:"branch,omitempty"`
	PR            int             `json:"pr,omitempty"`
	Lane          string          `json:"lane,omitempty"`
	By            string          `json:"by,omitempty"`
	Target        string          `json:"target,omitempty"`
	ResolvedModel string          `json:"resolved_model,omitempty"`
	Identity      json.RawMessage `json:"identity,omitempty"`
	IdentityKey   string          `json:"identity_key,omitempty"`
	UpdatedAt     string          `json:"updated_at,omitempty"`
	Notes         []UnitNote      `json:"notes,omitempty"`
	ReadUpto      uint64          `json:"read_upto,omitempty"`
}

type Reducer func(State, Record) (State, error)

type Writer struct {
	dir       string
	eventsDir string
	signer    *corereceipt.Signer
	limits    Limits
}

var (
	ErrRecordTooLarge = errors.New("eventlog: record exceeds 64 KiB")
	ErrLockTimeout    = errors.New("eventlog: lock timeout")
)

func Open(dir string, signer *corereceipt.Signer, limits Limits) (*Writer, error) {
	if dir == "" {
		return nil, errors.New("eventlog: dir is required")
	}
	if signer == nil {
		return nil, errors.New("eventlog: signer is required")
	}
	normalized, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	return &Writer{dir: dir, eventsDir: eventsDir(dir), signer: signer, limits: normalized}, nil
}

func (s State) CanonicalJSON() ([]byte, error) {
	return canonicalJSON(s)
}

func KnownDomain(eventType string) bool {
	return strings.HasPrefix(eventType, "task.") ||
		strings.HasPrefix(eventType, "job.") ||
		strings.HasPrefix(eventType, "unit.")
}

func TaskReducer(st State, rec Record) (State, error) {
	if st.Tasks == nil {
		st.Tasks = map[string]Task{}
	}
	if !strings.HasPrefix(rec.Type, "task.") {
		if KnownDomain(rec.Type) {
			return st, nil
		}
		return State{}, fmt.Errorf("eventlog: seq %d: unknown event type %q", rec.Seq, rec.Type)
	}
	if rec.TaskID == "" {
		return State{}, fmt.Errorf("eventlog: seq %d: task_id is required", rec.Seq)
	}
	switch rec.Type {
	case "task.created":
		return reduceCreated(st, rec)
	case "task.state_changed":
		return reduceStateChanged(st, rec)
	}
	return State{}, fmt.Errorf("eventlog: seq %d: unknown event type %q", rec.Seq, rec.Type)
}

func reduceCreated(st State, rec Record) (State, error) {
	payload, state, err := parseTaskPayload(rec)
	if err != nil {
		return State{}, err
	}
	st.Tasks[rec.TaskID] = Task{ID: rec.TaskID, State: state, Payload: payload}
	return st, nil
}

func reduceStateChanged(st State, rec Record) (State, error) {
	state, err := stateFromPayload(rec)
	if err != nil {
		return State{}, err
	}
	task, ok := st.Tasks[rec.TaskID]
	if !ok {
		return State{}, fmt.Errorf("eventlog: seq %d: task %q does not exist", rec.Seq, rec.TaskID)
	}
	task.State = state
	st.Tasks[rec.TaskID] = task
	return st, nil
}

func parseTaskPayload(rec Record) (json.RawMessage, string, error) {
	payload, err := canonicalPayload(rec.Payload)
	if err != nil {
		return nil, "", fmt.Errorf("eventlog: seq %d: payload: %w", rec.Seq, err)
	}
	state, err := stateFromPayload(rec)
	if err != nil {
		return nil, "", err
	}
	return payload, state, nil
}

func stateFromPayload(rec Record) (string, error) {
	var payload struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(rec.Payload, &payload); err != nil {
		return "", fmt.Errorf("eventlog: seq %d: payload: %w", rec.Seq, err)
	}
	if payload.State == "" {
		return "", fmt.Errorf("eventlog: seq %d: payload.state is required", rec.Seq)
	}
	return payload.State, nil
}

func normalizeLimits(l Limits) (Limits, error) {
	if l.Clock == nil {
		return Limits{}, errors.New("eventlog: limits clock is required")
	}
	if l.LockTimeout == 0 {
		l.LockTimeout = defaultLockTimeout
	}
	if l.LockTimeout < 0 {
		return Limits{}, errors.New("eventlog: lock timeout must be non-negative")
	}
	if l.MaxReplayRecords == 0 {
		l.MaxReplayRecords = defaultReplayCap
	}
	if l.MaxReplayRecords < 0 {
		return Limits{}, errors.New("eventlog: max replay records must be non-negative")
	}
	if l.MaxRecordBytes == 0 {
		l.MaxRecordBytes = MaxRecordBytes
	}
	if l.MaxRecordBytes > MaxRecordBytes || l.MaxRecordBytes < 1 {
		return Limits{}, errors.New("eventlog: max record bytes must be 1..65536")
	}
	return l, nil
}
