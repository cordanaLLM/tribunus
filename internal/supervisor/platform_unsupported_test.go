//go:build !linux

package supervisor

import (
	"errors"
	"testing"

	"github.com/tekmora/epitrop/internal/config"
)

func TestUnsupportedPlatformReportsNotSupported(t *testing.T) {
	_, err := New(config.Default(), Options{})
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("New() = %v, want ErrNotSupported", err)
	}
}
