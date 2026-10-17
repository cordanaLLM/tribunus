//go:build !linux

package supervisor

import "testing"

// The shim, process groups and the /proc reads of the soak test are Linux only.
func TestSoakSupervisorStartStop(t *testing.T) {
	t.Skip("soak test: the job shim runs on Linux only")
}
