package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"

	"github.com/tekmora/epitrop/internal/eventlog"
	"github.com/tekmora/epitrop/internal/graphlog"
)

// graphCallWith is graphCall for an action with positional arguments; need names them.
func graphCallWith(fs *flag.FlagSet, args []string, need ...string) (*graphlog.Service, []string, context.Context, context.CancelFunc, error) {
	cfg, pos, ctx, cancel, err := commandConfig(fs, args, need...)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	svc, err := newGraphService(cfg)
	if err != nil {
		cancel()
		return nil, nil, nil, nil, err
	}
	return svc, pos, ctx, cancel, nil
}

// runUnitsFrontier prints the nodes a slot may take next: units that are ready and whose
// every dependency has landed, one per line as id and quoted title. It writes nothing.
func runUnitsFrontier(args []string) error {
	fs := flag.NewFlagSet("units frontier", flag.ContinueOnError)
	svc, ctx, cancel, err := graphCall(fs, args)
	if err != nil {
		return err
	}
	defer cancel()
	nodes, err := svc.Frontier(ctx)
	if err != nil {
		return err
	}
	for i := 0; i < len(nodes); i++ {
		if _, err = fmt.Fprintf(graphOut, "%s\t%q\n", nodes[i].ID, nodes[i].Title); err != nil {
			return fmt.Errorf("units frontier: print: %w", err)
		}
	}
	return nil
}

// runUnitsAssign takes a frontier node and opens its unit in one record; starting the
// slot is "slot run".
func runUnitsAssign(args []string) error {
	fs := flag.NewFlagSet("units assign", flag.ContinueOnError)
	repo := fs.String("repo", "", "repository part of the unit id")
	lane := fs.String("lane", "", "lane the unit runs on")
	by := fs.String("by", "", "actor who assigns the unit")
	svc, pos, ctx, cancel, err := graphCallWith(fs, args, "node")
	if err != nil {
		return err
	}
	defer cancel()
	if *repo == "" || *lane == "" {
		return fmt.Errorf("units assign: --repo and --lane are required")
	}
	unit, err := svc.Assign(ctx, *repo, *lane, pos[0], *by)
	if err != nil {
		return err
	}
	return printUnit("units assign", unit)
}

// runUnitsRelease gives a claimed node back to ready and drops its unit, with a reason.
func runUnitsRelease(args []string) error {
	fs := flag.NewFlagSet("units release", flag.ContinueOnError)
	reason := fs.String("reason", "", "why the node goes back")
	by := fs.String("by", "", "actor who releases the node")
	svc, pos, ctx, cancel, err := graphCallWith(fs, args, "node")
	if err != nil {
		return err
	}
	defer cancel()
	if *reason == "" {
		return fmt.Errorf("units release: --reason is required")
	}
	unit, err := svc.Release(ctx, pos[0], *reason, *by)
	if err != nil {
		return err
	}
	return printUnit("units release", unit)
}

func printUnit(op string, unit eventlog.Unit) error {
	body, err := json.MarshalIndent(unit, "", "  ")
	if err != nil {
		return fmt.Errorf("%s: marshal: %w", op, err)
	}
	if _, err = fmt.Fprintln(graphOut, string(body)); err != nil {
		return fmt.Errorf("%s: print: %w", op, err)
	}
	return nil
}
