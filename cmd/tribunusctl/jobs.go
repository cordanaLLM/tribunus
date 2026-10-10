package main

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"time"

	"github.com/cordanaLLM/tribunus/internal/config"
	"github.com/cordanaLLM/tribunus/internal/supervisor"
)

func runJobShim(args []string) int {
	return supervisor.RunShim(args)
}

var newJobsSupervisor = supervisor.New

func runJobs(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("jobs command required: start, status, stop or supervise")
	}
	switch args[0] {
	case "start", "status", "stop", "supervise":
		return runJobsAction(args[0], args[1:])
	}
	return fmt.Errorf("unknown jobs command: %s", args[0])
}

func runJobsAction(action string, args []string) error {
	fs := flag.NewFlagSet("jobs "+action, flag.ContinueOnError)
	configPath := fs.String("config", "", "configuration file")
	pollSeconds := fs.Int("poll-interval-seconds", 1, "supervisor poll interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *configPath == "" {
		return fmt.Errorf("jobs %s: --config is required", action)
	}
	name, err := jobsNameArg(action, fs.Args())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := config.Load(ctx, *configPath)
	if err != nil {
		return err
	}
	sup, err := newJobsSupervisor(cfg, supervisor.Options{PollInterval: time.Duration(*pollSeconds) * time.Second})
	if err != nil {
		return err
	}
	return runLoadedJobsAction(ctx, sup, action, name)
}

func jobsNameArg(action string, args []string) (string, error) {
	if action == "supervise" && len(args) == 0 {
		return "", nil
	}
	if len(args) > 1 {
		return "", fmt.Errorf("jobs %s: expected at most one name", action)
	}
	if len(args) == 1 {
		return args[0], nil
	}
	return "", nil
}

func runLoadedJobsAction(ctx context.Context, sup *supervisor.Supervisor, action string, name string) error {
	switch action {
	case "start":
		return sup.Start(ctx, name)
	case "stop":
		return sup.Stop(ctx, name)
	case "status":
		return printJobsStatus(ctx, sup, name)
	case "supervise":
		return sup.Supervise(ctx)
	}
	return fmt.Errorf("unknown jobs command: %s", action)
}

func printJobsStatus(ctx context.Context, sup *supervisor.Supervisor, name string) error {
	statuses, err := sup.Status(ctx, name)
	if err != nil {
		return err
	}
	for i := 0; i < len(statuses); i++ {
		st := statuses[i]
		fmt.Println(st.Name + "\t" + string(st.State) + "\t" + strconv.Itoa(st.PID) + "\t" + st.Since + "\t" + strconv.Itoa(st.Restarts))
	}
	return nil
}
