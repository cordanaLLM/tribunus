//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package eventlog

import (
	"context"
	"fmt"
	"time"
)

type fileLock struct{}

func acquireLock(context.Context, string, time.Duration) (*fileLock, error) {
	return nil, fmt.Errorf("eventlog: platform has no supported exclusive file lock")
}

func (l *fileLock) Close() error {
	return nil
}
