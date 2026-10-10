//go:build linux

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cordanaLLM/tribunus/internal/config"
	"github.com/cordanaLLM/tribunus/internal/eventlog"
)

// fakeScopeEnv makes the test binary stand in for systemd-run. See TestMain. With
// fakeStragglerEnv set to a file, the stand-in also leaves a process behind in the job's
// process group and writes its pid there.
const (
	fakeScopeEnv     = "TRIBUNUS_TEST_FAKE_SCOPE"
	fakeStragglerEnv = "TRIBUNUS_TEST_FAKE_SCOPE_STRAGGLER"
)

// TestMain lets the test binary be the scope command of the negative scope tests. Started
// with systemd-run's arguments and fakeScopeEnv set, it does what a systemd-run that reports
// success does, waits a moment and execs the command in place, but creates no scope.
func TestMain(m *testing.M) {
	if os.Getenv(fakeScopeEnv) == "1" && len(os.Args) > 1 && os.Args[1] == "--user" {
		os.Exit(runFakeScopeCommand(os.Args))
	}
	os.Exit(m.Run())
}

func runFakeScopeCommand(args []string) int {
	// The first "--" ends systemd-run's own arguments; pasta and bwrap carry later ones.
	sep := 0
	for sep < len(args) && args[sep] != "--" {
		sep++
	}
	if sep+1 >= len(args) {
		fmt.Fprintln(os.Stderr, "fake scope command: no command after --")
		return 2
	}
	time.Sleep(100 * time.Millisecond)
	if err := leaveFakeStraggler(os.Getenv(fakeStragglerEnv)); err != nil {
		fmt.Fprintf(os.Stderr, "fake scope command: straggler: %v\n", err)
		return 2
	}
	command := args[sep+1:]
	// #nosec G204 -- test stand-in for systemd-run; the argv comes from the sandbox builder.
	err := syscall.Exec(command[0], command, os.Environ())
	fmt.Fprintf(os.Stderr, "fake scope command: exec %s: %v\n", command[0], err)
	return 127
}

// leaveFakeStraggler starts a process that is in the job's process group but neither under
// bwrap nor under a parent-death signal: only a kill of the whole group ends it.
func leaveFakeStraggler(pidFile string) error {
	if pidFile == "" {
		return nil
	}
	// No deadline: the stand-in execs the job right after, and the straggler must outlive it.
	straggler := exec.CommandContext(context.Background(), "/usr/bin/sleep", "30") // #nosec G204 -- fixed test stand-in.
	if err := straggler.Start(); err != nil {
		return err
	}
	return os.WriteFile(pidFile, []byte(strconv.Itoa(straggler.Process.Pid)), 0o600)
}

