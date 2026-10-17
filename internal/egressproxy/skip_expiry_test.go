package egressproxy

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// windowsRelaySkipExpires ends the Windows exception of TestRelayOneSetsTheTunnelDeadline:
// from this day on (UTC) the test fails on Windows instead of skipping, so the exception is
// renewed with a new reason or the skip goes.
var windowsRelaySkipExpires = time.Date(2027, time.February, 13, 0, 0, 0, 0, time.UTC)

// skipOnWindowsUntil skips t with reason while now is before expires, and fails it once the
// exception has expired.
func skipOnWindowsUntil(t testing.TB, expires, now time.Time, reason string) {
	t.Helper()
	if msg, live := skipVerdict(expires, now, reason); live {
		t.Skip(msg)
	} else {
		t.Fatal(msg)
	}
}

// skipVerdict reports the message for a skip exception and whether it still holds at now.
func skipVerdict(expires, now time.Time, reason string) (string, bool) {
	day := expires.UTC().Format(time.DateOnly)
	if now.Before(expires) {
		return fmt.Sprintf("%s (expires %s)", reason, day), true
	}
	return fmt.Sprintf("skip exception expired on %s: %s; renew it with a new reason or remove the skip", day, reason), false
}

func TestSkipVerdict(t *testing.T) {
	expires := time.Date(2027, time.February, 13, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		now      time.Time
		wantLive bool
		wantText string
	}{
		{"a day before", expires.Add(-24 * time.Hour), true, "expires 2027-02-13"},
		{"last instant", expires.Add(-time.Nanosecond), true, "expires 2027-02-13"},
		{"expiry instant", expires, false, "expired on 2027-02-13"},
		{"a day after", expires.Add(24 * time.Hour), false, "expired on 2027-02-13"},
	}
	for i := 0; i < len(tests); i++ {
		tc := tests[i]
		t.Run(tc.name, func(t *testing.T) {
			msg, live := skipVerdict(expires, tc.now, "reason")
			if live != tc.wantLive || !strings.Contains(msg, tc.wantText) || !strings.Contains(msg, "reason") {
				t.Fatalf("skipVerdict(%s) = %q, %t; want %t and %q", tc.now, msg, live, tc.wantLive, tc.wantText)
			}
		})
	}
}

func TestWindowsRelaySkipExpiresWithinCategoryD(t *testing.T) {
	entered := time.Date(2026, time.October, 16, 0, 0, 0, 0, time.UTC)
	if got := windowsRelaySkipExpires.Sub(entered); got != 120*24*time.Hour {
		t.Fatalf("windows relay skip runs %s from its entry, want 120 days (category D)", got)
	}
}
