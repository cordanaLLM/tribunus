package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cordanaLLM/tribunus/internal/config"
	"github.com/cordanaLLM/tribunus/internal/releasewatch"
)

type flagErr struct {
	err error
}

func (e flagErr) Error() string {
	return e.err.Error()
}

func (e flagErr) Unwrap() error {
	return e.err
}

func (e flagErr) ExitCode() int {
	return 2
}

// maxWatchRuntime bounds one watch process, matching the supervisor's job runtime bound.
const maxWatchRuntime = 365 * 24 * time.Hour

var newWatchService = func(cfg config.Config, opts releasewatch.ClientOptions) (*releasewatch.Service, error) {
	return releasewatch.NewService(cfg, opts)
}

func runWatch(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("watch command required: run or seed")
	}
	switch args[0] {
	case "run", "seed":
		return runWatchAction(args[0], args[1:])
	}
	return fmt.Errorf("unknown watch command: %s", args[0])
}

func runWatchAction(action string, args []string) error {
	fs := flag.NewFlagSet("watch "+action, flag.ContinueOnError)
	configPath := fs.String("config", "", "configuration file")
	once := fs.Bool("once", false, "run one pass then exit")
	interval := fs.Duration("interval", time.Hour, "interval between poll passes")
	if err := fs.Parse(args); err != nil {
		return flagErr{err: err}
	}
	if *configPath == "" {
		return fmt.Errorf("watch %s: --config is required", action)
	}

	loadCtx, cancelLoad := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelLoad()
	cfg, err := config.Load(loadCtx, *configPath)
	if err != nil {
		return err
	}
	svc, err := newWatchService(cfg, releasewatch.ClientOptions{})
	if err != nil {
		return err
	}
	// A supervisor stop sends SIGTERM: it ends the loop between passes or cancels the pass
	// in flight. Each pass carries its own deadline.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	switch action {
	case "run":
		runCtx, cancelRun := context.WithTimeout(ctx, maxWatchRuntime)
		defer cancelRun()
		return svc.Run(runCtx, *once, *interval)
	case "seed":
		seedCtx, cancelSeed := context.WithTimeout(ctx, 10*time.Minute)
		defer cancelSeed()
		return svc.Seed(seedCtx)
	}
	return fmt.Errorf("unknown watch command: %s", action)
}