// plantFakeScopeCommand puts the test binary first on PATH under the name systemd-run.
func plantFakeScopeCommand(t *testing.T) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable() = %v, want nil", err)
	}
	dir := t.TempDir()
	if err = os.Symlink(self, filepath.Join(dir, "systemd-run")); err != nil {
		t.Fatalf("Symlink(systemd-run) = %v, want nil", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(fakeScopeEnv, "1")
}

func TestNewScopeNameIsOneUnitPerStart(t *testing.T) {
	valid := regexp.MustCompile(`^tribunus-job-[0-9a-f]{16}\.scope$`)
	first, err := newScopeName()
	if err != nil || !valid.MatchString(first) {
		t.Fatalf("newScopeName() = %q, %v, want %s", first, err, valid)
	}
	second, err := newScopeName()
	if err != nil || second == first {
		t.Fatalf("newScopeName() twice = %q, %q, %v, want two names", first, second, err)
	}
}

func TestCgroupInScope(t *testing.T) {
	const scope = "tribunus-job-0123456789abcdef.scope"
	const app = "/user.slice/user-1000.slice/user@1000.service/app.slice/"
	cases := []struct {
		name   string
		cgroup string
		want   bool
	}{
		{"in the scope", app + scope, true},
		{"exited, scope removed", app + scope + " (deleted)", true},
		{"the service the job was started from", "/system.slice/hosted-compute-agent.service", false},
		{"another job's scope", app + "tribunus-job-fedcba9876543210.scope", false},
		{"a scope the shim itself runs in", app + "session-3.scope", false},
		{"a transient scope with another name", app + "run-p12-i34.scope", false},
		{"below the scope", app + scope + "/sub", false},
		{"scope name as a prefix", app + scope + ".d", false},
		{"root", "/", false},
		{"empty", "", false},
	}
	for i := 0; i < len(cases); i++ {
		if got := cgroupInScope(cases[i].cgroup, scope); got != cases[i].want {
			t.Errorf("cgroupInScope(%s: %q) = %v, want %v", cases[i].name, cases[i].cgroup, got, cases[i].want)
		}
	}
}

// scopeWorld scripts what the shim reads about one process, poll by poll. The last entry of
// each list repeats.
type scopeWorld struct {
	cgroups []string
	states  []string
	images  []string
	reads   int
	sleeps  int
}

func pick(values []string, i int) string {
	if i >= len(values) {
		i = len(values) - 1
	}
	return values[i]
}

func (w *scopeWorld) install(t *testing.T) {
	t.Helper()
	oldCgroup, oldStatus, oldExe, oldSleep := procCgroupReadFile, procStatusReadFile, procExeReadlink, scopeConfirmSleep
	t.Cleanup(func() {
		procCgroupReadFile, procStatusReadFile, procExeReadlink, scopeConfirmSleep = oldCgroup, oldStatus, oldExe, oldSleep
	})
	procStatusReadFile = func(string) ([]byte, error) {
		state := pick(w.states, w.sleeps)
		if state == "missing" {
			return nil, fs.ErrNotExist
		}
		return []byte("Name:\tjob\nState:\t" + state + "\nPPid:\t7\n"), nil
	}
	procExeReadlink = func(string) (string, error) {
		image := pick(w.images, w.sleeps)
		if image == "" {
			return "", fs.ErrNotExist
		}
		return image, nil
	}
	procCgroupReadFile = func(string) ([]byte, error) {
		w.reads++
		return []byte("0::" + pick(w.cgroups, w.sleeps) + "\n"), nil
	}
	scopeConfirmSleep = func(time.Duration) { w.sleeps++ }
}

func TestConfirmJobScope(t *testing.T) {
	const scope = "tribunus-job-0123456789abcdef.scope"
	const runner = "/usr/bin/systemd-run"
	const outside = "/system.slice/hosted-compute-agent.service"
	const inside = "/user.slice/user-1001.slice/user@1001.service/app.slice/" + scope
	cases := []struct {
		name      string
		world     scopeWorld
		wantErr   string
		wantReads int
	}{
		{"in the scope at once", scopeWorld{cgroups: []string{inside}, states: []string{"S (sleeping)"}, images: []string{"/usr/bin/env"}}, "", 1},
		{"enters the scope while still systemd-run", scopeWorld{cgroups: []string{outside, outside, inside}, states: []string{"R (running)"}, images: []string{runner}}, "", 3},
		{"enters the scope on the last poll", scopeWorld{cgroups: append(repeat(outside, scopeConfirmPolls-1), inside), states: []string{"S (sleeping)"}, images: []string{runner}}, "", scopeConfirmPolls},
		{"exited inside a scope already removed", scopeWorld{cgroups: []string{inside + " (deleted)"}, states: []string{"Z (zombie)"}, images: []string{""}}, "", 1},
		{"never leaves the cgroup it was started in", scopeWorld{cgroups: []string{outside}, states: []string{"S (sleeping)"}, images: []string{runner}}, "is still in cgroup " + outside + ", not in scope " + scope, scopeConfirmPolls},
		{"runs the job outside the scope", scopeWorld{cgroups: []string{outside}, states: []string{"S (sleeping)"}, images: []string{runner, runner, "/usr/bin/env"}}, "runs as /usr/bin/env in cgroup " + outside + ", outside scope " + scope, 3},
		{"exited outside the scope", scopeWorld{cgroups: []string{outside}, states: []string{"S (sleeping)", "Z (zombie)"}, images: []string{runner, ""}}, "exited in cgroup " + outside + " before it entered scope " + scope, 2},
		{"already reaped", scopeWorld{cgroups: []string{outside}, states: []string{"missing"}, images: []string{""}}, "exited in cgroup " + outside, 1},
		{"program unreadable outside the scope", scopeWorld{cgroups: []string{outside}, states: []string{"S (sleeping)"}, images: []string{""}}, "is still in cgroup " + outside, scopeConfirmPolls},
	}
	for i := 0; i < len(cases); i++ {
		tc := cases[i]
		t.Run(tc.name, func(t *testing.T) {
			tc.world.install(t)
			err := confirmJobScope(4242, scope, runner)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("confirmJobScope() = %v, want nil", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("confirmJobScope() = %v, want refusal holding %q", err, tc.wantErr)
			}
			if tc.world.reads != tc.wantReads {
				t.Fatalf("confirmJobScope() read the cgroup %d times, want %d", tc.world.reads, tc.wantReads)
			}
		})
	}
}

func repeat(value string, n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = value
	}
	return out
}

