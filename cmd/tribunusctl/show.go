package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/cordanaLLM/praetor/tribunus/catalog"
)

// maxSnapshotReadBytes bounds how large a snapshot file show will read
// (HISS-02).
const maxSnapshotReadBytes = 64 << 20

// runShow is tribunusctl's "show" subcommand: it reads a snapshot file
// written by sync and renders it as a table.
func runShow(args []string) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	in := fs.String("in", defaultSnapshotPath, "path to a JSON snapshot written by sync")
	if err := fs.Parse(args); err != nil {
		return err
	}

	snap, err := readSnapshotFile(*in)
	if err != nil {
		return err
	}
	return renderTable(os.Stdout, snap)
}

func readSnapshotFile(path string) (snap catalog.Snapshot, err error) {
	// #nosec G304 -- path is the --in CLI flag the operator supplies directly,
	// the same trust level as the repository's own config.go manifest-path convention.
	f, err := os.Open(path)
	if err != nil {
		return catalog.Snapshot{}, fmt.Errorf("open snapshot %s: %w", path, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()

	info, err := f.Stat()
	if err != nil {
		return catalog.Snapshot{}, fmt.Errorf("stat snapshot %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return catalog.Snapshot{}, fmt.Errorf("snapshot %s is not a regular file", path)
	}
	if info.Size() > maxSnapshotReadBytes {
		return catalog.Snapshot{}, fmt.Errorf("snapshot %s exceeds %d bytes", path, maxSnapshotReadBytes)
	}

	data, err := io.ReadAll(io.LimitReader(f, maxSnapshotReadBytes+1))
	if err != nil {
		return catalog.Snapshot{}, fmt.Errorf("read snapshot %s: %w", path, err)
	}
	if len(data) > maxSnapshotReadBytes {
		return catalog.Snapshot{}, fmt.Errorf("snapshot %s exceeds %d bytes", path, maxSnapshotReadBytes)
	}

	snap, err = catalog.ParseSnapshot(data)
	if err != nil {
		return catalog.Snapshot{}, fmt.Errorf("parse snapshot %s: %w", path, err)
	}
	return snap, nil
}

// renderTable prints one row per record (id, access path, context window,
// prices, provenance; absent values print as "-", never a fabricated number),
// then one line per source run. It reports the first write failure, so a
// truncated table is never mistaken for a complete one.
func renderTable(w io.Writer, snap catalog.Snapshot) error {
	if err := renderRecordTable(w, snap.Records); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "\n%d records, generated %s\n", len(snap.Records), snap.GeneratedAt.Format("2006-01-02T15:04:05Z07:00")); err != nil {
		return fmt.Errorf("write summary line: %w", err)
	}
	return renderSourceRuns(w, snap.SourceRuns)
}

// renderRecordTable writes the tab-aligned record rows and flushes them.
func renderRecordTable(w io.Writer, records []catalog.Record) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "MODEL\tACCESS\tCTX\tIN/M\tOUT/M\tSOURCE\tKIND"); err != nil {
		return fmt.Errorf("write table header: %w", err)
	}
	for _, r := range records {
		if _, err := fmt.Fprintln(tw, strings.Join([]string{
			r.ModelID,
			string(r.AccessPath),
			formatInt64Ptr(r.ContextWindow),
			formatFloatPtr(r.PriceInPerM),
			formatFloatPtr(r.PriceOutPerM),
			r.Provenance.Source,
			string(r.Provenance.Kind),
		}, "\t")); err != nil {
			return fmt.Errorf("write table row: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush table: %w", err)
	}
	return nil
}

// renderSourceRuns writes one status line per source run.
func renderSourceRuns(w io.Writer, runs []catalog.SourceRun) error {
	for _, run := range runs {
		line := fmt.Sprintf("  %-16s %-4s count=%d", run.Source, run.Status, run.Count)
		if run.Detail != "" {
			line += " " + run.Detail
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return fmt.Errorf("write source run line: %w", err)
		}
	}
	return nil
}

func formatInt64Ptr(v *int64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *v)
}

func formatFloatPtr(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%.4g", *v)
}
