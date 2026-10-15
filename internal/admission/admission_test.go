// SPDX-FileCopyrightText: 2026 lusoris <lusoris@pm.me>
//
// SPDX-License-Identifier: EUPL-1.2

package admission

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/golusoris/golusoris/core/clock"
	"github.com/tekmora/epitrop/internal/config"
	"github.com/tekmora/epitrop/internal/telemetry"
	"github.com/tekmora/epitrop/internal/telemetry/telemetrytest"
)

type fakeProber struct {
	mu    sync.Mutex
	fn    func(ctx context.Context, alias string) (ProbeResult, error)
	calls int
}

func (f *fakeProber) Probe(ctx context.Context, alias string) (ProbeResult, error) {
	f.mu.Lock()
	f.calls++
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, alias)
	}
	return ProbeResult{Model: "test-model", DeploymentID: "test-dep"}, nil
}

func validConfig() Config {
	return Config{
		Timeout:        2 * time.Second,
		Interval:       5 * time.Second,
		ReadyThreshold: 2,
	}
}

func refreshGate(t *testing.T, g *Gate, ctx context.Context, alias string) {
	t.Helper()
	if err := g.Refresh(ctx, alias); err != nil {
		t.Fatalf("Refresh(%q) = %v, want nil", alias, err)
	}
}

func assertAdmitOutcome(t *testing.T, g *Gate, ctx context.Context, alias string, wantOutcome Outcome) {
	t.Helper()
	res, err := g.Admit(ctx, alias)
	switch wantOutcome {
	case OutcomeAdmitted:
		if err != nil {
			t.Fatalf("Admit(%q) = %v, want admitted", alias, err)
		}
		if res.Model == "" {
			t.Fatalf("Admit(%q) model is empty", alias)
		}
	case OutcomeRefused:
		if err == nil || !errors.Is(err, ErrRefused) {
			t.Fatalf("Admit(%q) err = %v, want ErrRefused", alias, err)
		}
	case OutcomeUnknown:
		if err == nil || !errors.Is(err, ErrUnknown) {
			t.Fatalf("Admit(%q) err = %v, want ErrUnknown", alias, err)
		}
	}
}

func TestGate_ThresholdBoundary(t *testing.T) {
	clk := clock.NewFake()
	prober := &fakeProber{
		fn: func(ctx context.Context, alias string) (ProbeResult, error) {
			return ProbeResult{Model: "gpt-4o", DeploymentID: "dep-1"}, nil
		},
	}
	cfg := validConfig()
	gate, err := NewGate(prober, clk, cfg, []string{"cordana/auto"})
	if err != nil {
		t.Fatalf("NewGate() = %v", err)
	}

	ctx := context.Background()

	// 1 success < threshold (2) -> refused
	refreshGate(t, gate, ctx, "cordana/auto")
	assertAdmitOutcome(t, gate, ctx, "cordana/auto", OutcomeRefused)

	// 2nd success -> threshold met -> admitted
	refreshGate(t, gate, ctx, "cordana/auto")
	assertAdmitOutcome(t, gate, ctx, "cordana/auto", OutcomeAdmitted)
}

func TestGate_StaleBoundary(t *testing.T) {
	clk := clock.NewFake()
	prober := &fakeProber{
		fn: func(ctx context.Context, alias string) (ProbeResult, error) {
			return ProbeResult{Model: "gpt-4o", DeploymentID: "dep-1"}, nil
		},
	}
	cfg := validConfig()
	gate, err := NewGate(prober, clk, cfg, []string{"cordana/auto"})
	if err != nil {
		t.Fatalf("NewGate() = %v", err)
	}

	ctx := context.Background()
	refreshGate(t, gate, ctx, "cordana/auto")
	refreshGate(t, gate, ctx, "cordana/auto")
	assertAdmitOutcome(t, gate, ctx, "cordana/auto", OutcomeAdmitted)

	// Exactly Interval+Timeout old -> still admitted
	clk.Advance(cfg.Interval + cfg.Timeout)
	assertAdmitOutcome(t, gate, ctx, "cordana/auto", OutcomeAdmitted)

	// 1 ns more -> stale -> unknown
	clk.Advance(1 * time.Nanosecond)
	assertAdmitOutcome(t, gate, ctx, "cordana/auto", OutcomeUnknown)
}