func TestConfirmJobScopeWithoutAScopeReadsNothing(t *testing.T) {
	world := scopeWorld{cgroups: []string{"/"}, states: []string{"S (sleeping)"}, images: []string{"/usr/bin/true"}}
	world.install(t)
	if err := confirmJobScope(4242, "", ""); err != nil || world.reads != 0 {
		t.Fatalf("confirmJobScope(no scope) = %v after %d reads, want nil and 0", err, world.reads)
	}
}

func TestConfirmJobScopeRefusesUnreadableState(t *testing.T) {
	world := scopeWorld{cgroups: []string{"/"}, states: []string{"S (sleeping)"}, images: []string{"/usr/bin/systemd-run"}}
	world.install(t)
	procCgroupReadFile = func(string) ([]byte, error) { return nil, fs.ErrPermission }
	if err := confirmJobScope(4242, "x.scope", "/usr/bin/systemd-run"); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("confirmJobScope(cgroup unreadable) = %v, want the read error", err)
	}
	procCgroupReadFile = func(string) ([]byte, error) { return []byte("1:name=systemd:/x.scope\n"), nil }
	if err := confirmJobScope(4242, "x.scope", "/usr/bin/systemd-run"); err == nil || !strings.Contains(err.Error(), "no cgroup v2 entry") {
		t.Fatalf("confirmJobScope(cgroup v1 only) = %v, want refusal", err)
	}
	procStatusReadFile = func(string) ([]byte, error) { return nil, fs.ErrPermission }
	if err := confirmJobScope(4242, "x.scope", "/usr/bin/systemd-run"); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("confirmJobScope(status unreadable) = %v, want the read error", err)
	}
}

// TestProcessGoneCountsAZombie uses a real zombie: a child that has exited and is not yet
// collected still answers signal 0, which is why the tests no longer ask that way.
func TestProcessGoneCountsAZombie(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "/usr/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start(sleep) = %v, want nil", err)
	}
	pid := cmd.Process.Pid
	if gone, err := processGone(pid); err != nil || gone {
		t.Fatalf("processGone(running child) = %v, %v, want false", gone, err)
	}
	if !processExists(pid) {
		t.Fatalf("processExists(running child) = false, want true")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("Kill(child) = %v, want nil", err)
	}
	zombie := false
	for i := 0; i < 200 && !zombie; i++ {
		body, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
		zombie = err == nil && strings.HasPrefix(procStatusField(string(body), "State:"), "Z")
		time.Sleep(5 * time.Millisecond)
	}
	if !zombie {
		t.Fatalf("child %d never became a zombie", pid)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("signal 0 to zombie %d = %v, want nil: the zombie must still be in the process table here", pid, err)
	}
	if gone, err := processGone(pid); err != nil || !gone {
		t.Fatalf("processGone(zombie) = %v, %v, want true", gone, err)
	}
	if processExists(pid) {
		t.Fatalf("processExists(zombie) = true, want false")
	}
	if err := cmd.Wait(); err == nil {
		t.Fatalf("Wait(killed child) = nil, want its kill status")
	}
	if gone, err := processGone(pid); err != nil || !gone {
		t.Fatalf("processGone(collected child) = %v, %v, want true", gone, err)
	}
}

