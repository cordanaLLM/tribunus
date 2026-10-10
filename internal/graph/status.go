package graph

import (
	"fmt"
	"strings"
)

const maxFixRoundDigits = 3

type statusRule struct {
	name           StatusName
	claimStage     ClaimStage
	releaseOutcome ClaimOutcome
	transitions    []StatusName
}

// Status vocabulary source: Praetor #883 decision as amended 2026-10-09,
// aligned to feat/issue-claims @42f2a486b internal/forge/claim_marker.go.
var statusVocabulary = []statusRule{
	{name: StatusProposed, transitions: []StatusName{
		StatusReady, StatusClaimed, StatusBlocked, StatusDropped,
	}},
	{name: StatusReady, releaseOutcome: ClaimOutcomeHandedOver, transitions: []StatusName{
		StatusClaimed, StatusBlocked, StatusDropped,
	}},
	{name: StatusClaimed, claimStage: ClaimStageClaimed, transitions: []StatusName{
		StatusImplementing, StatusQueued, StatusBlocked, StatusReady, StatusDropped,
	}},
	{name: StatusImplementing, claimStage: ClaimStageImplementing, transitions: []StatusName{
		StatusReview, StatusBlocked, StatusReady, StatusDropped,
	}},
	{name: StatusReview, claimStage: ClaimStageReview, transitions: []StatusName{
		StatusFixRound, StatusQueued, StatusLanding, StatusBlocked, StatusReady, StatusDropped,
	}},
	{name: StatusFixRound, transitions: []StatusName{
		StatusImplementing, StatusReview, StatusBlocked, StatusReady, StatusDropped,
	}},
	{name: StatusBlocked, claimStage: ClaimStageBlocked, transitions: []StatusName{
		StatusProposed, StatusReady, StatusClaimed, StatusImplementing, StatusQueued, StatusDropped,
	}},
	{name: StatusQueued, claimStage: ClaimStageQueued, transitions: []StatusName{
		StatusLanding, StatusBlocked, StatusReady, StatusDropped,
	}},
	{name: StatusLanding, claimStage: ClaimStageLanding, transitions: []StatusName{
		StatusLanded, StatusReview, StatusBlocked, StatusReady, StatusDropped,
	}},
	{name: StatusLanded, releaseOutcome: ClaimOutcomeLanded},
	{name: StatusDropped, releaseOutcome: ClaimOutcomeAbandoned},
}

func validKind(kind Kind) bool {
	switch kind {
	case KindEpic, KindUnit, KindDecision, KindResearch, KindGate:
		return true
	}
	return false
}

func validateStatus(status Status) error {
	if !knownStatus(status.Name) {
		return fmt.Errorf("unknown %q", status.Name)
	}
	return nil
}

func allowTransition(from, to Status) error {
	if err := validateStatus(to); err != nil {
		return err
	}
	fromRule, ok := findStatusRule(from.Name)
	if !ok {
		return fmt.Errorf("unknown %q", from.Name)
	}
	toName := statusClass(to.Name)
	for i := 0; i < len(fromRule.transitions); i++ {
		if fromRule.transitions[i] == toName {
			return nil
		}
	}
	return fmt.Errorf("%q to %q refused", from.Name, to.Name)
}

// StatusFromClaim maps claim-marker stages and release outcomes to graph status.
func StatusFromClaim(stage ClaimStage, outcome ClaimOutcome) (Status, error) {
	if stage == ClaimStageReleased {
		return statusFromReleaseOutcome(outcome)
	}
	if outcome != "" {
		return Status{}, fmt.Errorf("claim outcome %q is invalid for stage %q", outcome, stage)
	}
	if strings.HasPrefix(string(stage), fixRoundPrefix()) {
		return statusFromFixRoundStage(stage)
	}
	return statusFromClaimStage(stage)
}

func statusFromClaimStage(stage ClaimStage) (Status, error) {
	for i := 0; i < len(statusVocabulary); i++ {
		if statusVocabulary[i].claimStage != "" && statusVocabulary[i].claimStage == stage {
			return Status{Name: statusVocabulary[i].name}, nil
		}
	}
	return Status{}, fmt.Errorf("claim stage %q is unknown", stage)
}

func statusFromReleaseOutcome(outcome ClaimOutcome) (Status, error) {
	for i := 0; i < len(statusVocabulary); i++ {
		if statusVocabulary[i].releaseOutcome != "" && statusVocabulary[i].releaseOutcome == outcome {
			return Status{Name: statusVocabulary[i].name}, nil
		}
	}
	return Status{}, fmt.Errorf("claim release outcome %q is unknown", outcome)
}

func statusFromFixRoundStage(stage ClaimStage) (Status, error) {
	status := Status{Name: StatusName(stage)}
	if err := validateStatus(status); err != nil {
		return Status{}, err
	}
	return status, nil
}

func knownStatus(name StatusName) bool {
	if name == StatusFixRound {
		return false
	}
	_, ok := findStatusRule(name)
	return ok
}

func findStatusRule(name StatusName) (statusRule, bool) {
	class := statusClass(name)
	for i := 0; i < len(statusVocabulary); i++ {
		if statusVocabulary[i].name == class {
			return statusVocabulary[i], true
		}
	}
	return statusRule{}, false
}

func statusClass(name StatusName) StatusName {
	if _, ok := fixRoundNumber(name); ok {
		return StatusFixRound
	}
	return name
}

func fixRoundNumber(name StatusName) (int, bool) {
	raw := string(name)
	if !strings.HasPrefix(raw, fixRoundPrefix()) {
		return 0, false
	}
	suffix := strings.TrimPrefix(raw, fixRoundPrefix())
	if len(suffix) > maxFixRoundDigits {
		return 0, false
	}
	round := 0
	for i := 0; i < len(suffix); i++ {
		if suffix[i] < '0' || suffix[i] > '9' {
			return 0, false
		}
		round = round*10 + int(suffix[i]-'0')
	}
	return round, round >= 1
}

func fixRoundPrefix() string {
	return "fix-round-"
}

// KnownStatus reports whether name is in the status vocabulary. A fix round must carry its
// number ("fix-round-2"); the class name "fix-round-N" itself is not a status.
func KnownStatus(name StatusName) bool {
	return knownStatus(name)
}

// TerminalStatus reports whether name is a known status that nothing follows.
func TerminalStatus(name StatusName) bool {
	rule, ok := findStatusRule(name)
	return ok && len(rule.transitions) == 0
}

// AllowTransition is the one transition table of the vocabulary, for every package that
// records a status: nil when a move from one status to another is allowed.
func AllowTransition(from, to StatusName) error {
	return allowTransition(Status{Name: from}, Status{Name: to})
}
