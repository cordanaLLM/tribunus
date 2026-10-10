//go:build !linux

package supervisor

import (
	"fmt"
	"os/exec"
)

type heldLock struct{}

func platformSupported() error {
	return fmt.Errorf("%w: missing linux parent-death signal, flock and process-group signals", ErrNotSupported)
}

func holdLock(path string) (*heldLock, error) {
	return nil, fmt.Errorf("%w: missing flock for %s", ErrNotSupported, path)
}

func lockHeld(path string) (bool, error) {
	return false, fmt.Errorf("%w: missing flock for %s", ErrNotSupported, path)
}

func prepareProcess(cmd *exec.Cmd) {
}

func signalProcessGroup(target jobSignalTarget, sig string) error {
	return fmt.Errorf("%w: missing process-group signal %s for pid %d", ErrNotSupported, sig, target.PID)
}

func killProcess(target jobSignalTarget) error {
	return fmt.Errorf("%w: missing process-group SIGKILL for pid %d", ErrNotSupported, target.PID)
}

func killStartedProcessGroup(pid int) error {
	return fmt.Errorf("%w: missing process-group SIGKILL for pid %d", ErrNotSupported, pid)
}

func (l *heldLock) Close() error {
	return nil
}