func TestProcessGoneReportsUnreadableState(t *testing.T) {
	old := procStatusReadFile
	t.Cleanup(func() { procStatusReadFile = old })
	procStatusReadFile = func(string) ([]byte, error) { return nil, fs.ErrPermission }
	if gone, err := processGone(4242); gone || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("processGone(unreadable) = %v, %v, want false and the read error", gone, err)
	}
	if shimExited(4242) {
		t.Fatalf("shimExited(unreadable) = true, want a shim of unknown state taken as alive")
	}
	procStatusReadFile = func(string) ([]byte, error) { return nil, syscall.ESRCH }
	if gone, err := processGone(4242); !gone || err != nil {
		t.Fatalf("processGone(ESRCH) = %v, %v, want true", gone, err)
	}
	procStatusReadFile = func(string) ([]byte, error) { return []byte("Name:\tx\nState:\tX (dead)\n"), nil }
	if gone, err := processGone(4242); !gone || err != nil {
		t.Fatalf("processGone(dead) = %v, %v, want true", gone, err)
	}
}

// TestScopeProbeRefusesAScopeCommandThatCreatesNoScope is the negative case of the probe the
// sandbox integration tests depend on: a scope command that exits 0 without a scope must
// skip them, not let them run against a job outside any scope.
func TestScopeProbeRefusesAScopeCommandThatCreatesNoScope(t *testing.T) {
	plantFakeScopeCommand(t)
	passed := false
	t.Run("probe", func(t *testing.T) {
		requireSystemdUserScope(t)
		passed = true
	})
	if passed {
		t.Fatalf("requireSystemdUserScope passed with a scope command that creates no scope, want skip")
	}
}

// TestSandboxedJobOutsideItsScopeIsRefused is the negative case of the scope check: the
// scope command reports success and starts the job, but the job stays in the cgroup it was
// started from. The shim must kill it, record no start and say why in the job log.
func TestSandboxedJobOutsideItsScopeIsRefused(t *testing.T) {
	plantFakeScopeCommand(t)
	job := sandboxedSupervisorJob(t, "sandbox-noscope", `while true; do printf . >> "$PWD/beat"; sleep 0.05; done`)
	stragglerFile := filepath.Join(t.TempDir(), "straggler.pid")
	t.Setenv(fakeStragglerEnv, stragglerFile)
	job.Sandbox.EnvAllow = []string{fakeScopeEnv, fakeStragglerEnv}
	requireSandboxToolchain(t, job.Sandbox)
	beat := filepath.Join(job.Sandbox.Workspace, "beat")
	sup := testSupervisor(t, job)
	err := sup.Start(testContext(t), job.Name)
	if err == nil || !strings.Contains(err.Error(), "did not start: its shim exited") {
		t.Fatalf("Start(job outside its scope) = %v, want did-not-start naming the exited shim", err)
	}
	log, err := os.ReadFile(job.LogPath)
	if err != nil {
		t.Fatalf("ReadFile(job log) = %v, want nil", err)
	}
	if !strings.Contains(string(log), "tribunus: job-shim: sandbox-noscope refused: sandbox: job ") || !strings.Contains(string(log), "scope "+scopeNamePrefix) {
		t.Fatalf("job log = %q, want the refusal naming the scope", log)
	}
	t.Logf("refusal in the job log: %s", strings.TrimSpace(string(log)))
	state, err := sup.replayState(testContext(t))
	if err != nil {
		t.Fatalf("replayState() = %v, want nil", err)
	}
	if recorded := state.Jobs[job.Name]; recorded.Since != "" || recorded.LastEvent != "" {
		t.Fatalf("event log holds %+v for the refused job, want no record of a start", recorded)
	}
	rec, ok := readJobRecord(sup.recordPath(job.Name))
	if !ok {
		t.Fatalf("job record %s missing, want the record of the killed job", sup.recordPath(job.Name))
	}
	waitForProcessGone(t, rec.PID)
	// The whole process group dies, not only the process the parent-death signal reaches.
	straggler, err := os.ReadFile(stragglerFile)
	if err != nil {
		t.Fatalf("ReadFile(straggler pid) = %v, want the pid the scope command left behind", err)
	}
	stragglerPID, err := strconv.Atoi(string(straggler))
	if err != nil {
		t.Fatalf("straggler pid %q = %v, want a pid", straggler, err)
	}
	waitForProcessGone(t, stragglerPID)
	waitForUnlock(t, sup, job)
	first := fileSize(beat)
	time.Sleep(300 * time.Millisecond)
	if fileSize(beat) != first {
		t.Fatalf("heartbeat %s still growing after the refusal: the job outlived it", beat)
	}
}

