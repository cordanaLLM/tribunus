package codexlocal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cordanaLLM/praetor/tribunus/catalog"
)

func writeSession(t *testing.T, dir, name, body string, modTime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
	return path
}

const validLine = `{"timestamp":"2026-09-13T12:56:47.753Z","ordinal":1,"type":"event_msg","payload":{"type":"token_count","info":{},"rate_limits":{"limit_id":"codex","limit_name":null,"primary":{"used_percent":92.0,"window_minutes":10080,"resets_at":1789858494},"secondary":null,"credits":{"has_credits":false,"unlimited":false,"balance":"0"},"plan_type":"pro"}}}`

func TestFetch_Positive(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "old.jsonl", validLine, time.Now().Add(-time.Hour))
	// Newest file carries a later rate_limits reading than the older one.
	newer := strings.Replace(validLine, `"used_percent":92.0`, `"used_percent":45.5`, 1)
	writeSession(t, dir, "new.jsonl", validLine+"\n"+newer, time.Now())

	res := Fetch(context.Background(), dir)
	if res.Status != catalog.StatusOK {
		t.Fatalf("Status = %v, detail = %q, want ok", res.Status, res.Detail)
	}
	if res.Count != 1 || len(res.Records) != 1 {
		t.Fatalf("got %d records, want 1", res.Count)
	}
	rec := res.Records[0]
	if rec.ModelID != "codex-cli/codex" {
		t.Fatalf("ModelID = %q, want codex-cli/codex", rec.ModelID)
	}
	if rec.UsageWindow == nil || rec.UsageWindow.UsedPercent == nil || *rec.UsageWindow.UsedPercent != 45.5 {
		t.Fatalf("UsageWindow = %+v, want used_percent from the newest file's last line (45.5)", rec.UsageWindow)
	}
	if rec.Provenance.Kind != catalog.KindMeasured || rec.Provenance.Source != SourceName {
		t.Fatalf("Provenance = %+v", rec.Provenance)
	}
	if err := rec.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestFetch_Negative(t *testing.T) {
	t.Run("missing directory", func(t *testing.T) {
		res := Fetch(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"))
		if res.Status != catalog.StatusSkip {
			t.Fatalf("Status = %v, want skip for a missing sessions directory", res.Status)
		}
	})

	t.Run("no jsonl files", func(t *testing.T) {
		dir := t.TempDir()
		writeSession(t, dir, "notes.txt", "irrelevant", time.Now())
		res := Fetch(context.Background(), dir)
		if res.Status != catalog.StatusSkip {
			t.Fatalf("Status = %v, want skip when no *.jsonl files exist", res.Status)
		}
	})

	t.Run("malformed newest session is not silently guessed", func(t *testing.T) {
		dir := t.TempDir()
		writeSession(t, dir, "broken.jsonl", "not json at all\n{\"payload\":", time.Now())
		res := Fetch(context.Background(), dir)
		// Malformed lines are skipped individually; a file with none parseable
		// yields a skip (no rate_limits found), never a fabricated record.
		if res.Status != catalog.StatusSkip {
			t.Fatalf("Status = %v, detail=%q, want skip for a file with no parseable rate_limits", res.Status, res.Detail)
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		dir := t.TempDir()
		writeSession(t, dir, "new.jsonl", validLine, time.Now())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res := Fetch(ctx, dir)
		if res.Status != catalog.StatusFail {
			t.Fatalf("Status = %v, want fail for an already-canceled context", res.Status)
		}
	})
}

// TestFetch_Boundary covers a session with rate_limits present but no
// primary window on every line (secondary-only), which must skip rather
// than fabricate.
func TestFetch_Boundary(t *testing.T) {
	dir := t.TempDir()
	line := `{"timestamp":"2026-09-13T12:56:47.753Z","ordinal":1,"type":"event_msg","payload":{"rate_limits":{"limit_id":"codex","primary":null,"secondary":null,"plan_type":"pro"}}}`
	writeSession(t, dir, "new.jsonl", line, time.Now())

	res := Fetch(context.Background(), dir)
	if res.Status != catalog.StatusSkip {
		t.Fatalf("Status = %v, detail=%q, want skip when rate_limits.primary is null", res.Status, res.Detail)
	}
}

// TestFetch_SessionEndsWithNullPrimary reproduces a real shape found live on
// this workstation on 2026-09-18: the newest session's very last
// rate_limits line has primary:null (a session-end event), while an
// earlier line in the same file has a real reading. Fetch must report that
// earlier reading, not skip -- the file has usable data, it just is not on
// the last line.
func TestFetch_SessionEndsWithNullPrimary(t *testing.T) {
	dir := t.TempDir()
	nullPrimaryLine := `{"timestamp":"2026-09-13T13:13:28.673Z","ordinal":2,"type":"event_msg","payload":{"rate_limits":{"limit_id":"codex","primary":null,"secondary":null,"plan_type":"pro"}}}`
	writeSession(t, dir, "new.jsonl", validLine+"\n"+nullPrimaryLine, time.Now())

	res := Fetch(context.Background(), dir)
	if res.Status != catalog.StatusOK {
		t.Fatalf("Status = %v, detail = %q, want ok using the earlier non-null reading", res.Status, res.Detail)
	}
	if res.Records[0].UsageWindow == nil || res.Records[0].UsageWindow.UsedPercent == nil || *res.Records[0].UsageWindow.UsedPercent != 92.0 {
		t.Fatalf("UsageWindow = %+v, want the earlier line's used_percent=92.0", res.Records[0].UsageWindow)
	}
}
