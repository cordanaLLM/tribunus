// Package gitexec runs git for the control plane. It never prompts and reads no
// configuration but the repository's own: the caller's git variables and the operator's
// global and system settings are dropped, so a command acts on the repository it names and
// on nothing a hook or a shell handed down.
package gitexec

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Env returns the environment git runs in: environ without any of git's own variables,
// then this package's settings, then extra.
//
// Git exports GIT_DIR, GIT_WORK_TREE and GIT_INDEX_FILE to every hook it runs. A runner
// started from a hook would hand them on, and a command meant for one repository would then
// act on the hook's. GIT_CONFIG_COUNT and GIT_CONFIG_PARAMETERS would carry settings in the
// same way. None of git's variables is needed, so all of them are dropped; a caller that
// needs one passes it in extra.
func Env(environ []string, extra ...string) []string {
	env := make([]string, 0, len(environ)+4+len(extra))
	for i := 0; i < len(environ); i++ {
		name, _, _ := strings.Cut(environ[i], "=")
		if !strings.HasPrefix(strings.ToUpper(name), "GIT_") {
			env = append(env, environ[i])
		}
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "LC_ALL=C")
	return append(env, extra...)
}

// Run runs one git command in dir when dir is set, with env as its whole environment and
// stdin as its input when stdin is not nil, and returns its trimmed output. An error carries
// git's own message; it wraps the *exec.ExitError when git ran and failed.
func Run(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) (string, error) {
	// #nosec G204 -- callers pass fixed subcommands; names they take from input follow
	// "--" or are checked not to start with "-".
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}
