package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// cliBuildTimeout bounds one build of the command. It is separate from the deadline a test
// gives the run: a cold build on a loaded runner can take longer than the run itself is
// allowed to.
const cliBuildTimeout = 5 * time.Minute

// cliTestVersion is set as main.version in the shared build, so the version test needs no
// build of its own.
const cliTestVersion = "v9.8.7"

// cliBuild holds the one build of the command that the tests running it share.
var cliBuild struct {
	once sync.Once
	dir  string
	bin  string
	err  error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if cliBuild.dir != "" {
		if err := os.RemoveAll(cliBuild.dir); err != nil {
			fmt.Fprintf(os.Stderr, "remove the command build: %v\n", err)
		}
	}
	os.Exit(code)
}

// buildCLI returns the path of the command, built once for this test process with
// cliTestVersion as its version.
func buildCLI(t *testing.T) string {
	t.Helper()
	cliBuild.once.Do(func() {
		cliBuild.bin, cliBuild.err = buildCLIOnce()
	})
	if cliBuild.err != nil {
		t.Fatal(cliBuild.err)
	}
	return cliBuild.bin
}

func buildCLIOnce() (string, error) {
	dir, err := os.MkdirTemp("", "epitropctl-test-")
	if err != nil {
		return "", fmt.Errorf("create the build directory: %w", err)
	}
	cliBuild.dir = dir
	ctx, cancel := context.WithTimeout(context.Background(), cliBuildTimeout)
	defer cancel()
	bin := filepath.Join(dir, "epitropctl")
	out, err := exec.CommandContext(ctx, "go", "build", "-ldflags=-X main.version="+cliTestVersion, "-o", bin, ".").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("build epitropctl: %w\n%s", err, out)
	}
	return bin, nil
}
