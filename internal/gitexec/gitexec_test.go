package gitexec

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestEnvDropsGitsOwnVariablesOnly(t *testing.T) {
	got := Env([]string{
		"PATH=/usr/bin", "GIT_DIR=/elsewhere/.git", "GIT_WORK_TREE=/elsewhere", "GIT_INDEX_FILE=/elsewhere/.git/index",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.editor", "GIT_CONFIG_VALUE_0=/elsewhere/editor",
		"GIT_CONFIG_GLOBAL=/elsewhere/gitconfig", "git_dir=/lower-case", "GITHUB_TOKEN=kept", "GITEA=kept", "HOME=/home/someone",
	}, "GIT_INDEX_FILE=/mine/index")
	want := []string{
		"PATH=/usr/bin", "GITHUB_TOKEN=kept", "GITEA=kept", "HOME=/home/someone",
		"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull, "LC_ALL=C",
		"GIT_INDEX_FILE=/mine/index",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Env() = %q, want %q", got, want)
	}
}

func TestRunFeedsInputAndReportsGitsMessage(t *testing.T) {
	dir := t.TempDir()
	env := Env(os.Environ())
	if _, err := Run(t.Context(), dir, env, nil, "init", "--quiet"); err != nil {
		t.Fatalf("Run(init) = %v, want nil", err)
	}
	oid, err := Run(t.Context(), dir, env, strings.NewReader("blob"), "hash-object", "--stdin")
	if err != nil || oid != "43ae31d836edd668d12fb87a494e340c2c5a8698" {
		t.Fatalf("Run(hash-object) = %q, %v; want the id of the input", oid, err)
	}
	_, err = Run(t.Context(), dir, env, nil, "rev-parse", "--verify", "refs/heads/absent")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || !strings.Contains(err.Error(), "fatal") {
		t.Fatalf("Run(rev-parse absent) = %v, want an exit error with git's message", err)
	}
}
