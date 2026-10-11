//go:build !linux

package supervisor

import (
	"fmt"

	"github.com/tekmora/epitrop/internal/config"
)

func buildSandboxCommand(command []string, sandbox config.SandboxConfig, opts sandboxBuildOptions) (jobCommand, error) {
	return jobCommand{}, fmt.Errorf("%w: sandbox mode=enforce requires Linux", ErrNotSupported)
}

func sandboxPreflight(config.SandboxConfig) error {
	return fmt.Errorf("%w: sandbox mode=enforce requires Linux", ErrNotSupported)
}
