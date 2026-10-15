// SPDX-FileCopyrightText: 2026 lusoris <lusoris@pm.me>
//
// SPDX-License-Identifier: EUPL-1.2

package lintexceptions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const header = "linters:\n  exclusions:\n    rules:\n"

func day(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil {
		t.Fatalf("parse %s: %v", value, err)
	}
	return parsed
}

func TestRepositoryExceptionsAreDatedAndCurrent(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", ".golangci.yml"))
	if err != nil {
		t.Fatalf("read .golangci.yml: %v", err)
	}
	rules, err := Check(string(body), time.Now().UTC())
	if err != nil {
		t.Fatalf(".golangci.yml: %v", err)
	}
	t.Logf("%d dated exception rules", rules)
}

func TestThisCheckIsItselfCurrent(t *testing.T) {
	expired, err := Expired(SelfExpiry, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if expired {
		t.Fatalf("tools/lintexceptions expired on %s: the engine should read these exceptions by now; retire or renew it", SelfExpiry)
	}
}

func TestCheckAcceptsAnEmptyRulesList(t *testing.T) {
	config := "linters:\n  exclusions:\n    rules: []\nissues:\n  uniq-by-line: false\n"
	rules, err := Check(config, day(t, "2026-10-15"))
	if err != nil || rules != 0 {
		t.Fatalf("Check = %d, %v; want 0, nil", rules, err)
	}
}

func TestCheckRefusesAnIndentedRuleAfterAnEmptyList(t *testing.T) {
	config := "linters:\n  exclusions:\n    rules: []\n" +
		"      - path: ^a\\.go$\n" +
		"        linters: [gocyclo]\n"
	if _, err := Check(config, day(t, "2026-10-15")); err == nil {
		t.Fatal("Check accepted a rule without its comment after an empty list")
	}
}

func TestCheckAcceptsDatedRules(t *testing.T) {
	config := header +
		"      # category C, expires 2026-10-25, #133\n" +
		"      - path: ^a\\.go$\n" +
		"        linters: [gocyclo]\n" +
		"      # category D, expires 2027-01-01, #7\n" +
		"      - path: ^b\\.go$\n" +
		"        linters: [gocognit]\n" +
		"issues:\n  uniq-by-line: false\n"
	rules, err := Check(config, day(t, "2026-10-15"))
	if err != nil || rules != 2 {
		t.Fatalf("Check = %d, %v; want 2, nil", rules, err)
	}
}

func TestCheckReadsCRLFLines(t *testing.T) {
	config := strings.ReplaceAll(header+
		"      # category C, expires 2026-10-25, #133\n"+
		"      - path: ^a\\.go$\n"+
		"        linters: [gocyclo]\n", "\n", "\r\n")
	rules, err := Check(config, day(t, "2026-10-15"))
	if err != nil || rules != 1 {
		t.Fatalf("Check = %d, %v; want 1, nil", rules, err)
	}
}

func TestCheckRefusesPlantedDefects(t *testing.T) {
	cases := map[string]string{
		"expired":         "      # category C, expires 2026-10-14, #133\n      - path: ^a\\.go$\n        linters: [gocyclo]\n",
		"missing comment": "      - path: ^a\\.go$\n        linters: [gocyclo]\n",
		"no category":     "      # expires 2026-10-25, #133\n      - path: ^a\\.go$\n        linters: [gocyclo]\n",
		"bad date":        "      # category C, expires 2026-13-40, #133\n      - path: ^a\\.go$\n        linters: [gocyclo]\n",
		"other linter":    "      # category C, expires 2026-10-25, #133\n      - path: ^a\\.go$\n        linters: [errcheck]\n",
		"text rule":       "      # category C, expires 2026-10-25, #133\n      - path: ^a\\.go$\n        text: anything\n",
	}
	for name, rules := range cases {
		if _, err := Check(header+rules, day(t, "2026-10-15")); err == nil {
			t.Errorf("%s: Check accepted %q", name, rules)
		}
	}
	if _, err := Check("linters:\n  enable: [gocyclo]\n", day(t, "2026-10-15")); err == nil {
		t.Error("a configuration without a rules block was accepted")
	}
}

func TestExpiredHoldsThroughTheExpiryDay(t *testing.T) {
	cases := []struct {
		today string
		want  bool
	}{
		{"2026-10-24", false},
		{"2026-10-25", false},
		{"2026-10-26", true},
	}
	for _, c := range cases {
		got, err := Expired("2026-10-25", day(t, c.today))
		if err != nil || got != c.want {
			t.Errorf("Expired(2026-10-25, %s) = %v, %v; want %v", c.today, got, err, c.want)
		}
	}
}
