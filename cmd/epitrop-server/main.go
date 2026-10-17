// Command epitrop-server serves the run record over the Model Context Protocol on stdio.
// Every tool it serves only reads; see internal/mcp.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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

const configLoadTimeout = 30 * time.Second

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

// serve loads the config named by args and serves the tools on in and out until the
// client disconnects or ctx ends. Nothing but protocol frames is written to out.
func serve(ctx context.Context, args []string, in io.ReadCloser, out io.WriteCloser) error {
	srv, err := newServer(args)
	if err != nil {
		return err
	}
	err = srv.Run(ctx, &sdkmcp.IOTransport{Reader: in, Writer: out})
	if err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("epitrop-server: %w", err)
	}
	return nil
}

// newServer parses args, loads the config and opens what the tools read.
func newServer(args []string) (*sdkmcp.Server, error) {
	fs := flag.NewFlagSet("epitrop-server", flag.ContinueOnError)
	configPath := fs.String("config", "", "configuration file")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if *configPath == "" {
		return nil, errors.New("epitrop-server: --config is required")
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("epitrop-server: unexpected argument %q", fs.Arg(0))
	}
	ctx, cancel := context.WithTimeout(context.Background(), configLoadTimeout)
	defer cancel()
	cfg, err := config.Load(ctx, *configPath)
	if err != nil {
		return nil, err
	}
	unitsSvc, err := logsetup.UnitsService(cfg)
	if err != nil {
		return nil, err
	}
	jobs, err := jobLister(cfg)
	if err != nil {
		return nil, err
	}
	return mcp.NewServer(appVersion(), mcp.Sources{Jobs: jobs, Units: unitsSvc}), nil
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
