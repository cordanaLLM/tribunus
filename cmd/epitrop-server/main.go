// Command epitrop-server serves the run record over the Model Context Protocol, on stdio
// or over HTTP on a loopback address. Every tool it serves only reads; see internal/mcp.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tekmora/epitrop/internal/config"
	"github.com/tekmora/epitrop/internal/logsetup"
	"github.com/tekmora/epitrop/internal/mcp"
	"github.com/tekmora/epitrop/internal/supervisor"
)

// version is set at link time.
var version string

const (
	configLoadTimeout = 30 * time.Second
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 10 * time.Second
	// watchInterval is how often the HTTP transport replays the event log for readiness
	// and the event stream.
	watchInterval = 2 * time.Second
)

// newSupervisor opens the supervisor; tests replace it.
var newSupervisor = supervisor.New

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := serve(ctx, os.Args[1:], os.Stdin, os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// serve loads the config named by args and serves the tools on the configured transport
// until the client disconnects or ctx ends. On stdio nothing but protocol frames is
// written to out; on HTTP out gets the address served.
func serve(ctx context.Context, args []string, in io.ReadCloser, out io.WriteCloser) error {
	cfg, src, err := load(args)
	if err != nil {
		return err
	}
	if cfg.Server.Transport == config.TransportHTTP {
		return serveHTTP(ctx, cfg.Server.Listen, src, out)
	}
	err = mcp.NewServer(appVersion(), src).Run(ctx, &sdkmcp.IOTransport{Reader: in, Writer: out})
	if err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("epitrop-server: %w", err)
	}
	return nil
}

// serveHTTP serves the tools, probes and event stream on addr until ctx ends, then stops
// within shutdownTimeout. Request contexts end with ctx, so open streams close.
func serveHTTP(ctx context.Context, addr string, src mcp.Sources, out io.Writer) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("epitrop-server: %w", err)
	}
	h := mcp.NewHTTP(appVersion(), src, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	srv := &http.Server{
		Handler:           h.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	go h.Watch(ctx, watchInterval)
	if _, err := fmt.Fprintf(out, "epitrop-server: serving http://%s%s\n", ln.Addr(), mcp.PathMCP); err != nil {
		return errors.Join(fmt.Errorf("epitrop-server: %w", err), srv.Close())
	}
	select {
	case err := <-served:
		return fmt.Errorf("epitrop-server: %w", err)
	case <-ctx.Done():
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(stopCtx); err != nil {
		return errors.Join(fmt.Errorf("epitrop-server: shutdown: %w", err), srv.Close())
	}
	return nil
}

// load parses args, loads the config and opens what the tools read.
func load(args []string) (config.Config, mcp.Sources, error) {
	fs := flag.NewFlagSet("epitrop-server", flag.ContinueOnError)
	configPath := fs.String("config", "", "configuration file")
	if err := fs.Parse(args); err != nil {
		return config.Config{}, mcp.Sources{}, err
	}
	if *configPath == "" {
		return config.Config{}, mcp.Sources{}, errors.New("epitrop-server: --config is required")
	}
	if fs.NArg() > 0 {
		return config.Config{}, mcp.Sources{}, fmt.Errorf("epitrop-server: unexpected argument %q", fs.Arg(0))
	}
	ctx, cancel := context.WithTimeout(context.Background(), configLoadTimeout)
	defer cancel()
	cfg, err := config.Load(ctx, *configPath)
	if err != nil {
		return config.Config{}, mcp.Sources{}, err
	}
	unitsSvc, err := logsetup.UnitsService(cfg)
	if err != nil {
		return config.Config{}, mcp.Sources{}, err
	}
	jobs, err := jobLister(cfg)
	if err != nil {
		return config.Config{}, mcp.Sources{}, err
	}
	return cfg, mcp.Sources{Jobs: jobs, Units: unitsSvc}, nil
}

// jobLister opens the supervisor for status reads only: no tool starts a job, so the shim
// command it defaults to is never run. Where the platform has no supervisor, the jobs tool
// answers with the reason instead of the server refusing to start; any other error does.
func jobLister(cfg config.Config) (mcp.JobLister, error) {
	sup, err := newSupervisor(cfg, supervisor.Options{})
	if errors.Is(err, supervisor.ErrNotSupported) {
		return unavailableJobs{err: err}, nil
	}
	if err != nil {
		return nil, err
	}
	return sup, nil
}

// unavailableJobs answers every status request with why jobs cannot be read.
type unavailableJobs struct{ err error }

func (u unavailableJobs) Status(context.Context, string) ([]supervisor.JobStatus, error) {
	return nil, u.err
}

func appVersion() string {
	if version != "" {
		return version
	}
	return "dev"
}