func TestGate_Negative_ProbeTimeout(t *testing.T) {
	clk := clock.NewFake()
	prober := &fakeProber{
		fn: func(ctx context.Context, alias string) (ProbeResult, error) {
			<-ctx.Done()
			return ProbeResult{}, ctx.Err()
		},
	}
	cfg := validConfig()
	cfg.Timeout = 10 * time.Millisecond
	gate, err := NewGate(prober, clk, cfg, []string{"cordana/auto"})
	if err != nil {
		t.Fatalf("NewGate() = %v, want nil", err)
	}

	ctx := context.Background()
	refreshErr := gate.Refresh(ctx, "cordana/auto")
	if refreshErr == nil || !errors.Is(refreshErr, context.DeadlineExceeded) {
		t.Fatalf("Refresh() error = %v, want context.DeadlineExceeded", refreshErr)
	}

	_, admitErr := gate.Admit(ctx, "cordana/auto")
	if admitErr == nil || !errors.Is(admitErr, ErrRefused) {
		t.Fatalf("Admit() error = %v, want ErrRefused", admitErr)
	}
	var refErr *RefusedError
	if !errors.As(admitErr, &refErr) || !refErr.Retryable() {
		t.Fatalf("Admit() error = %v, want Retryable RefusedError", admitErr)
	}
	if !errors.Is(admitErr, context.DeadlineExceeded) {
		t.Fatalf("Admit() cause = %v, want context.DeadlineExceeded unwrapped", admitErr)
	}
}

func TestGate_Negative_NoProbe(t *testing.T) {
	clk := clock.NewFake()
	prober := &fakeProber{}
	cfg := validConfig()
	gate, err := NewGate(prober, clk, cfg, []string{"cordana/auto"})
	if err != nil {
		t.Fatalf("NewGate() = %v, want nil", err)
	}

	ctx := context.Background()
	_, admitErr := gate.Admit(ctx, "cordana/auto")
	if admitErr == nil || !errors.Is(admitErr, ErrUnknown) {
		t.Fatalf("Admit() error = %v, want ErrUnknown", admitErr)
	}
	var refErr *RefusedError
	if !errors.As(admitErr, &refErr) || !refErr.Retryable() {
		t.Fatalf("Admit() error = %v, want Retryable RefusedError", admitErr)
	}
}

func TestGate_FailureResetsStreak(t *testing.T) {
	clk := clock.NewFake()
	failProbe := false
	prober := &fakeProber{
		fn: func(ctx context.Context, alias string) (ProbeResult, error) {
			if failProbe {
				return ProbeResult{}, errors.New("upstream gateway error")
			}
			return ProbeResult{Model: "qwen-27b", DeploymentID: "dep-27"}, nil
		},
	}
	cfg := validConfig()
	gate, err := NewGate(prober, clk, cfg, []string{"cordana/auto"})
	if err != nil {
		t.Fatalf("NewGate() = %v, want nil", err)
	}

	ctx := context.Background()
	// Two successes -> admitted
	refreshGate(t, gate, ctx, "cordana/auto")
	refreshGate(t, gate, ctx, "cordana/auto")
	assertAdmitOutcome(t, gate, ctx, "cordana/auto", OutcomeAdmitted)

	// Probe fails -> streak reset
	failProbe = true
	if err := gate.Refresh(ctx, "cordana/auto"); err == nil {
		t.Fatal("Refresh() succeeded, want failure")
	}
	assertAdmitOutcome(t, gate, ctx, "cordana/auto", OutcomeRefused)

	// 1 success is not enough (ReadyThreshold=2)
	failProbe = false
	refreshGate(t, gate, ctx, "cordana/auto")
	assertAdmitOutcome(t, gate, ctx, "cordana/auto", OutcomeRefused)

	// 2nd success -> admitted again
	refreshGate(t, gate, ctx, "cordana/auto")
	assertAdmitOutcome(t, gate, ctx, "cordana/auto", OutcomeAdmitted)
}

func TestNewGate_RefusalsTable(t *testing.T) {
	clk := clock.NewFake()
	prober := &fakeProber{}
	cfg := validConfig()

	tooManyAliases := make([]string, config.MaxRouterAliases+1)
	for i := 0; i < len(tooManyAliases); i++ {
		tooManyAliases[i] = fmt.Sprintf("cordana/alias-%d", i)
	}

	tests := []struct {
		name    string
		p       Prober
		c       clock.Clock
		cfg     Config
		aliases []string
	}{
		{"nil prober", nil, clk, cfg, []string{"cordana/auto"}},
		{"nil clock", prober, nil, cfg, []string{"cordana/auto"}},
		{"zero timeout", prober, clk, Config{Timeout: 0, Interval: time.Second, ReadyThreshold: 1}, []string{"cordana/auto"}},
		{"negative timeout", prober, clk, Config{Timeout: -time.Second, Interval: time.Second, ReadyThreshold: 1}, []string{"cordana/auto"}},
		{"zero interval", prober, clk, Config{Timeout: time.Second, Interval: 0, ReadyThreshold: 1}, []string{"cordana/auto"}},
		{"negative interval", prober, clk, Config{Timeout: time.Second, Interval: -time.Second, ReadyThreshold: 1}, []string{"cordana/auto"}},
		{"zero threshold", prober, clk, Config{Timeout: time.Second, Interval: time.Second, ReadyThreshold: 0}, []string{"cordana/auto"}},
		{"negative threshold", prober, clk, Config{Timeout: time.Second, Interval: time.Second, ReadyThreshold: -1}, []string{"cordana/auto"}},
		{"nil aliases", prober, clk, cfg, nil},
		{"empty aliases slice", prober, clk, cfg, []string{}},
		{"empty alias string", prober, clk, cfg, []string{""}},
		{"empty alias in list", prober, clk, cfg, []string{"cordana/auto", ""}},
		{"duplicate alias", prober, clk, cfg, []string{"cordana/auto", "cordana/auto"}},
		{"too many aliases", prober, clk, cfg, tooManyAliases},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate, err := NewGate(tt.p, tt.c, tt.cfg, tt.aliases)
			if err == nil {
				t.Fatalf("NewGate(%s) = %+v, want error", tt.name, gate)
			}
		})
	}
}

