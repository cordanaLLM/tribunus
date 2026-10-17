package supervisor

import (
	"math"
	"testing"

	"github.com/tekmora/epitrop/internal/config"
	"github.com/tekmora/epitrop/internal/eventlog"
)

func TestFoldFloorIsTheLowestCursorOfTheArmedEventJobs(t *testing.T) {
	jobs := []config.JobConfig{
		{Name: "on-merge", Schedule: "event"},
		{Name: "on-tag", Schedule: "event"},
		{Name: "unarmed", Schedule: "event"},
		{Name: "nightly", Schedule: "0 3 * * *"},
	}
	st := eventlog.State{Jobs: map[string]eventlog.Job{
		// The cursor is the later of the arming and the last trigger.
		"on-merge": {ArmedTrigger: "t", ArmedSeq: 40, LastTriggerSeq: 70},
		"on-tag":   {ArmedTrigger: "t", ArmedSeq: 55, LastTriggerSeq: 0},
		"unarmed":  {ArmedSeq: 3},
		"nightly":  {ArmedTrigger: "t", ArmedSeq: 2},
		// A job no longer configured keeps nothing back.
		"removed": {ArmedTrigger: "t", ArmedSeq: 1},
	}}
	f := Fold(jobs)
	if f.Name != FoldName || f.Reduce == nil || f.Floor == nil {
		t.Fatalf("Fold() = %+v, want name %q with a reducer and a floor", f, FoldName)
	}
	if got := f.Floor(st); got != 55 {
		t.Fatalf("Floor() = %d, want 55", got)
	}
}

func TestFoldFloorWithoutArmedEventJobsKeepsNothing(t *testing.T) {
	jobs := []config.JobConfig{{Name: "nightly", Schedule: "0 3 * * *"}, {Name: "on-merge", Schedule: "event"}}
	st := eventlog.State{Jobs: map[string]eventlog.Job{"nightly": {ArmedTrigger: "t", ArmedSeq: 2}}}
	if got := Fold(jobs).Floor(st); got != math.MaxUint64 {
		t.Fatalf("Floor() = %d, want no limit", got)
	}
	if got := Fold(nil).Floor(eventlog.State{}); got != math.MaxUint64 {
		t.Fatalf("Floor(no jobs) = %d, want no limit", got)
	}
}

func TestFoldFloorOfAJobArmedAtZero(t *testing.T) {
	jobs := []config.JobConfig{{Name: "on-merge", Schedule: "event"}}
	st := eventlog.State{Jobs: map[string]eventlog.Job{"on-merge": {ArmedTrigger: "t"}}}
	if got := Fold(jobs).Floor(st); got != 0 {
		t.Fatalf("Floor() = %d, want 0", got)
	}
}
