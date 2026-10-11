//go:build linux

package egressproxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// stopScript is a command that handles the stop signal itself and then exits with 42.
const stopScript = `trap 'exit 42' TERM; echo ready; sleep 30 & wait`

// startRelayGroup starts the relay as the leader of its own process group, as a job is under
// its supervisor, and returns once the command has printed its first line.
func startRelayGroup(ctx context.Context, t *testing.T, socket string, listen string, command ...string) (*exec.Cmd, *bufio.Reader, string) {
	t.Helper()
	cmd := relayCommand(ctx, t, socket, listen, command...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe() = %v, want nil", err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatalf("start the relay = %v, want nil", err)
	}
	reader := bufio.NewReader(out)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("first line of the command = %q, %v, want a line", line, errors.Join(err, cmd.Process.Kill(), cmd.Wait()))
	}
	return cmd, reader, strings.TrimSpace(line)
}

// endRelayGroup waits for the relay and returns its exit code.
func endRelayGroup(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	var exit *exec.ExitError
	if err := cmd.Wait(); err != nil && !errors.As(err, &exit) {
		t.Fatalf("wait for the relay = %v, want its exit", err)
	}
	return cmd.ProcessState.ExitCode()
}

// TestRelayOutlivesTheStopSignal: the supervisor's stop signal goes to the whole process
// group. The relay must outlive it, or the sandbox ends before the command has handled the
// signal; the command, started through env --default-signal=TERM as the sandbox builder does,
// gets it and its exit code is the relay's.
func TestRelayOutlivesTheStopSignal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*testWait)
	defer cancel()
	cmd, _, line := startRelayGroup(ctx, t, shortDir(t)+"/none", freeLoopback(t), "env", "--default-signal=TERM", "sh", "-c", stopScript)
	if line != "ready" {
		t.Errorf("first line = %q, want ready", line)
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatalf("Kill(group, TERM) = %v, want nil", err)
	}
	if code := endRelayGroup(t, cmd); code != 42 {
		t.Fatalf("relay exit = %d, want 42 from the command's own handling of the signal", code)
	}
}

// TestRelayCommandInheritsTheIgnoredSignal is why the sandbox builder puts env
// --default-signal=TERM behind the relay: a command started without it ignores the stop
// signal as the relay does, and runs on.
func TestRelayCommandInheritsTheIgnoredSignal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*testWait)
	defer cancel()
	cmd, _, line := startRelayGroup(ctx, t, shortDir(t)+"/none", freeLoopback(t), "sh", "-c", "echo ready; sleep 1; exit 9")
	if line != "ready" {
		t.Errorf("first line = %q, want ready", line)
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatalf("Kill(group, TERM) = %v, want nil", err)
	}
	if code := endRelayGroup(t, cmd); code != 9 {
		t.Fatalf("relay exit = %d, want 9: the command ran to its end", code)
	}
}

func threadCount(pid int) (int, error) {
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}
	lines := strings.Split(string(status), "\n")
	for i := 0; i < len(lines); i++ {
		if value, ok := strings.CutPrefix(lines[i], "Threads:"); ok {
			return strconv.Atoi(strings.TrimSpace(value))
		}
	}
	return 0, fmt.Errorf("no Threads line in /proc/%d/status", pid)
}

// openTunnels opens count tunnels to target through the relay at listen and keeps them open
// until the test ends.
func openTunnels(t *testing.T, listen string, target string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		conn, err := (&net.Dialer{Timeout: testWait}).DialContext(context.Background(), "tcp", listen)
		if err != nil {
			t.Fatalf("tunnel %d: connect to the relay = %v, want nil", i, err)
		}
		t.Cleanup(func() {
			if err := conn.Close(); err != nil {
				t.Errorf("tunnel %d: Close() = %v, want nil", i, err)
			}
		})
		if err = conn.SetDeadline(time.Now().Add(testWait)); err != nil {
			t.Fatalf("tunnel %d: SetDeadline() = %v, want nil", i, err)
		}
		if _, err = io.WriteString(conn, connectRequest(target)); err != nil {
			t.Fatalf("tunnel %d: send the request = %v, want nil", i, err)
		}
		status, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil || !strings.Contains(status, " 200 ") {
			t.Fatalf("tunnel %d: status = %q, %v, want 200", i, status, err)
		}
	}
}

// TestRelayThreadCount: the relay's threads count against the task limit of the job it runs
// in front of. With every tunnel it can carry open at once it stays within MaxRelayTasks,
// the room a job with a proxy must have beside its own tasks.
func TestRelayThreadCount(t *testing.T) {
	target := upstream(t)
	server := startServer(t, Options{Destinations: []string{target}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*testWait)
	defer cancel()
	listen := freeLoopback(t)
	cmd, _, line := startRelayGroup(ctx, t, server.SocketPath(), listen, "env", "--default-signal=TERM", "sh", "-c", stopScript)
	if line != "ready" {
		t.Errorf("first line = %q, want ready", line)
	}
	before, beforeErr := threadCount(cmd.Process.Pid)
	openTunnels(t, listen, target, maxTunnels)
	threads, err := threadCount(cmd.Process.Pid)
	if killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); killErr != nil {
		t.Fatalf("Kill(group, TERM) = %v, want nil", killErr)
	}
	if code := endRelayGroup(t, cmd); code != 42 {
		t.Errorf("relay exit = %d, want 42 from the command", code)
	}
	t.Logf("relay threads: %d idle, %d with %d tunnels open", before, threads, maxTunnels)
	if err != nil || beforeErr != nil || threads < 1 || threads > MaxRelayTasks {
		t.Fatalf("threads = %d, %v, %v, want 1..%d", threads, err, beforeErr, MaxRelayTasks)
	}
}

// TestRelayRunsOnAFixedNumberOfProcessors: a relay started without the variable the sandbox
// sets still runs on RelayProcs processors. That alone does not bound its threads: the
// runtime has started some by then, more on a host with more processors (see RelayProcs).
func TestRelayRunsOnAFixedNumberOfProcessors(t *testing.T) {
	before := runtime.GOMAXPROCS(RelayProcs + 2)
	t.Cleanup(func() {
		runtime.GOMAXPROCS(before)
		signal.Reset(syscall.SIGTERM)
	})
	code, err := runRelay(relayConfig{Socket: filepath.Join(shortDir(t), "none.sock"), Listen: freeLoopback(t), Command: []string{"true"}})
	if code != 0 || err != nil {
		t.Fatalf("runRelay() = %d, %v, want 0 and nil", code, err)
	}
	if got := runtime.GOMAXPROCS(0); got != RelayProcs {
		t.Errorf("the relay ran on %d processors, want %d", got, RelayProcs)
	}
}
