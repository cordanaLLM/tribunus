//go:build linux

package supervisor

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/tekmora/epitrop/internal/config"
)

// The cutover 1 example (docs/cutover-1.md) is a configuration the supervisor accepts, and it
// declares one job per background job the plan moves plus the shadow job of the parallel run.
func TestCutoverOneExampleIsASupervisorConfig(t *testing.T) {
	cfg, err := config.Load(testContext(t), filepath.Join("..", "..", "docs", "cutover-1.example.yaml"))
	if err != nil {
		t.Fatalf("Load(docs/cutover-1.example.yaml) = %v, want nil", err)
	}
	if err = checkConfig(cfg); err != nil {
		t.Fatalf("checkConfig(cutover 1) = %v, want nil", err)
	}
	names := make([]string, 0, len(cfg.Jobs))
	for _, job := range cfg.Jobs {
		names = append(names, job.Name)
	}
	want := []string{"landing-daemon", "auto-rebase", "update-intake", "fleet-monitor", "scratch-cleanup", "auto-rebase-shadow"}
	if !slices.Equal(names, want) {
		t.Fatalf("cutover 1 jobs = %v, want %v", names, want)
	}
}