func startedPayload(shimPID int) map[string]any {
	return map[string]any{"state": string(StateRunning), "pid": 4242, "shim_pid": shimPID, "since": "2026-10-10T00:00:00Z", "sandbox": "enforce/none"}
}

// TestWaitStartedCountsALateStartByALiveShim: a shim that is alive and records its start
// late, as one waiting for systemd-run to place the job does, still started the job.
func TestWaitStartedCountsALateStartByALiveShim(t *testing.T) {
	sup := testSupervisor(t, testJob("slow", []string{"/bin/true"}))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	shim := os.Getpid()
	appended := make(chan error, 1)
	go func() {
		time.Sleep(2 * time.Second)
		appended <- sup.appendJobEvent(ctx, "job.started", "slow", startedPayload(shim))
	}()
	if err := sup.waitStarted(ctx, "slow", eventlog.Job{}, shim); err != nil {
		t.Fatalf("waitStarted(start recorded after 2 s by a live shim) = %v, want nil", err)
	}
	if err := <-appended; err != nil {
		t.Fatalf("appendJobEvent(job.started) = %v, want nil", err)
	}
}

// TestWaitStartedEndsWhenTheShimHasExited: an exited shim records nothing more, so the wait
// ends with it and says so, long before the poll bound.
func TestWaitStartedEndsWhenTheShimHasExited(t *testing.T) {
	sup := testSupervisor(t, testJob("probe", []string{"/bin/true"}))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	shim := exec.CommandContext(ctx, "/usr/bin/true")
	if err := shim.Start(); err != nil {
		t.Fatalf("Start(true) = %v, want nil", err)
	}
	began := time.Now()
	err := sup.waitStarted(ctx, "probe", eventlog.Job{}, shim.Process.Pid)
	if err == nil || !strings.Contains(err.Error(), "probe did not start: its shim exited without recording a start") {
		t.Fatalf("waitStarted(exited shim) = %v, want did-not-start naming the exited shim", err)
	}
	if took := time.Since(began); took > 3*time.Second {
		t.Fatalf("waitStarted(exited shim) took %s, want it to end with the shim", took)
	}
	if err = shim.Wait(); err != nil {
		t.Fatalf("Wait(true) = %v, want nil", err)
	}
}

