package releasewatch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFenceNotesCannotBeClosedByTheNotes(t *testing.T) {
	notes := "thanks @someone, fixes #12 and other/repo#3\n```\nescape attempt\n````"
	got := fenceNotes(notes)
	if !strings.HasPrefix(got, "`````text\n") || !strings.HasSuffix(got, "\n`````") {
		t.Fatalf("fenceNotes = %q, want a five-backtick fence around notes with a four-backtick run", got)
	}
	if plain := fenceNotes("no backticks"); plain != "```text\nno backticks\n```" {
		t.Fatalf("fenceNotes(plain) = %q, want a three-backtick fence", plain)
	}
}

func TestIssueMarkerEncodesCommentTerminators(t *testing.T) {
	got := issueMarker("github:o/r@v1-->@someone")
	if strings.Count(got, "-->") != 1 || !strings.HasSuffix(got, " -->") {
		t.Fatalf("issueMarker = %q, want the only --> to be the comment end", got)
	}
}

// TestFiledIssueFencesNotesAndEncodesMarker checks the body actually sent to GitHub.
func TestFiledIssueFencesNotesAndEncodesMarker(t *testing.T) {
	var posted string
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/releases"):
			testWrite(w, `[{"tag_name":"v1-->x","html_url":"https://example.invalid/r","body":"cc @maintainer, see #99"}]`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues"):
			testWrite(w, `[]`)
		case r.Method == http.MethodPost:
			var payload GitHubIssuePayload
			body, _ := io.ReadAll(r.Body) //nolint:errcheck // test capture
			if err := json.Unmarshal(body, &payload); err == nil {
				posted = payload.Body
			}
			w.WriteHeader(http.StatusCreated)
			testWrite(w, `{"number":7}`)
		}
	}))
	defer gh.Close()
	svc, err := NewService(oneRouteConfig(t, 100), ClientOptions{GitHubBase: gh.URL, Stdout: &bytes.Buffer{}, LookupEnv: func(string) (string, bool) { return "tok", true }})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err = svc.RunPass(context.Background()); err != nil {
		t.Fatalf("RunPass = %v, want nil", err)
	}
	if !strings.HasPrefix(posted, "<!-- release-watch: github:owner/upstream@v1--%3Ex -->") {
		t.Fatalf("posted body = %q, want the encoded marker first", posted)
	}
	if !strings.Contains(posted, "```text\ncc @maintainer, see #99\n```") {
		t.Fatalf("posted body = %q, want the notes inside a code fence", posted)
	}
}

func TestTruncateRunesKeepsWholeRunes(t *testing.T) {
	long := strings.Repeat("é", MaxNotesRunes+50)
	got := truncateRunes(long, MaxNotesRunes)
	if n := len([]rune(got)); n != MaxNotesRunes {
		t.Fatalf("truncateRunes kept %d runes, want %d", n, MaxNotesRunes)
	}
	if short := truncateRunes("short", MaxNotesRunes); short != "short" {
		t.Fatalf("truncateRunes(short) = %q, want it unchanged", short)
	}
}

// TestIssueRefusedByGitHubIsNotFiled: any answer but 201 Created is a failed delivery, even
// with a JSON body, and the release stays unseen.
func TestIssueRefusedByGitHubIsNotFiled(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusUnprocessableEntity)
			testWrite(w, `{"message":"Validation Failed"}`)
		case strings.Contains(r.URL.Path, "/releases"):
			testWrite(w, `[{"tag_name":"v2"}]`)
		default:
			testWrite(w, `[]`)
		}
	}))
	defer gh.Close()
	var stdout bytes.Buffer
	svc, err := NewService(oneRouteConfig(t, 100), ClientOptions{GitHubBase: gh.URL, Stdout: &stdout, LookupEnv: func(string) (string, bool) { return "tok", true }})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err = svc.RunPass(context.Background()); err == nil {
		t.Fatal("RunPass with a 422 issue reply = nil, want a failed delivery")
	}
	if svc.State().Has("github:owner/upstream@v2") || !strings.Contains(stdout.String(), "HTTP 422") {
		t.Fatalf("seen=%v stdout=%q, want unseen and the 422 logged", svc.State().Has("github:owner/upstream@v2"), stdout.String())
	}
}

// TestSourceErrorStatusIsNotAnEmptyResult: an error status is a failed source even when its
// body parses as an empty list; it must not read as "no new releases".
func TestSourceErrorStatusIsNotAnEmptyResult(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The tags fallback answers cleanly, so only the releases status can fail the source.
		if strings.Contains(r.URL.Path, "/releases") {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		testWrite(w, `[]`)
	}))
	defer gh.Close()
	svc, err := NewService(oneRouteConfig(t, 100), ClientOptions{GitHubBase: gh.URL, Stdout: &bytes.Buffer{}, LookupEnv: func(string) (string, bool) { return "", false }})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err = svc.RunPass(context.Background()); err == nil || !strings.Contains(err.Error(), "github:owner/upstream") {
		t.Fatalf("RunPass with a 503 source = %v, want the source named as failed", err)
	}
}
