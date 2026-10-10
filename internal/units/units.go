// Package units keeps the run record: one entry per work unit with its stage, written to
// the signed event log at each stage boundary, so that after a crash the open units and
// what is safe to relaunch can be read back instead of reconstructed from transcripts.
package units

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"unicode"

	"github.com/cordanaLLM/tribunus/internal/eventlog"
)

type Stage string

const (
	StagePlanned      Stage = "planned"
	StageImplementing Stage = "implementing"
	StageVerifying    Stage = "verifying"
	StageReview       Stage = "review"
	StageLanding      Stage = "landing"
	StageLanded       Stage = "landed"
	StageAbandoned    Stage = "abandoned"
	StageBlocked      Stage = "blocked"

	MaxUnreadNotes   = 256
	MaxUnits         = 10000
	MaxNoteBytes     = 4096
	MaxFieldBytes    = 512
	MaxIdentityBytes = 8192
	MaxPR            = 1 << 30
)

var (
	unitIDRegex      = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,100}(#[0-9]{1,9}|:[a-z0-9-]{1,40})$`)
	senderRegex      = regexp.MustCompile(`^[A-Za-z0-9._@/:-]{1,64}$`)
	identityKeyRegex = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	validStages      = map[string]bool{
		string(StagePlanned):      true,
		string(StageImplementing): true,
		string(StageVerifying):    true,
		string(StageReview):       true,
		string(StageLanding):      true,
		string(StageLanded):       true,
		string(StageAbandoned):    true,
		string(StageBlocked):      true,
	}
)

// RecordPayload is the payload of unit.recorded. A nil field keeps the unit's previous
// value: worktree, branch, pull request, lane and the identity fields are sticky.
type RecordPayload struct {
	Stage         string          `json:"stage"`
	Worktree      *string         `json:"worktree,omitempty"`
	Branch        *string         `json:"branch,omitempty"`
	PR            *int            `json:"pr,omitempty"`
	Lane          *string         `json:"lane,omitempty"`
	By            *string         `json:"by,omitempty"`
	Target        *string         `json:"target,omitempty"`
	ResolvedModel *string         `json:"resolved_model,omitempty"`
	Identity      json.RawMessage `json:"identity,omitempty"`
	IdentityKey   *string         `json:"identity_key,omitempty"`
	Reopen        bool            `json:"reopen,omitempty"`
}

type NotePayload struct {
	From string `json:"from"`
	Text string `json:"text"`
}

type NoteReadPayload struct {
	UptoSeq uint64 `json:"upto_seq"`
}

type ResumeRow struct {
	ID        string
	Stage     string
	UpdatedAt string
	Worktree  string
	Branch    string
	PR        int
	Lane      string
	Relaunch  string
}

func IsValidStage(s string) bool {
	return validStages[s]
}

func IsTerminalStage(s string) bool {
	return s == string(StageLanded) || s == string(StageAbandoned)
}

func IsValidID(id string) bool {
	return unitIDRegex.MatchString(id)
}

// RelaunchRule says whether a crashed unit may simply be started again. Only work that
// has not reached a pull request is safe: anything later may already be under review or
// landed, and starting it again would redo or duplicate it.
func RelaunchRule(u eventlog.Unit) (bool, string) {
	if u.Stage == string(StageBlocked) {
		return false, "blocked"
	}
	if u.PR != 0 {
		return false, fmt.Sprintf("has PR #%d; check its state before relaunching", u.PR)
	}
	if u.Stage == string(StageReview) || u.Stage == string(StageLanding) {
		return false, "in review/landing"
	}
	if u.Stage == string(StagePlanned) || u.Stage == string(StageImplementing) || u.Stage == string(StageVerifying) {
		return true, ""
	}
	return false, u.Stage
}

// validateRecordPayload checks everything about a record that does not depend on the log.
func validateRecordPayload(payload RecordPayload) error {
	if !IsValidStage(payload.Stage) {
		return fmt.Errorf("units: unknown stage %q", payload.Stage)
	}
	fields := []struct {
		name  string
		value *string
	}{
		{"worktree", payload.Worktree}, {"branch", payload.Branch}, {"lane", payload.Lane},
		{"by", payload.By}, {"target", payload.Target}, {"resolved_model", payload.ResolvedModel},
	}
	for i := 0; i < len(fields); i++ {
		if fields[i].value == nil {
			continue
		}
		if err := checkPlainField(fields[i].name, *fields[i].value); err != nil {
			return err
		}
	}
	if payload.PR != nil && (*payload.PR < 0 || *payload.PR > MaxPR) {
		return fmt.Errorf("units: pr must be 0..%d", MaxPR)
	}
	return validateIdentity(payload.Identity, payload.IdentityKey)
}

// checkPlainField keeps a value printable on one tab-separated line.
func checkPlainField(name string, value string) error {
	if len(value) > MaxFieldBytes {
		return fmt.Errorf("units: %s exceeds %d bytes", name, MaxFieldBytes)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("units: %s holds a control character", name)
		}
	}
	return nil
}

// validateIdentity accepts Praetor's run identity object as given. It checks only what
// Praetor itself refuses: an identity without the model that ran and the harness and
// version that ran it. The key belongs to the identity, so it is refused without one.
func validateIdentity(identity json.RawMessage, key *string) error {
	if len(identity) == 0 {
		if key != nil {
			return errors.New("units: identity_key needs the identity it was computed from")
		}
		return nil
	}
	if len(identity) > MaxIdentityBytes {
		return fmt.Errorf("units: identity exceeds %d bytes", MaxIdentityBytes)
	}
	var required struct {
		PhysicalModel  string `json:"physical_model"`
		Harness        string `json:"harness"`
		HarnessVersion string `json:"harness_version"`
	}
	if err := json.Unmarshal(identity, &required); err != nil {
		return fmt.Errorf("units: identity is not a JSON object: %w", err)
	}
	if required.PhysicalModel == "" || required.Harness == "" || required.HarnessVersion == "" {
		return errors.New("units: identity needs physical_model, harness and harness_version")
	}
	if key != nil && !identityKeyRegex.MatchString(*key) {
		return errors.New("units: identity_key must be sha256:<64 hex>")
	}
	return nil
}

func validateNoteInput(id string, from string, text string) error {
	if !IsValidID(id) {
		return fmt.Errorf("units: invalid unit id %q", id)
	}
	if !senderRegex.MatchString(from) {
		return fmt.Errorf("units: invalid sender %q", from)
	}
	if len(text) < 1 || len(text) > MaxNoteBytes {
		return fmt.Errorf("units: note text must be 1..%d bytes", MaxNoteBytes)
	}
	return nil
}
