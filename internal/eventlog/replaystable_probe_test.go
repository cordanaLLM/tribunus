package eventlog

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestProbeReplayStableRestoreLatency is a measurement, not a check: it repeats the
// TestReplayStableWaitsOutAnAppendInFlight scenario and logs how late the restoring
// goroutine wakes, how long the atomic HEAD write takes, when HEAD is back relative to
// the start of ReplayStable, and how many rounds failed. It never fails on the numbers.
func TestProbeReplayStableRestoreLatency(t *testing.T) {
	const rounds = 60
	var wake, write, visible, replay []time.Duration
	failed := 0
	for i := 0; i < rounds; i++ {
		w, wr, v, r, err := probeReplayStableRound(t)
		wake, write, visible, replay = append(wake, w), append(write, wr), append(visible, v), append(replay, r)
		if err != nil {
			failed++
			t.Logf("round %d: wake %v write %v visible %v replay %v err %v", i, w, wr, v, r, err)
		}
	}
	t.Logf("PROBE rounds=%d failed=%d", rounds, failed)
	t.Logf("PROBE sleep60ms-wake %s", probeSummary(wake))
	t.Logf("PROBE atomicReplace %s", probeSummary(write))
	t.Logf("PROBE head-visible-after-start %s", probeSummary(visible))
	t.Logf("PROBE replaystable %s", probeSummary(replay))
}

func probeReplayStableRound(t *testing.T) (wake, write, visible, replay time.Duration, err error) {
	t.Helper()
	dir, signer, limits := testLog(t)
	appendEvent(t, dir, signer, limits, Event{Type: "task.created", TaskID: "task-1", Payload: raw(`{"state":"open"}`)})
	head := filepath.Join(dir, "events", "HEAD.json")
	body, readErr := os.ReadFile(head)
	if readErr != nil {
		t.Fatalf("ReadFile(HEAD) = %v, want nil", readErr)
	}
	if rmErr := os.Remove(head); rmErr != nil {
		t.Fatalf("Remove(HEAD) = %v, want nil", rmErr)
	}
	type timing struct {
		wake, write time.Duration
		done        time.Time
		err         error
	}
	restored := make(chan timing, 1)
	start := time.Now()
	go func() {
		began := time.Now()
		time.Sleep(60 * time.Millisecond)
		woke := time.Now()
		werr := atomicReplace(filepath.Dir(head), filepath.Base(head), body, 0o600)
		done := time.Now()
		restored <- timing{wake: woke.Sub(began), write: done.Sub(woke), done: done, err: werr}
	}()
	_, err = ReplayStable(testContext(t), dir, signer.PublicKey(), limits, TaskReducer)
	replay = time.Since(start)
	got := <-restored
	if got.err != nil {
		t.Fatalf("restore HEAD = %v, want nil", got.err)
	}
	return got.wake, got.write, got.done.Sub(start), replay, err
}

func probeSummary(d []time.Duration) string {
	s := slices.Clone(d)
	slices.Sort(s)
	at := func(q int) time.Duration { return s[(len(s)-1)*q/100] }
	return "min=" + s[0].String() + " p50=" + at(50).String() + " p90=" + at(90).String() + " p99=" + at(99).String() + " max=" + s[len(s)-1].String()
}
