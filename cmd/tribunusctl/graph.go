package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/cordanaLLM/tribunus/internal/config"
	"github.com/cordanaLLM/tribunus/internal/eventlog"
	"github.com/cordanaLLM/tribunus/internal/graph"
	"github.com/cordanaLLM/tribunus/internal/graphlog"
	"github.com/jonboulle/clockwork"
)

var newGraphService = func(cfg config.Config) (*graphlog.Service, error) {
	clk := clockwork.NewRealClock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	signer, err := eventlog.NewSignerFromKeyFile(ctx, cfg.EventLog.SigningKeyPath, cfg.EventLog.Dir, clk)
	if err != nil {
		return nil, err
	}
	return graphlog.NewService(cfg.EventLog.Dir, signer, clk, eventLogLimits(cfg, clk), cfg.Graph.MaxDepth)
}

// graphStdin is the reader "--file=-" takes the stage from; graphOut and graphErr are where
// the commands write.
var (
	graphStdin io.Reader = os.Stdin
	graphOut   io.Writer = os.Stdout
	graphErr   io.Writer = os.Stderr
)

func runGraph(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("graph command required: show or apply")
	}
	switch args[0] {
	case "show":
		return runGraphShow(args[1:])
	case "apply":
		return runGraphApply(args[1:])
	}
	return fmt.Errorf("unknown graph command: %s", args[0])
}

// graphCall parses the flags of one action, loads the config and opens the service.
func graphCall(fs *flag.FlagSet, args []string) (*graphlog.Service, context.Context, context.CancelFunc, error) {
	cfg, _, ctx, cancel, err := commandConfig(fs, args)
	if err != nil {
		return nil, nil, nil, err
	}
	svc, err := newGraphService(cfg)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	return svc, ctx, cancel, nil
}

// runGraphShow prints the task graph the event log holds. A stage the replay ignored is not
// part of it; the count goes to stderr, so that the output stays the graph alone.
func runGraphShow(args []string) error {
	fs := flag.NewFlagSet("graph show", flag.ContinueOnError)
	svc, ctx, cancel, err := graphCall(fs, args)
	if err != nil {
		return err
	}
	defer cancel()
	snap, ignored, err := svc.Show(ctx)
	if err != nil {
		return err
	}
	if ignored > 0 {
		if _, err = fmt.Fprintf(graphErr, "graph: the log holds %d stage(s) the graph refused; they are not part of this graph\n", ignored); err != nil {
			return fmt.Errorf("graph show: write: %w", err)
		}
	}
	return printSnapshot(snap)
}

// runGraphApply commits one stage, read from a file as {"ops": [...]}, and prints the graph
// after it. A stage the graph refuses changes nothing.
func runGraphApply(args []string) error {
	fs := flag.NewFlagSet("graph apply", flag.ContinueOnError)
	file := fs.String("file", "", "file holding the stage as {\"ops\": [...]}, or - for stdin")
	svc, ctx, cancel, err := graphCall(fs, args)
	if err != nil {
		return err
	}
	defer cancel()
	if *file == "" {
		return fmt.Errorf("graph apply: --file is required")
	}
	ops, err := readStage(*file)
	if err != nil {
		return err
	}
	snap, err := svc.Commit(ctx, ops)
	if err != nil {
		return err
	}
	return printSnapshot(snap)
}

// readStage reads the operations of a stage from path, or from stdin for "-". A document
// larger than a record, with a field the stage does not have, or with text after it is
// refused here, before anything is replayed.
func readStage(path string) ([]graph.Event, error) {
	body, err := stageBytes(path)
	if err != nil {
		return nil, fmt.Errorf("graph apply: read stage: %w", err)
	}
	if len(body) > eventlog.MaxRecordBytes {
		return nil, fmt.Errorf("graph apply: the stage is larger than %d bytes, the size of a record", eventlog.MaxRecordBytes)
	}
	var payload graphlog.StagePayload
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&payload); err != nil {
		return nil, fmt.Errorf("graph apply: stage: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("graph apply: stage: text after the stage document")
	}
	return payload.Ops, nil
}

// stageBytes reads at most one byte more than a record may hold, from path or from stdin.
func stageBytes(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(io.LimitReader(graphStdin, eventlog.MaxRecordBytes+1))
	}
	// #nosec G304 -- the operator names the stage file on the command line.
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(f, eventlog.MaxRecordBytes+1))
	if closeErr := f.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	return body, err
}

func printSnapshot(snap graph.Snapshot) error {
	body, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("graph: marshal: %w", err)
	}
	if _, err = fmt.Fprintf(graphOut, "%s\n", body); err != nil {
		return fmt.Errorf("graph: write: %w", err)
	}
	return nil
}
