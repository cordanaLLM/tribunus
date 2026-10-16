package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemUnits_Positive(t *testing.T) {
	bin := buildCLI(t)
	cfgPath, stateDir := writeUnitsConfig(t)
	unitID := "epitrop:unit-sys-1"

	systemUnitsSet(t, bin, cfgPath, unitID)
	checkEventLogRecords(t, stateDir)
	systemUnitsShow(t, bin, cfgPath, unitID)
	systemUnitsResume(t, bin, cfgPath, unitID)
}

func systemUnitsSet(t *testing.T, bin string, cfgPath string, unitID string) {
	t.Helper()
	stdout, stderr, code, err := runSystemCLI(t, bin, "units", "set", "--config="+cfgPath, "--status=proposed", unitID)
	if err != nil || code != 0 {
		t.Fatalf("units set code = %d, err = %v: %s\n%s", code, err, stdout, stderr)
	}
}

func systemUnitsShow(t *testing.T, bin string, cfgPath string, unitID string) {
	t.Helper()
	stdout, stderr, code, err := runSystemCLI(t, bin, "units", "show", "--config="+cfgPath, unitID)
	if err != nil || code != 0 {
		t.Fatalf("units show code = %d, err = %v: %s\n%s", code, err, stdout, stderr)
	}
	for _, want := range []string{`"id": "epitrop:unit-sys-1"`, `"status": "proposed"`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("units show output missing %q: %s", want, stdout)
		}
	}
}

func systemUnitsResume(t *testing.T, bin string, cfgPath string, unitID string) {
	t.Helper()
	stdout, stderr, code, err := runSystemCLI(t, bin, "units", "resume", "--config="+cfgPath)
	if err != nil || code != 0 {
		t.Fatalf("units resume code = %d, err = %v: %s\n%s", code, err, stdout, stderr)
	}
	wantHeader := "id\tstatus\tupdated_at\tworktree\tbranch\tpr\tlane\trelaunch\n"
	if !strings.HasPrefix(stdout, wantHeader) {
		t.Fatalf("units resume header missing: %s", stdout)
	}
	if !strings.Contains(stdout, unitID+"\tproposed\t") {
		t.Fatalf("units resume missing unit row: %s", stdout)
	}
}

func TestSystemUnits_Negative(t *testing.T) {
	bin := buildCLI(t)
	cfgPath, _ := writeUnitsConfig(t)

	t.Run("set missing status flag", func(t *testing.T) {
		_, stderr, code, err := runSystemCLI(t, bin, "units", "set", "--config="+cfgPath, "epitrop:unit-1")
		if err == nil || code != 1 {
			t.Fatalf("units set missing --status code = %d, err = %v, want 1", code, err)
		}
		if !strings.Contains(stderr, "units set: --status is required") {
			t.Fatalf("stderr = %q, want --status is required error", stderr)
		}
	})

	t.Run("show missing unit", func(t *testing.T) {
		_, stderr, code, err := runSystemCLI(t, bin, "units", "show", "--config="+cfgPath, "epitrop:no-such-unit")
		if err == nil || code != 1 {
			t.Fatalf("units show missing unit code = %d, err = %v, want 1", code, err)
		}
		if !strings.Contains(stderr, "does not exist") {
			t.Fatalf("stderr = %q, want 'does not exist' error", stderr)
		}
	})

	t.Run("missing config flag", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing.json")
		_, stderr, code, err := runSystemCLI(t, bin, "units", "show", "--config="+missing, "unit-1")
		if err == nil || code != 1 {
			t.Fatalf("units show missing config code = %d, err = %v, want 1", code, err)
		}
		if !strings.Contains(stderr, "missing.json") {
			t.Fatalf("stderr = %q, want missing config error", stderr)
		}
	})
}
