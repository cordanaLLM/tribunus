package releasewatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSeenFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, SeenFileName), []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile(seen-set) = %v", err)
	}
	return dir
}

func TestLoadStateReadsVersionOne(t *testing.T) {
	dir := writeSeenFile(t, `{"version":1,"seen":{"github:o/r@v1":"2026-10-16T00:00:00Z"}}`)
	st, err := LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState(version 1) = %v, want nil", err)
	}
	if !st.Has("github:o/r@v1") {
		t.Fatalf("LoadState(version 1) lost the seen entry")
	}
}

// A file of another version, or one without a version, is refused rather than read as
// version 1: a later binary's file could hold fields this one would drop on its next save.
func TestLoadStateRefusesOtherVersions(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"newer", `{"version":2,"seen":{}}`, "has version 2, want 1"},
		{"missing", `{"seen":{}}`, "has version 0, want 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadState(writeSeenFile(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadState(%s) = %v, want an error containing %q", tc.body, err, tc.want)
			}
		})
	}
}
