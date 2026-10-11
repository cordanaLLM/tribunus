package main

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestHelpOutputFramesEpitropAsGraphRouter(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	output, err := exec.CommandContext(ctx, "go", "run", ".", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("epitropctl --help: %v\n%s", err, output)
	}
	if want := []byte("epitropctl - model data sync for the Epitrop graph router\n"); !bytes.HasPrefix(output, want) {
		t.Fatalf("epitropctl --help = %q, want banner %q", output, want)
	}
	if old := []byte("Epitrop model catalog"); bytes.Contains(output, old) {
		t.Fatalf("epitropctl --help retains old framing %q: %q", old, output)
	}
	if !bytes.Contains(output, []byte("\nUsage:\n")) {
		t.Fatalf("epitropctl --help lost usage section: %q", output)
	}
	if !bytes.Contains(output, []byte("epitropctl schema")) {
		t.Fatalf("epitropctl --help missing schema command: %q", output)
	}
}

func TestSchemaCommandPrintsSnapshotSchema(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	output, err := exec.CommandContext(ctx, "go", "run", ".", "schema").CombinedOutput()
	if err != nil {
		t.Fatalf("epitropctl schema: %v\n%s", err, output)
	}
	if !bytes.Contains(output, []byte(`"schema_version"`)) {
		t.Fatalf("epitropctl schema output missing schema_version: %s", output)
	}
	if !bytes.Contains(output, []byte(`"https://github.com/tekmora/epitrop/catalog/snapshot.schema.json"`)) {
		t.Fatalf("epitropctl schema output missing $id: %s", output)
	}
}

func TestVersionOutputUsesInjectedVersion(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	output, err := exec.CommandContext(ctx, "go", "run", "-ldflags=-X main.version=v9.8.7", ".", "version").CombinedOutput()
	if err != nil {
		t.Fatalf("epitropctl version: %v\n%s", err, output)
	}
	if want := []byte("v9.8.7\n"); !bytes.Equal(output, want) {
		t.Fatalf("epitropctl version = %q, want %q", output, want)
	}
}