func TestGate_UnknownAlias(t *testing.T) {
	clk := clock.NewFake()
	prober := &fakeProber{}
	cfg := validConfig()
	gate, err := NewGate(prober, clk, cfg, []string{"cordana/auto"})
	if err != nil {
		t.Fatalf("NewGate() = %v", err)
	}

	ctx := context.Background()
	if err := gate.Refresh(ctx, "cordana/other"); err == nil {
		t.Fatal("Refresh(unknown) succeeded, want error")
	}

	_, admitErr := gate.Admit(ctx, "cordana/other")
	if admitErr == nil || !errors.Is(admitErr, ErrUnknown) {
		t.Fatalf("Admit(unknown) = %v, want ErrUnknown", admitErr)
	}
	var refErr *RefusedError
	if !errors.As(admitErr, &refErr) || !refErr.Retryable() {
		t.Fatalf("Admit(unknown) = %v, want Retryable RefusedError", admitErr)
	}
}

func TestGate_TelemetryMetrics(t *testing.T) {
	rec := telemetrytest.Install(t)
	clk := clock.NewFake()

	fail := false
	prober := &fakeProber{
		fn: func(ctx context.Context, alias string) (ProbeResult, error) {
			if fail {
				return ProbeResult{}, errors.New("upstream down")
			}
			return ProbeResult{Model: "m1", DeploymentID: "d1"}, nil
		},
	}
	cfg := validConfig()
	gate, err := NewGate(prober, clk, cfg, []string{"cordana/auto"})
	if err != nil {
		t.Fatalf("NewGate() = %v", err)
	}

	ctx := context.Background()

	// No probe -> unknown (1)
	assertAdmitOutcome(t, gate, ctx, "cordana/auto", OutcomeUnknown)
	if v := rec.Value(t, telemetry.MetricAdmissionDecisionsTotal, "outcome", "unknown"); v != 1 {
		t.Fatalf("admission decisions unknown = %d, want 1", v)
	}

	// Failed probe -> refused (1)
	fail = true
	if err := gate.Refresh(ctx, "cordana/auto"); err == nil {
		t.Fatal("Refresh() succeeded, want error")
	}
	assertAdmitOutcome(t, gate, ctx, "cordana/auto", OutcomeRefused)
	if v := rec.Value(t, telemetry.MetricAdmissionDecisionsTotal, "outcome", "refused"); v != 1 {
		t.Fatalf("admission decisions refused = %d, want 1", v)
	}

	// 2 successes -> admitted (1)
	fail = false
	refreshGate(t, gate, ctx, "cordana/auto")
	refreshGate(t, gate, ctx, "cordana/auto")
	assertAdmitOutcome(t, gate, ctx, "cordana/auto", OutcomeAdmitted)
	if v := rec.Value(t, telemetry.MetricAdmissionDecisionsTotal, "outcome", "admitted"); v != 1 {
		t.Fatalf("admission decisions admitted = %d, want 1", v)
	}
}

func TestRefusedError_FormatAndUnwrap(t *testing.T) {
	cause := errors.New("root failure")
	errWithCause := &RefusedError{
		Alias:      "cordana/auto",
		Outcome:    OutcomeRefused,
		RetryAfter: 5 * time.Second,
		Cause:      cause,
	}
	if !errors.Is(errWithCause, ErrRefused) || !errors.Is(errWithCause, cause) {
		t.Fatalf("Unwrap() matching failed for errWithCause")
	}
	if !errWithCause.Retryable() {
		t.Fatal("Retryable() = false, want true")
	}
	if errWithCause.Error() == "" {
		t.Fatal("Error() is empty")
	}

	errWithoutCause := &RefusedError{
		Alias:      "cordana/auto",
		Outcome:    OutcomeUnknown,
		RetryAfter: 5 * time.Second,
	}
	if !errors.Is(errWithoutCause, ErrUnknown) {
		t.Fatalf("errors.Is(errWithoutCause, ErrUnknown) = false, want true")
	}
	if errWithoutCause.Error() == "" {
		t.Fatal("Error() is empty")
	}
}

func TestConfigFrom(t *testing.T) {
	p := config.ProbeConfig{
		TimeoutSeconds:  12,
		IntervalSeconds: 34,
		ReadyThreshold:  5,
	}
	cfg := ConfigFrom(p)
	if cfg.Timeout != 12*time.Second || cfg.Interval != 34*time.Second || cfg.ReadyThreshold != 5 {
		t.Errorf("ConfigFrom mismatch: %+v", cfg)
	}
}
