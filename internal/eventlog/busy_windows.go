//go:build windows

package eventlog

import (
	"errors"

	"golang.org/x/sys/windows"
)

// fileBusy reports a sharing violation: Windows refuses to open a file that another handle
// holds without sharing, as an append's rename does for a moment.
func fileBusy(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
