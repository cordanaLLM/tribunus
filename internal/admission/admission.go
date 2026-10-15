// SPDX-FileCopyrightText: 2026 lusoris <lusoris@pm.me>
//
// SPDX-License-Identifier: EUPL-1.2

// Package admission admits work for a router alias only after a bounded probe
// through that alias succeeds within a deadline, refusing fast otherwise.
package admission

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/golusoris/golusoris/core/clock"
	"github.com/golusoris/golusoris/core/validate"
	"github.com/tekmora/epitrop/internal/config"
	"github.com/tekmora/epitrop/internal/telemetry"
)

// Outcome represents the result of an admission check.
type Outcome string

const (
	// OutcomeAdmitted indicates that the alias has passed required probes and is ready.
	OutcomeAdmitted Outcome = "admitted"
	// OutcomeRefused indicates that the alias probe failed or has not met the threshold.
	OutcomeRefused Outcome = "refused"
	// OutcomeUnknown indicates that the alias has no probe recorded, is stale, or is not configured.
	OutcomeUnknown Outcome = "unknown"
)

var (
	// ErrRefused indicates a router alias probe was refused because the alias is not ready or failed.
	ErrRefused = errors.New("admission: refused")
	// ErrUnknown indicates a router alias status is unknown (missing, stale, or unrecognized).
	ErrUnknown = errors.New("admission: unknown")
)

// RefusedError describes why work for a router alias was refused.
type RefusedError struct {
	Alias      string
	Outcome    Outcome
	RetryAfter time.Duration
	Cause      error
}

// Error formats the refusal with alias, outcome, and underlying cause.
func (e *RefusedError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("admission: alias %q %s: %v", e.Alias, e.Outcome, e.Cause)
	}
	return fmt.Sprintf("admission: alias %q %s", e.Alias, e.Outcome)
}

// Unwrap returns ErrRefused or ErrUnknown depending on Outcome, plus Cause if present.
func (e *RefusedError) Unwrap() []error {
	var base error
	switch e.Outcome {
	case OutcomeRefused:
		base = ErrRefused
	case OutcomeUnknown:
		base = ErrUnknown
	default:
		base = ErrUnknown
	}
	if e.Cause != nil {
		return []error{base, e.Cause}
	}
	return []error{base}
}

// Retryable returns true because admission refusals are temporary and may succeed on retry.
func (e *RefusedError) Retryable() bool {
	return true
}

// ProbeResult holds the resolved model name and deployment identifier from a probe.
type ProbeResult struct {
	Model        string
	DeploymentID string
}

// Prober executes a health probe against a router alias.
type Prober interface {
	Probe(ctx context.Context, alias string) (ProbeResult, error)
}

// Config specifies timeout, interval, and consecutive success threshold for probes.
type Config struct {
	Timeout        time.Duration
	Interval       time.Duration
	ReadyThreshold int
}

// ConfigFrom converts a configuration ProbeConfig into an admission Config.
func ConfigFrom(p config.ProbeConfig) Config {
	return Config{
		Timeout:        time.Duration(p.TimeoutSeconds) * time.Second,
		Interval:       time.Duration(p.IntervalSeconds) * time.Second,
		ReadyThreshold: p.ReadyThreshold,
	}
}

// Gate manages admission state for router aliases and enforces probe freshness.
type Gate struct {
	mu     sync.Mutex
	prober Prober
	clock  clock.Clock
	cfg    Config
	states map[string]*aliasState
}

type aliasState struct {
	consecutiveSuccesses int
	lastProbeTime        time.Time
	lastResult           ProbeResult
	lastError            error
}

// NewGate constructs an admission Gate for the given aliases.
func NewGate(p Prober, c clock.Clock, cfg Config, aliases []string) (*Gate, error) {
	if err := validateGateConfig(p, c, cfg); err != nil {
		return nil, err
	}
	states, err := initAliasStates(aliases)
	if err != nil {
		return nil, err
	}
	return &Gate{
		prober: p,
		clock:  c,
		cfg:    cfg,
		states: states,
	}, nil
}

func validateGateConfig(p Prober, c clock.Clock, cfg Config) error {
	if p == nil || validate.IsNil(p) {
		return errors.New("admission: prober is required")
	}
	if c == nil || validate.IsNil(c) {
		return errors.New("admission: clock is required")
	}
	if cfg.Timeout <= 0 {
		return errors.New("admission: timeout must be positive")
	}
	if cfg.Interval <= 0 {
		return errors.New("admission: interval must be positive")
	}
	if cfg.ReadyThreshold <= 0 {
		return errors.New("admission: ready threshold must be positive")
	}
	return nil
}

