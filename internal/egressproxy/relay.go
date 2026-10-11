package egressproxy

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	// RelayCommand is the first argument that makes a program run as the relay.
	RelayCommand = "__egress-relay"

	// MaxRelayLife bounds the relay and its command from inside the sandbox. It is above
	// the longest runtime a job may have, so the supervisor's deadline always comes first.
	MaxRelayLife = 366 * 24 * time.Hour

	// RelayFailed is the exit code when the relay itself failed, as env and timeout use
	// it. Every other code is the command's.
	RelayFailed = 125

	// RelayProcs is the number of processors the relay runs on; its threads count against
	// the job's task limit. The sandbox puts it into the relay's environment as GOMAXPROCS:
	// the Go runtime sizes itself from the host's processors before the relay's first line
	// runs (13 to 14 threads measured on 32 processors, 5 to 7 with the variable).
	RelayProcs = 2
	// MaxRelayTasks is how many of a job's tasks the relay may take: the 7 measured, and
	// room for threads the runtime adds while others wait in a system call. A job with a
	// proxy needs a task limit that leaves this room beside its own tasks.
	MaxRelayTasks = 16
	// signalExitBase plus the signal's number is the exit code after a signal ended the
	// command, as a shell and bwrap report it.
	signalExitBase = 128
)

type relayConfig struct {
	Socket  string
	Listen  string
	Command []string
}

// RunRelay is the relay as a program:
//
//	__egress-relay --socket <path> --listen <loopback address:port> -- <command> [args]
//
// It listens first, then starts the command, and gives every connection on the listening
// address a connection to the socket. It checks nothing: the server at the other end of the
// socket decides what is reached. The exit code is the command's, 128 plus the signal's
// number when a signal ended the command, and RelayFailed when the relay failed.
func RunRelay(args []string, stderr io.Writer) int {
	cfg, err := parseRelayArgs(args)
	if err != nil {
		return reported(stderr, RelayFailed, err)
	}
	code, err := runRelay(cfg)
	return reported(stderr, code, err)
}

// reported writes err as one line of the relay and returns the exit code. A fault nobody
// can read must not pass as a clean run: when the line cannot be written, a code of 0
// becomes RelayFailed.
func reported(stderr io.Writer, code int, err error) int {
	if err == nil {
		return code
	}
	if _, writeErr := fmt.Fprintf(stderr, "epitrop: egress relay: %s\n", ErrorLine(err)); writeErr != nil && code == 0 {
		return RelayFailed
	}
	return code
}

// ErrorLine is err as one line. Joined errors print one to a line, and in the log of a job
// every line after the first would read as the job's own output.
func ErrorLine(err error) string {
	return oneLine(err.Error())
}

// oneLine is text without a line end inside it.
func oneLine(text string) string {
	return strings.ReplaceAll(text, "\n", "; ")
}

func parseRelayArgs(args []string) (relayConfig, error) {
	if len(args) > 0 && args[0] == RelayCommand {
		args = args[1:]
	}
	var cfg relayConfig
	fs := flag.NewFlagSet("egress-relay", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.Socket, "socket", "", "")
	fs.StringVar(&cfg.Listen, "listen", "", "")
	if err := fs.Parse(args); err != nil {
		return relayConfig{}, fmt.Errorf("parse: %w", err)
	}
	cfg.Command = fs.Args()
	if cfg.Socket == "" || len(cfg.Command) == 0 {
		return relayConfig{}, errors.New("a socket and a command are required")
	}
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return relayConfig{}, fmt.Errorf("listen address %q: want address:port", cfg.Listen)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return relayConfig{}, fmt.Errorf("listen address %q: the relay listens on a loopback address only", cfg.Listen)
	}
	return cfg, nil
}

func runRelay(cfg relayConfig) (int, error) {
	runtime.GOMAXPROCS(RelayProcs)
	// The stop signal goes to the whole process group of the job. The relay ignores it, as
	// the sandbox above it does, and the command gets it back at its default before it
	// starts. Until this line a stop signal ends the relay, and with it a sandbox in which
	// the command has not started yet.
	signal.Ignore(syscall.SIGTERM)
	ctx, cancel := context.WithTimeout(context.Background(), MaxRelayLife)
	defer cancel()
	listener, err := listenLoopback(ctx, cfg.Listen)
	if err != nil {
		return RelayFailed, err
	}
	served := make(chan error, 1)
	go func() { served <- serveRelay(ctx, listener, cfg.Socket) }()
	code, err := runCommand(ctx, cfg.Command)
	cancel()
	return code, errors.Join(err, <-served)
}

func listenLoopback(ctx context.Context, address string) (*net.TCPListener, error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", address, err)
	}
	tcp, ok := listener.(*net.TCPListener)
	if !ok {
		return nil, errors.Join(fmt.Errorf("listen on %s: not a TCP listener", address), closed(listener))
	}
	return tcp, nil
}

// serveRelay connects every accepted connection to the socket until ctx ends.
func serveRelay(ctx context.Context, listener *net.TCPListener, socket string) error {
	relays := newPool()
	deadline := tunnelDeadline(ctx)
	return relays.serve(ctx, listener, deadline,
		func(conn net.Conn) { relays.fault(relayOne(ctx, conn, socket, deadline)) },
		func(net.Conn) {})
}

func relayOne(ctx context.Context, client net.Conn, socket string, deadline time.Time) (err error) {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	upstream, err := (&net.Dialer{}).DialContext(dialCtx, "unix", socket)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("connect to the proxy socket %s: %w", socket, err)
	}
	defer func() { err = errors.Join(err, closed(upstream)) }()
	if err = errors.Join(client.SetDeadline(deadline), upstream.SetDeadline(deadline)); err != nil {
		return fmt.Errorf("tunnel deadline: %w", err)
	}
	if err = pipe(ctx, client, client, upstream); peerEnded(err) {
		// The program in the sandbox ended the tunnel in its own way, or the proxy closed a
		// request it refused. Neither is a fault of the relay, and what the two directions
		// report beside it follows from it.
		return nil
	}
	return err
}

// runCommand runs the command with the relay's own input and output and returns its exit
// code.
func runCommand(ctx context.Context, command []string) (int, error) {
	// #nosec G204 -- the argv is the job command the sandbox builder put behind the relay.
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return RelayFailed, fmt.Errorf("start %s: %w", command[0], err)
	}
	var exit *exec.ExitError
	if err := cmd.Wait(); err != nil && !errors.As(err, &exit) {
		return RelayFailed, fmt.Errorf("wait for %s: %w", command[0], err)
	}
	if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return signalExitBase + int(status.Signal()), nil
	}
	return cmd.ProcessState.ExitCode(), nil
}
