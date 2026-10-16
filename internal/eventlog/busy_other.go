//go:build !windows

package eventlog

// fileBusy is false outside Windows: no other platform refuses a read while a file is renamed.
func fileBusy(error) bool {
	return false
}
