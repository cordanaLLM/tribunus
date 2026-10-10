//go:build linux

package supervisor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type heldLock struct {
	file *os.File
}

var (
	processSignaller    = unix.Kill
	processGroupID      = unix.Getpgid
	procStatusReadFile  = os.ReadFile
	procCmdlineReadFile = os.ReadFile
	shimProcessProbe    = unix.Kill
)

func platformSupported() error {
	return nil
}

func holdLock(path string) (*heldLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("supervisor: mkdir lock dir: %w", err)
	}
	// #nosec G304 -- lock path is derived from configured state dir plus checked job name.
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("supervisor: open lock %s: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	if err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, closeHeldLock(file, fmt.Errorf("supervisor: flock %s: %w", path, err))
	}
	return &heldLock{file: file}, nil
}

func lockHeld(path string) (bool, error) {
	lock, err := holdLock(path)
	if err == nil {
		return false, lock.Close()
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return true, nil
	}
	return false, err
}

func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &unix.SysProcAttr{Setpgid: true, Pdeathsig: unix.SIGKILL}
}

// errProcessGone marks a signal target that no longer exists. There is nothing to signal,
// so callers treat it like the ESRCH the signal itself would return: done, nothing sent.
var errProcessGone = errors.New("process is gone")

func signalProcessGroup(target jobSignalTarget, sig string) error {
	signal, err := signalFromName(sig)
	if err != nil {
		return err
	}
	if err = validateSignalTarget(target); err != nil {
		if errors.Is(err, errProcessGone) {
			return nil
		}
		return err
	}
	if err = processSignaller(-target.PID, signal); err != nil && !errors.Is(err, unix.ESRCH) {
		return fmt.Errorf("supervisor: signal %s pid %d: %w", sig, target.PID, err)
	}
	return nil
}

func killProcess(target jobSignalTarget) error {
	if err := validateSignalTarget(target); err != nil {
		if errors.Is(err, errProcessGone) {
			return nil
		}
		return err
	}
	return killStartedProcessGroup(target.PID)
}

func killStartedProcessGroup(pid int) error {
	if err := validateStartedProcessGroup(pid); err != nil {
		if errors.Is(err, errProcessGone) {
			return nil
		}
		return err
	}
	if err := processSignaller(-pid, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
		return fmt.Errorf("supervisor: SIGKILL pid %d: %w", pid, err)
	}
	return nil
}

func validateStartedProcessGroup(pid int) error {
	if pid <= 1 {
		return fmt.Errorf("supervisor: refuse signal: pid %d is not safe", pid)
	}
	if pid == os.Getpid() || pid == unix.Getpgrp() {
		return fmt.Errorf("supervisor: refuse signal: pid %d targets supervisor", pid)
	}
	pgid, err := processGroupID(pid)
	if errors.Is(err, unix.ESRCH) {
		return fmt.Errorf("supervisor: pid %d: %w", pid, errProcessGone)
	}
	if err != nil {
		return fmt.Errorf("supervisor: refuse signal: getpgid %d: %w", pid, err)
	}
	if pgid != pid {
		return fmt.Errorf("supervisor: refuse signal: pid %d is not a process-group leader", pid)
	}
	return nil
}

func validateSignalTarget(target jobSignalTarget) error {
	if err := validateStartedProcessGroup(target.PID); err != nil {
		return signalRecordError(target, err)
	}
	if target.PID == target.ShimPID {
		return signalRecordReason(target, "pid equals shim_pid")
	}
	if err := validateSignalParent(target); err != nil {
		return err
	}
	return validateSignalShim(target)
}

func validateSignalParent(target jobSignalTarget) error {
	parent, err := linuxProcessParent(target.PID)
	if errors.Is(err, fs.ErrNotExist) {
		return signalRecordError(target, fmt.Errorf("pid %d: %w", target.PID, errProcessGone))
	}
	if err != nil {
		return signalRecordError(target, err)
	}
	if parent != target.ShimPID {
		return signalRecordReason(target, fmt.Sprintf("parent pid %d does not match shim_pid %d", parent, target.ShimPID))
	}
	return nil
}

func validateSignalShim(target jobSignalTarget) error {
	if err := shimProcessProbe(target.ShimPID, 0); err != nil {
		return signalRecordError(target, fmt.Errorf("shim_pid %d is not alive: %w", target.ShimPID, err))
	}
	held, err := lockHeld(target.LockPath)
	if err != nil {
		return signalRecordError(target, err)
	}
	if !held {
		return signalRecordReason(target, "job lock is not held")
	}
	reason, err := shimCmdlineMismatch(target)
	if err != nil {
		return signalRecordError(target, err)
	}
	if reason != "" {
		return signalRecordReason(target, reason)
	}
	return nil
}

func linuxProcessParent(pid int) (int, error) {
	body, err := procStatusReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, err
	}
	lines := strings.Split(string(body), "\n")
	for i := 0; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "PPid:") {
			return strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(lines[i], "PPid:")))
		}
	}
	return 0, fmt.Errorf("PPid missing for %d", pid)
}

// shimCmdlineMismatch reads the shim's command line once and returns why it is not this
// job's shim, or "" when it is. An unreadable or empty command line (a zombie or a kernel
// thread) is an error, never a match.
func shimCmdlineMismatch(target jobSignalTarget) (string, error) {
	body, err := procCmdlineReadFile(filepath.Join("/proc", strconv.Itoa(target.ShimPID), "cmdline"))
	if err != nil {
		return "", err
	}
	// strings.Split always returns at least one element; it is "" only for an empty line.
	args := strings.Split(strings.TrimRight(string(body), "\x00"), "\x00")
	if args[0] == "" {
		return "", fmt.Errorf("cmdline of shim_pid %d is empty", target.ShimPID)
	}
	if !hasArg(args, "__job-shim") {
		return "__job-shim missing", nil
	}
	if !hasArgPair(args, "--name", target.Name) {
		return "--name mismatch", nil
	}
	if !hasArgPair(args, "--lock", target.LockPath) {
		return "--lock mismatch", nil
	}
	if !hasArgPair(args, "--record", target.RecordPath) {
		return "--record mismatch", nil
	}
	return "", nil
}

func hasArg(args []string, want string) bool {
	for i := 0; i < len(args); i++ {
		if args[i] == want {
			return true
		}
	}
	return false
}

func hasArgPair(args []string, key string, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func signalRecordError(target jobSignalTarget, err error) error {
	return fmt.Errorf("supervisor: record %s: refuse signal for %s: %w", target.RecordPath, target.Name, err)
}

func signalRecordReason(target jobSignalTarget, reason string) error {
	return fmt.Errorf("supervisor: record %s: refuse signal for %s: %s", target.RecordPath, target.Name, reason)
}

func signalFromName(name string) (unix.Signal, error) {
	switch name {
	case "TERM":
		return unix.SIGTERM, nil
	case "INT":
		return unix.SIGINT, nil
	case "HUP":
		return unix.SIGHUP, nil
	}
	return 0, fmt.Errorf("supervisor: unsupported signal %q", name)
}

func (l *heldLock) Close() error {
	if err := unix.Flock(int(l.file.Fd()), unix.LOCK_UN); err != nil {
		return closeHeldLock(l.file, fmt.Errorf("supervisor: unlock %s: %w", l.file.Name(), err))
	}
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("supervisor: close lock %s: %w", l.file.Name(), err)
	}
	return nil
}

func closeHeldLock(file *os.File, cause error) error {
	if err := file.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("supervisor: close lock %s: %w", file.Name(), err))
	}
	return cause
}
