package main

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestHelpOutputFramesTribunusAsGraphRouter(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	output, err := exec.CommandContext(ctx, "go", "run", ".", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("tribunusctl --help: %v\n%s", err, output)
	}
	if want := []byte("tribunusctl - model data sync for the Tribunus graph router\n"); !bytes.HasPrefix(output, want) {
		t.Fatalf("tribunusctl --help = %q, want banner %q", output, want)
	}
	if old := []byte("Tribunus model catalog"); bytes.Contains(output, old) {
		t.Fatalf("tribunusctl --help retains old framing %q: %q", old, output)
	}
	if !bytes.Contains(output, []byte("\nUsage:\n")) {
		t.Fatalf("tribunusctl --help lost usage section: %q", output)
	}
}