// TestWaitStartedKeepsTheReplayErrorWhenTheContextEnds: while the shim is alive the wait goes
// on, whatever the replay returns. When the caller's context ends it, the error names both
// the deadline and the replay that was still failing.
func TestWaitStartedKeepsTheReplayErrorWhenTheContextEnds(t *testing.T) {
	sup := testSupervisor(t, testJob("probe", []string{"/bin/true"}))
	if err := sup.appendJobEvent(testContext(t), "job.stopped", "probe", map[string]any{"state": "stopped"}); err != nil {
		t.Fatalf("append seed = %v, want nil", err)
	}
	corruptEventLog(t, sup.cfg.EventLog.Dir)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	err := sup.waitStarted(ctx, "probe", eventlog.Job{}, os.Getpid())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitStarted(corrupt log, live shim) = %v, want the deadline: a live shim is waited for", err)
	}
	if !strings.Contains(err.Error(), "probe did not start: ") || !strings.Contains(err.Error(), "last replay: ") || !strings.Contains(err.Error(), "eventlog: seq 1 file ") {
		t.Fatalf("waitStarted(corrupt log, live shim) = %v, want did-not-start with the last replay error", err)
	}
	var both interface{ Unwrap() []error }
	if !errors.As(err, &both) || len(both.Unwrap()) != 2 {
		t.Fatalf("waitStarted(corrupt log, live shim) = %v, want it to wrap the deadline and the replay error", err)
	}
}

// TestWaitStartedNamesOnlyTheContextWhenReplaysWork: with nothing wrong in the log, the
// error of an ended wait is the context's alone.
func TestWaitStartedNamesOnlyTheContextWhenReplaysWork(t *testing.T) {
	sup := testSupervisor(t, testJob("probe", []string{"/bin/true"}))
	// One record, so that every replay reads the log and checks its context.
	if err := sup.appendJobEvent(testContext(t), "job.stopped", "probe", map[string]any{"state": "stopped"}); err != nil {
		t.Fatalf("append seed = %v, want nil", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	err := sup.waitStarted(ctx, "probe", eventlog.Job{}, os.Getpid())
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "last replay") {
		t.Fatalf("waitStarted(clean log, live shim) = %v, want did-not-start with the deadline only", err)
	}
	if got, want := err.Error(), "supervisor: probe did not start: context deadline exceeded"; got != want {
		t.Fatalf("waitStarted(clean log, live shim) = %q, want %q", got, want)
	}
	// A replay that failed only because the context had ended adds nothing to say.
	err = sup.waitStarted(canceledContext(t), "probe", eventlog.Job{}, os.Getpid())
	if got, want := fmt.Sprint(err), "supervisor: probe did not start: context canceled"; got != want {
		t.Fatalf("waitStarted(canceled context, live shim) = %q, want %q", got, want)
	}
}

// TestWaitStartedReplaysOnceMoreAfterTheShimExited: a shim can record its start and exit
// between a replay and the check that finds it gone. That start must still count.
func TestWaitStartedReplaysOnceMoreAfterTheShimExited(t *testing.T) {
	sup := testSupervisor(t, testJob("fast", []string{"/bin/true"}))
	ctx := testContext(t)
	old := procStatusReadFile
	t.Cleanup(func() { procStatusReadFile = old })
	checks := 0
	procStatusReadFile = func(string) ([]byte, error) {
		checks++
		if err := sup.appendJobEvent(ctx, "job.started", "fast", startedPayload(4243)); err != nil {
			t.Errorf("appendJobEvent(job.started) = %v, want nil", err)
		}
		return nil, fs.ErrNotExist
	}
	if err := sup.waitStarted(ctx, "fast", eventlog.Job{}, 4243); err != nil || checks != 1 {
		t.Fatalf("waitStarted(start recorded as the shim exited) = %v after %d checks, want nil after 1", err, checks)
	}
}

// requireSandboxToolchain skips where the sandbox preflight itself would stop the shim, so
// a test of a later refusal cannot pass or fail for that reason.
func requireSandboxToolchain(t *testing.T, sandbox config.SandboxConfig) {
	t.Helper()
	if _, err := sandboxToolchain(sandbox, sandboxTools{}); err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("sandboxToolchain() = %v under CI, want the installed toolchain", err)
		}
		t.Skipf("sandbox test skipped: toolchain unavailable: %v", err)
	}
}
