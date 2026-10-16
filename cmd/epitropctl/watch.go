package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tekmora/epitrop/internal/config"
	"github.com/tekmora/epitrop/internal/releasewatch"
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

type watchFlags struct {
	configPath string
	once       bool
	interval   time.Duration
	githubBase string
	hfBase     string
	ntfyBase   string
}

func validateBaseURL(action, flagName, raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("watch %s: --%s must be an absolute http(s) URL", action, flagName)
	}
	return nil
}

func parseWatchFlags(action string, args []string) (watchFlags, error) {
	fs := flag.NewFlagSet("watch "+action, flag.ContinueOnError)
	configPath := fs.String("config", "", "configuration file")
	once := fs.Bool("once", false, "run one pass then exit")
	interval := fs.Duration("interval", time.Hour, "interval between poll passes")
	githubBase := fs.String("github-base", "", "override GitHub API base URL (for tests and offline mirrors)")
	hfBase := fs.String("huggingface-base", "", "override Hugging Face API base URL (for tests and offline mirrors)")
	ntfyBase := fs.String("ntfy-base", "", "override ntfy base URL (for tests and offline mirrors)")
	if err := fs.Parse(args); err != nil {
		return watchFlags{}, flagErr{err: err}
	}
	if *configPath == "" {
		return watchFlags{}, fmt.Errorf("watch %s: --config is required", action)
	}
	if err := validateBaseURL(action, "github-base", *githubBase); err != nil {
		return watchFlags{}, err
	}
	if err := validateBaseURL(action, "huggingface-base", *hfBase); err != nil {
		return watchFlags{}, err
	}
	if err := validateBaseURL(action, "ntfy-base", *ntfyBase); err != nil {
		return watchFlags{}, err
	}
	return watchFlags{
		configPath: *configPath,
		once:       *once,
		interval:   *interval,
		githubBase: *githubBase,
		hfBase:     *hfBase,
		ntfyBase:   *ntfyBase,
	}, nil
}

func runWatchAction(action string, args []string) error {
	flags, err := parseWatchFlags(action, args)
	if err != nil {
		return err
	}

	loadCtx, cancelLoad := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelLoad()
	cfg, err := config.Load(loadCtx, flags.configPath)
	if err != nil {
		return err
	}
	opts := releasewatch.ClientOptions{
		GitHubBase:      flags.githubBase,
		HuggingFaceBase: flags.hfBase,
		NtfyBase:        flags.ntfyBase,
	}
	svc, err := newWatchService(cfg, opts)
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
		return svc.Run(runCtx, flags.once, flags.interval)
	case "seed":
		seedCtx, cancelSeed := context.WithTimeout(ctx, 10*time.Minute)
		defer cancelSeed()
		return svc.Seed(seedCtx)
	}
	return fmt.Errorf("unknown watch command: %s", action)
}