func initAliasStates(aliases []string) (map[string]*aliasState, error) {
	if len(aliases) == 0 {
		return nil, errors.New("admission: aliases list is empty")
	}
	if len(aliases) > config.MaxRouterAliases {
		return nil, fmt.Errorf("admission: alias count %d exceeds maximum %d", len(aliases), config.MaxRouterAliases)
	}

	states := make(map[string]*aliasState, len(aliases))
	for _, alias := range aliases {
		if alias == "" {
			return nil, errors.New("admission: empty alias")
		}
		if _, exists := states[alias]; exists {
			return nil, fmt.Errorf("admission: duplicate alias %q", alias)
		}
		states[alias] = &aliasState{}
	}
	return states, nil
}

// Refresh runs a bounded probe against alias and updates its health state.
func (g *Gate) Refresh(ctx context.Context, alias string) error {
	if ctx == nil {
		return errors.New("admission: nil context")
	}

	g.mu.Lock()
	st, ok := g.states[alias]
	g.mu.Unlock()
	if !ok {
		return fmt.Errorf("admission: unknown alias %q", alias)
	}

	probeCtx, cancel := context.WithTimeout(ctx, g.cfg.Timeout)
	defer cancel()

	res, err := g.prober.Probe(probeCtx, alias)
	now := g.clock.Now()

	g.mu.Lock()
	if err == nil {
		st.consecutiveSuccesses++
		st.lastProbeTime = now
		st.lastResult = res
		st.lastError = nil
	} else {
		st.consecutiveSuccesses = 0
		st.lastProbeTime = now
		st.lastError = err
	}
	g.mu.Unlock()

	if err != nil {
		return fmt.Errorf("admission: probe alias %q: %w", alias, err)
	}
	return nil
}

// Admit checks whether alias is ready to accept work without calling the prober.
func (g *Gate) Admit(ctx context.Context, alias string) (ProbeResult, error) {
	outcome := OutcomeUnknown
	defer func() {
		g.recordDecision(ctx, outcome)
	}()

	g.mu.Lock()
	st, ok := g.states[alias]
	if !ok {
		g.mu.Unlock()
		return ProbeResult{}, &RefusedError{
			Alias:      alias,
			Outcome:    OutcomeUnknown,
			RetryAfter: g.cfg.Interval,
			Cause:      fmt.Errorf("unknown alias %q", alias),
		}
	}
	snapshot := *st
	g.mu.Unlock()

	res, resOutcome, err := evaluateState(&snapshot, g.clock.Now(), g.cfg, alias)
	outcome = resOutcome
	return res, err
}

func evaluateState(st *aliasState, now time.Time, cfg Config, alias string) (ProbeResult, Outcome, error) {
	staleThreshold := cfg.Interval + cfg.Timeout
	if st.lastProbeTime.IsZero() {
		return ProbeResult{}, OutcomeUnknown, &RefusedError{
			Alias:      alias,
			Outcome:    OutcomeUnknown,
			RetryAfter: cfg.Interval,
			Cause:      errors.New("no probe recorded yet"),
		}
	}

	if now.Sub(st.lastProbeTime) > staleThreshold {
		return ProbeResult{}, OutcomeUnknown, &RefusedError{
			Alias:      alias,
			Outcome:    OutcomeUnknown,
			RetryAfter: cfg.Interval,
			Cause:      fmt.Errorf("probe is stale (%v old, max %v)", now.Sub(st.lastProbeTime), staleThreshold),
		}
	}

	if st.lastError != nil {
		return ProbeResult{}, OutcomeRefused, &RefusedError{
			Alias:      alias,
			Outcome:    OutcomeRefused,
			RetryAfter: cfg.Interval,
			Cause:      st.lastError,
		}
	}

	if st.consecutiveSuccesses < cfg.ReadyThreshold {
		return ProbeResult{}, OutcomeRefused, &RefusedError{
			Alias:      alias,
			Outcome:    OutcomeRefused,
			RetryAfter: cfg.Interval,
			Cause:      fmt.Errorf("not ready: %d of %d successes", st.consecutiveSuccesses, cfg.ReadyThreshold),
		}
	}

	return st.lastResult, OutcomeAdmitted, nil
}

func (g *Gate) recordDecision(ctx context.Context, outcome Outcome) {
	if tel := telemetry.Default(); tel != nil {
		tel.AdmissionDecision(ctx, string(outcome))
	}
}
