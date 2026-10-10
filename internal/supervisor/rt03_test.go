//go:build linux

package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cordanaLLM/tribunus/internal/config"
)

type rt03Fixture struct {
	ID        string          `json:"id"`
	Boundary  string          `json:"boundary"`
	Direction string          `json:"direction"`
	Task      rt03Task        `json:"task"`
	Attempt   rt03Attempt     `json:"attempt"`
	Expect    json.RawMessage `json:"expect"`
}

type rt03Task struct {
	Network  string   `json:"network"`
	EnvAllow []string `json:"env_allow"`
}

type rt03Attempt struct {
	Kind string `json:"kind"`
}

type rt03World struct {
	workspace  string
	eventLog   string
	configFile string
	signingKey string
	hostHome   string
}

func TestRT03Fixtures(t *testing.T) {
	fixtures := loadRT03Fixtures(t)
	for i := 0; i < len(fixtures); i++ {
		fixture := fixtures[i]
		t.Run(fixture.ID, func(t *testing.T) {
			allowed := runRT03Fixture(t, fixture)
			if fixture.Direction == "violation" && allowed {
				t.Fatalf("%s allowed violation, want refused", fixture.ID)
			}
			if fixture.Direction == "compliant" && !allowed {
				t.Fatalf("%s refused compliant action, want allowed", fixture.ID)
			}
		})
	}
}

func TestSandboxProductionArgvKeepsSystemdPrefix(t *testing.T) {
	workspace := t.TempDir()
	sandbox := config.SandboxConfig{Mode: "enforce", Workspace: workspace, Network: "none", MemoryMax: "2G", CPUWeight: 100, TasksMax: 512}
	built, err := buildSandboxCommand([]string{"/usr/bin/bash", "-c", "true"}, sandbox, sandboxBuildOptions{
		Tools: sandboxTools{SystemdRun: "/usr/bin/systemd-run", Bwrap: "/usr/bin/bwrap", Env: "/usr/bin/env", Systemd: true},
	})
	if err != nil {
		t.Fatalf("buildSandboxCommand() = %v, want nil", err)
	}
	args := built.Argv
	want := []string{"/usr/bin/systemd-run", "--user", "--scope", "--quiet", "--collect"}
	for i := 0; i < len(want); i++ {
		if args[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q in %q", i, args[i], want[i], args)
		}
	}
}

func loadRT03Fixtures(t *testing.T) []rt03Fixture {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "rt03", "*.json"))
	if err != nil {
		t.Fatalf("Glob(rt03) = %v, want nil", err)
	}
	out := make([]rt03Fixture, 0, len(paths))
	for i := 0; i < len(paths); i++ {
		out = append(out, readRT03Fixture(t, paths[i]))
	}
	return out
}

func readRT03Fixture(t *testing.T, path string) rt03Fixture {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) = %v, want nil", path, err)
	}
	var fixture rt03Fixture
	if err = json.Unmarshal(body, &fixture); err != nil {
		t.Fatalf("Unmarshal(%s) = %v, want nil", path, err)
	}
	return fixture
}

func runRT03Fixture(t *testing.T, fixture rt03Fixture) bool {
	t.Helper()
	world := newRT03World(t)
	env, cleanup := rt03Env(t, fixture, world)
	defer cleanup()
	sandbox := rt03Sandbox(fixture, world)
	tools := rt03Tools(t, sandbox.Network)
	built := rt03Command(t, fixture, sandbox, tools, env)
	ok := runRT03Command(t, built)
	return rt03Allowed(t, fixture, world, ok)
}

func newRT03World(t *testing.T) rt03World {
	t.Helper()
	root := t.TempDir()
	world := rt03World{
		workspace:  filepath.Join(root, "workspace"),
		eventLog:   filepath.Join(root, "control", "events"),
		configFile: filepath.Join(root, "control", "config.yaml"),
		signingKey: filepath.Join(root, "control", "seed.hex"),
		hostHome:   filepath.Join(root, "home"),
	}
	for _, dir := range []string{world.workspace, filepath.Dir(world.eventLog), world.hostHome} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) = %v, want nil", dir, err)
		}
	}
	writeRT03File(t, world.configFile, "original\n")
	writeRT03File(t, world.signingKey, "secret\n")
	return world
}

func rt03Sandbox(fixture rt03Fixture, world rt03World) config.SandboxConfig {
	return config.SandboxConfig{
		Mode:      "enforce",
		Workspace: world.workspace,
		EnvAllow:  append([]string(nil), fixture.Task.EnvAllow...),
		Network:   fixture.Task.Network,
		MemoryMax: "256M",
		CPUWeight: 100,
		TasksMax:  128,
	}
}

func rt03Env(t *testing.T, fixture rt03Fixture, world rt03World) (map[string]string, func()) {
	t.Helper()
	env := map[string]string{
		"WORKSPACE":      world.workspace,
		"EVENT_LOG_DIR":  world.eventLog,
		"CONFIG_PATH":    world.configFile,
		"HOST_HOME":      world.hostHome,
		"SIGNING_KEY":    world.signingKey,
		"SECRET_TOKEN":   "must-not-leak",
		"VISIBLE_TOKEN":  "allowed",
		"SHIM_PID":       strconv.Itoa(os.Getpid()),
		"SUPERVISOR_PID": strconv.Itoa(os.Getpid()),
	}
	cleanup := func() {}
	if strings.HasPrefix(fixture.Attempt.Kind, "tcp_connect") {
		host, port, stop := rt03Listener(t, fixture.Attempt.Kind)
		env["TARGET_HOST"], env["TARGET_PORT"] = host, port
		cleanup = stop
	}
	return env, cleanup
}

func rt03Tools(t *testing.T, network string) sandboxTools {
	t.Helper()
	bwrap := rt03LookPath(t, "bwrap")
	tools := sandboxTools{Bwrap: bwrap, Env: rt03LookPath(t, "env"), Systemd: false}
	if network == "egress" {
		tools.Pasta = rt03LookPath(t, "pasta")
	}
	return tools
}

func rt03LookPath(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("LookPath(%s) = %v under CI, want installed tool", name, err)
		}
		t.Skipf("sandbox RT-03 fixture skipped: %s missing: %v", name, err)
	}
	return path
}

func rt03Command(t *testing.T, fixture rt03Fixture, sandbox config.SandboxConfig, tools sandboxTools, env map[string]string) jobCommand {
	t.Helper()
	script := rt03Script(fixture.Attempt.Kind)
	command := []string{"/usr/bin/bash", "-c", script}
	built, err := buildSandboxCommand(command, sandbox, sandboxBuildOptions{
		Paths: sandboxPaths{EventLogDir: env["EVENT_LOG_DIR"], SigningKey: env["SIGNING_KEY"]},
		Tools: tools,
		Env:   mapLookup(env),
	})
	if err != nil {
		t.Fatalf("buildSandboxCommand(%s) = %v, want nil", fixture.ID, err)
	}
	return built
}

// runRT03Command runs the built command with the builder's environment, exactly as the
// shim does, so the credentials fixtures test the production environment handling.
func runRT03Command(t *testing.T, built jobCommand) bool {
	args := built.Argv
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) // #nosec G204 -- argv built by sandbox builder under test.
	cmd.Env = built.Env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("sandbox command failed: %v output=%s argv=%q", err, out, args)
	}
	if err != nil && os.Getenv("CI") != "true" && rt03RuntimeUnavailable(string(out), err) {
		t.Skipf("sandbox RT-03 fixture skipped: runtime unavailable: %v: %s", err, out)
	}
	if ctx.Err() != nil {
		t.Fatalf("sandbox command timed out: %v output=%s", ctx.Err(), out)
	}
	return err == nil
}

func rt03Allowed(t *testing.T, fixture rt03Fixture, world rt03World, commandOK bool) bool {
	t.Helper()
	switch fixture.Attempt.Kind {
	case "write_workspace":
		return fileHas(t, filepath.Join(world.workspace, "allowed.txt"), "workspace\n")
	case "write_event_log":
		return fileHas(t, filepath.Join(world.eventLog, "probe.txt"), "event-log\n")
	case "overwrite_config":
		return fileHas(t, world.configFile, "hacked\n")
	case "write_home":
		return fileHas(t, filepath.Join(world.hostHome, "probe.txt"), "home\n")
	default:
		return commandOK
	}
}

func rt03Script(kind string) string {
	switch kind {
	case "write_event_log":
		return `mkdir -p "$(dirname "$EVENT_LOG_DIR")" && printf 'event-log\n' > "$EVENT_LOG_DIR/probe.txt"`
	case "overwrite_config":
		return `printf 'hacked\n' > "$CONFIG_PATH"`
	case "write_home":
		return `printf 'home\n' > "$HOST_HOME/probe.txt"`
	case "write_workspace":
		return `printf 'workspace\n' > "$WORKSPACE/allowed.txt"`
	case "read_secret_env":
		return `test -n "${SECRET_TOKEN+x}"`
	case "read_signing_key":
		return `test -r "$SIGNING_KEY"`
	case "read_allowed_env":
		return `test "$VISIBLE_TOKEN" = allowed`
	case "signal_shim":
		return `kill -0 "$SHIM_PID"`
	case "signal_supervisor":
		return `kill -0 "$SUPERVISOR_PID"`
	case "signal_own_child":
		return `sleep 10 & child=$!; kill -TERM "$child"; wait "$child"; test "$?" -ne 0`
	case "tcp_connect", "tcp_connect_loopback", "tcp_connect_host":
		return `: > /dev/tcp/"$TARGET_HOST"/"$TARGET_PORT"`
	case "tcp_connect_gateway":
		// pasta forwards an unmapped gateway address to the outside world, where TEST-NET-1
		// never answers, so the attempt is bounded.
		return `timeout 5 bash -c ': > /dev/tcp/"$0"/"$1"' "$TARGET_HOST" "$TARGET_PORT"`
	}
	return "false"
}

func rt03Listener(t *testing.T, kind string) (string, string, func()) {
	t.Helper()
	host := "127.0.0.1"
	if kind == "tcp_connect_host" {
		host = firstNonLoopbackIPv4(t)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ln := rt03ServiceListener(ctx, t, host)
	done := make(chan struct{})
	go acceptRT03(ln, done)
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort(%s) = %v, want nil", ln.Addr().String(), err)
	}
	target := host
	if kind == "tcp_connect_gateway" {
		target = sandboxNetGateway
	}
	return target, port, func() {
		if err := ln.Close(); err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			t.Logf("Close(listener) = %v", err)
		}
		<-done
	}
}

// rt03ServiceListener binds below the kernel's ephemeral port range, where host services
// listen. pasta's automatic forwarding skips ephemeral ports, so a listener on port 0
// could never show a forwarding leak.
func rt03ServiceListener(ctx context.Context, t *testing.T, host string) net.Listener {
	t.Helper()
	const lowest = 10000
	body, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		t.Fatalf("read ephemeral port range: %v", err)
	}
	fields := strings.Fields(string(body))
	ephemeral, err := strconv.Atoi(fields[0])
	if err != nil || ephemeral <= lowest+256 {
		t.Fatalf("ephemeral port range %q leaves no service ports above %d: %v", body, lowest, err)
	}
	span := ephemeral - lowest
	start := os.Getpid() % span
	for i := 0; i < 256; i++ {
		port := lowest + (start+i)%span
		ln, listenErr := (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if listenErr == nil {
			return ln
		}
	}
	t.Fatalf("no free service port on %s in %d attempts", host, 256)
	return nil
}

func acceptRT03(ln net.Listener, done chan<- struct{}) {
	defer close(done)
	tcp, ok := ln.(*net.TCPListener)
	if !ok {
		return
	}
	if err := tcp.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		return
	}
	conn, err := ln.Accept()
	if err == nil {
		if closeErr := conn.Close(); closeErr != nil {
			return
		}
	}
}

func firstNonLoopbackIPv4(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("InterfaceAddrs() = %v, want nil", err)
	}
	for i := 0; i < len(addrs); i++ {
		if ip := rt03IPv4(addrs[i]); ip != "" {
			return ip
		}
	}
	if os.Getenv("CI") != "true" {
		t.Skip("sandbox RT-03 egress fixture skipped: no non-loopback IPv4 address")
	}
	t.Fatal("no non-loopback IPv4 address under CI")
	return ""
}

func rt03IPv4(addr net.Addr) string {
	ipNet, ok := addr.(*net.IPNet)
	if !ok || ipNet.IP.IsLoopback() {
		return ""
	}
	ip := ipNet.IP.To4()
	if ip == nil {
		return ""
	}
	return ip.String()
}

func mapLookup(env map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := env[name]
		return value, ok
	}
}

func writeRT03File(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) = %v, want nil", path, err)
	}
}

func fileHas(t *testing.T, path string, want string) bool {
	t.Helper()
	body, err := os.ReadFile(path) // #nosec G304 -- test-controlled path.
	if err != nil {
		return false
	}
	return string(body) == want
}

func rt03RuntimeUnavailable(out string, err error) bool {
	text := fmt.Sprintf("%v %s", err, out)
	if strings.Contains(text, "Operation not permitted") || strings.Contains(text, "creating new namespace failed") {
		return true
	}
	return strings.Contains(text, "/dev/net/tun")
}

func TestSandboxedJobKilledShimReleasesLock(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux parent-death signal required")
	}
	requireSystemdUserScope(t)
	job := sandboxedSupervisorJob(t, "sandbox-kill", `trap : TERM; while true; do sleep 1; done`)
	sup := testSupervisor(t, job)
	if err := sup.Start(testContext(t), job.Name); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}
	before := statusOne(t, sup, job.Name)
	assertSafeSignalPID(t, before.ShimPID)
	proc := os.Process{Pid: before.ShimPID}
	if err := proc.Signal(os.Kill); err != nil {
		t.Fatalf("SIGKILL shim %d = %v, want nil", before.ShimPID, err)
	}
	waitForProcessGone(t, before.PID)
	waitForUnlock(t, sup, job)
}

func TestSandboxedJobStopGraceReachesChild(t *testing.T) {
	requireSystemdUserScope(t)
	// A TERM that lands before the trap is installed either kills the job at its default
	// action or, before the inner env restores the default, is ignored and escalated after
	// the grace period. Stop only once the job reports its trap installed.
	script := `trap 'printf term > "$MARKER"; exit 0' TERM; printf ready > "$PWD/ready"; while true; do sleep 1; done`
	job := sandboxedSupervisorJob(t, "sandbox-term", script)
	marker := filepath.Join(job.Sandbox.Workspace, "term-marker")
	ready := filepath.Join(job.Sandbox.Workspace, "ready")
	job.Sandbox.EnvAllow = append(job.Sandbox.EnvAllow, "MARKER")
	t.Setenv("MARKER", marker)
	sup := testSupervisor(t, job)
	if err := sup.Start(testContext(t), job.Name); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}
	for i := 0; i < 250 && fileSize(ready) == 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if fileSize(ready) == 0 {
		t.Fatalf("job never reported its TERM trap installed (%s missing)", ready)
	}
	if err := sup.Stop(testContext(t), job.Name); err != nil {
		t.Fatalf("Stop() = %v, want nil", err)
	}
	if !fileHas(t, marker, "term") {
		body, err := os.ReadFile(job.LogPath)
		if err != nil {
			body = []byte(err.Error())
		}
		t.Fatalf("marker %s missing, want graceful TERM, log=%s", marker, body)
	}
}

func TestSandboxedJobCgroupMemoryMax(t *testing.T) {
	requireSystemdUserScope(t)
	job := sandboxedSupervisorJob(t, "sandbox-cgroup", `while true; do sleep 1; done`)
	sup := testSupervisor(t, job)
	if err := sup.Start(testContext(t), job.Name); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}
	st := statusOne(t, sup, job.Name)
	cgroup, memoryMax, err := jobCgroupMemoryMax(st.PID)
	if err != nil {
		t.Skipf("cgroup memory.max unavailable: %v", err)
	}
	if !strings.Contains(cgroup, "run-") || !strings.Contains(cgroup, ".scope") {
		t.Fatalf("cgroup = %q, want run-*.scope", cgroup)
	}
	if memoryMax != "268435456" {
		t.Fatalf("memory.max = %q, want 268435456", memoryMax)
	}
}

func requireSystemdUserScope(t *testing.T) {
	t.Helper()
	rt03LookPath(t, "systemd-run")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemd-run", "--user", "--scope", "--quiet", "--collect", "--", "/usr/bin/true")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	t.Skipf("sandbox supervisor integration skipped: systemd user scope unavailable: %v: %s", err, out)
}

func jobCgroupMemoryMax(pid int) (string, string, error) {
	body, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", "", err
	}
	cgroup, err := unifiedCgroupPath(string(body))
	if err != nil {
		return "", "", err
	}
	body, err = os.ReadFile(filepath.Join("/sys/fs/cgroup", cgroup, "memory.max")) // #nosec G304 -- cgroup path comes from /proc for the child under test.
	if err != nil {
		return cgroup, "", err
	}
	return cgroup, strings.TrimSpace(string(body)), nil
}

func unifiedCgroupPath(body string) (string, error) {
	lines := strings.Split(body, "\n")
	for i := 0; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "0::") {
			return strings.TrimPrefix(lines[i], "0::"), nil
		}
	}
	return "", fmt.Errorf("unified cgroup entry missing")
}

func sandboxedSupervisorJob(t *testing.T, name string, script string) config.JobConfig {
	t.Helper()
	workspace := t.TempDir()
	job := testJob(name, []string{"/usr/bin/bash", "-c", script})
	// testJob's log lives in the shared temp dir and accumulates across runs; a sandbox
	// failure message must show this run's output only.
	job.LogPath = filepath.Join(t.TempDir(), name+".log")
	job.Sandbox = config.SandboxConfig{Mode: "enforce", Workspace: workspace, Network: "none", MemoryMax: "256M", CPUWeight: 100, TasksMax: 128}
	job.Stop.GraceSeconds = 2
	return job
}

func TestSandboxArgvCarriesNoEnvValues(t *testing.T) {
	const secret = "s3cr3t-value-never-on-argv"
	sandbox := config.SandboxConfig{Mode: "enforce", Workspace: t.TempDir(), Network: "egress", MemoryMax: "2G", CPUWeight: 100, TasksMax: 512, EnvAllow: []string{"FORGE_TOKEN"}}
	built, err := buildSandboxCommand([]string{"/usr/bin/true"}, sandbox, sandboxBuildOptions{
		Tools: sandboxTools{SystemdRun: "/usr/bin/systemd-run", Bwrap: "/usr/bin/bwrap", Pasta: "/usr/bin/pasta", Env: "/usr/bin/env", Systemd: true},
		Env:   mapLookup(map[string]string{"FORGE_TOKEN": secret, "XDG_RUNTIME_DIR": "/run/user/1000", "UNLISTED": "x"}),
	})
	if err != nil {
		t.Fatalf("buildSandboxCommand() = %v, want nil", err)
	}
	for i := 0; i < len(built.Argv); i++ {
		if strings.Contains(built.Argv[i], secret) {
			t.Fatalf("argv[%d] = %q carries the secret, want it only in the environment", i, built.Argv[i])
		}
	}
	env := strings.Join(built.Env, "\n")
	if !strings.Contains(env, "FORGE_TOKEN="+secret) || !strings.Contains(env, "XDG_RUNTIME_DIR=/run/user/1000") || strings.Contains(env, "UNLISTED=") {
		t.Fatalf("env = %q, want the allowlisted and bus variables only", built.Env)
	}
	argv := strings.Join(built.Argv, " ")
	for _, want := range []string{"-- /usr/bin/env --ignore-signal=TERM /usr/bin/pasta", "--address 192.0.2.2", "--map-host-loopback none", "-T none -U none", "--unsetenv XDG_RUNTIME_DIR", "--unsetenv DBUS_SESSION_BUS_ADDRESS", "--unsetenv INVOCATION_ID", "-- /usr/bin/env --default-signal=TERM /usr/bin/true"} {
		if !strings.Contains(argv, want) {
			t.Fatalf("argv = %q, want %q", argv, want)
		}
	}
}

func TestSandboxJobSeesExactlyItsEnvironment(t *testing.T) {
	tools := rt03Tools(t, "none")
	sandbox := config.SandboxConfig{Mode: "enforce", Workspace: t.TempDir(), Network: "none", MemoryMax: "2G", CPUWeight: 100, TasksMax: 512, EnvAllow: []string{"ALLOWED"}}
	built, err := buildSandboxCommand([]string{"/usr/bin/env"}, sandbox, sandboxBuildOptions{
		Tools: tools,
		Env:   mapLookup(map[string]string{"ALLOWED": "yes", "SECRET": "no", "XDG_RUNTIME_DIR": "/run/user/1", "DBUS_SESSION_BUS_ADDRESS": "unix:path=/x"}),
	})
	if err != nil {
		t.Fatalf("buildSandboxCommand() = %v, want nil", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, built.Argv[0], built.Argv[1:]...) // #nosec G204 -- argv built by sandbox builder under test.
	cmd.Env = append(append([]string(nil), built.Env...), "INVOCATION_ID=systemd-would-add-this")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sandboxed env = %v, want nil", err)
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		names = append(names, strings.SplitN(line, "=", 2)[0])
	}
	sort.Strings(names)
	if got, want := strings.Join(names, ","), "ALLOWED,HOME,PATH,PWD,TMPDIR"; got != want {
		t.Fatalf("variables inside the sandbox = %s, want exactly %s", got, want)
	}
}

func TestSandboxRuntimeRefusesWorkspaceChangedSinceLoad(t *testing.T) {
	base := t.TempDir()
	other := filepath.Join(base, "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatalf("Mkdir(other) = %v, want nil", err)
	}
	workspace := filepath.Join(base, "workspace")
	if err := os.Symlink(other, workspace); err != nil {
		t.Fatalf("Symlink(workspace) = %v, want nil", err)
	}
	sandbox := config.SandboxConfig{Mode: "enforce", Workspace: workspace, Network: "none", MemoryMax: "2G", CPUWeight: 100, TasksMax: 512}
	_, err := buildSandboxCommand([]string{"/usr/bin/true"}, sandbox, sandboxBuildOptions{
		Tools: sandboxTools{Bwrap: "/usr/bin/bwrap"},
	})
	if err == nil || !strings.Contains(err.Error(), "changed since config load") {
		t.Fatalf("buildSandboxCommand(workspace now a symlink) = %v, want refusal", err)
	}
}

// TestSandboxedJobDiesWithItsShim proves the job inside the sandbox, not only bwrap, dies
// with a killed shim: the job's heartbeat file stops growing.
func TestSandboxedJobDiesWithItsShim(t *testing.T) {
	requireSystemdUserScope(t)
	job := sandboxedSupervisorJob(t, "sandbox-orphan", `while true; do printf . >> "$PWD/beat"; sleep 0.05; done`)
	beat := filepath.Join(job.Sandbox.Workspace, "beat")
	sup := testSupervisor(t, job)
	if err := sup.Start(testContext(t), job.Name); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}
	for i := 0; i < 100 && fileSize(beat) == 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if fileSize(beat) == 0 {
		t.Fatalf("heartbeat %s never started", beat)
	}
	before := statusOne(t, sup, job.Name)
	assertSafeSignalPID(t, before.ShimPID)
	proc := os.Process{Pid: before.ShimPID}
	if err := proc.Signal(os.Kill); err != nil {
		t.Fatalf("SIGKILL shim %d = %v, want nil", before.ShimPID, err)
	}
	waitForProcessGone(t, before.PID)
	stable := false
	for i := 0; i < 20 && !stable; i++ {
		first := fileSize(beat)
		time.Sleep(300 * time.Millisecond)
		stable = fileSize(beat) == first
	}
	if !stable {
		t.Fatalf("heartbeat %s still growing after the shim was killed: the sandboxed job outlived its shim", beat)
	}
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func TestSandboxPreflightRefusesTIOCSTI(t *testing.T) {
	old := sandboxReadFile
	t.Cleanup(func() { sandboxReadFile = old })
	sandboxReadFile = func(string) ([]byte, error) { return []byte("1\n"), nil }
	if err := requireTIOCSTIDisabled(); err == nil || !strings.Contains(err.Error(), "must be 0") {
		t.Fatalf("requireTIOCSTIDisabled(legacy_tiocsti=1) = %v, want refusal", err)
	}
	sandboxReadFile = func(string) ([]byte, error) { return []byte("0\n"), nil }
	if err := requireTIOCSTIDisabled(); err != nil {
		t.Fatalf("requireTIOCSTIDisabled(legacy_tiocsti=0) = %v, want nil", err)
	}
}

func TestSandboxPreflightRefusesMissingPasta(t *testing.T) {
	rt03LookPath(t, "systemd-run")
	rt03LookPath(t, "bwrap")
	old := sandboxLookPath
	t.Cleanup(func() { sandboxLookPath = old })
	sandboxLookPath = func(name string) (string, error) {
		if name == "pasta" {
			return "", exec.ErrNotFound
		}
		return old(name)
	}
	if _, err := sandboxToolchain(config.SandboxConfig{Network: "egress"}, sandboxTools{}); err == nil || !strings.Contains(err.Error(), "sandbox: pasta") {
		t.Fatalf("sandboxToolchain(egress, no pasta) = %v, want pasta refusal", err)
	}
}

func TestSandboxRuntimeRefusesWorkspaceOverlappingEventLog(t *testing.T) {
	base := t.TempDir()
	events := filepath.Join(base, "events")
	if err := os.Mkdir(events, 0o700); err != nil {
		t.Fatalf("Mkdir(events) = %v, want nil", err)
	}
	sandbox := config.SandboxConfig{Mode: "enforce", Workspace: base, Network: "none", MemoryMax: "2G", CPUWeight: 100, TasksMax: 512}
	_, err := buildSandboxCommand([]string{"/usr/bin/true"}, sandbox, sandboxBuildOptions{
		Paths: sandboxPaths{EventLogDir: events},
		Tools: sandboxTools{Bwrap: "/usr/bin/bwrap", Env: "/usr/bin/env"},
	})
	if err == nil || !strings.Contains(err.Error(), "overlaps protected path") {
		t.Fatalf("buildSandboxCommand(workspace contains the event log) = %v, want refusal", err)
	}
}

func withEnvLookPath(t *testing.T, path string) {
	t.Helper()
	old := sandboxLookPath
	t.Cleanup(func() { sandboxLookPath = old })
	sandboxLookPath = func(name string) (string, error) {
		if name == "env" {
			return path, nil
		}
		return old(name)
	}
}

func TestSandboxPreflightRefusesEnvOutsideUsr(t *testing.T) {
	body, err := os.ReadFile(rt03LookPath(t, "env"))
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "env")
	if err = os.WriteFile(outside, body, 0o700); err != nil {
		t.Fatalf("copy env: %v", err)
	}
	withEnvLookPath(t, outside)
	if _, err = resolveSignalEnv(); err == nil || !strings.Contains(err.Error(), "outside /usr") {
		t.Fatalf("resolveSignalEnv(%s) = %v, want outside-/usr refusal", outside, err)
	}
}

func TestSandboxPreflightRefusesEnvWithoutSignalFlags(t *testing.T) {
	withEnvLookPath(t, rt03LookPath(t, "false"))
	if _, err := resolveSignalEnv(); err == nil || !strings.Contains(err.Error(), "lacks --ignore-signal") {
		t.Fatalf("resolveSignalEnv(false) = %v, want probe refusal", err)
	}
}

func TestSandboxRuntimeRefusesInputExposingSigningKey(t *testing.T) {
	keyDir := t.TempDir()
	key := filepath.Join(keyDir, "seed")
	if err := os.WriteFile(key, []byte("seed"), 0o600); err != nil {
		t.Fatalf("WriteFile(key) = %v, want nil", err)
	}
	sandbox := config.SandboxConfig{Mode: "enforce", Workspace: t.TempDir(), Inputs: []string{keyDir}, Network: "none", MemoryMax: "2G", CPUWeight: 100, TasksMax: 512}
	_, err := buildSandboxCommand([]string{"/usr/bin/true"}, sandbox, sandboxBuildOptions{
		Paths: sandboxPaths{SigningKey: key},
		Tools: sandboxTools{Bwrap: "/usr/bin/bwrap", Env: "/usr/bin/env"},
	})
	if err == nil || !strings.Contains(err.Error(), "input "+keyDir+" overlaps protected path") {
		t.Fatalf("buildSandboxCommand(input contains the signing key) = %v, want refusal", err)
	}
}

func TestSandboxRuntimeRefusesInputChangedSinceLoad(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "elsewhere")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("Mkdir(target) = %v, want nil", err)
	}
	input := filepath.Join(base, "input")
	if err := os.Symlink(target, input); err != nil {
		t.Fatalf("Symlink(input) = %v, want nil", err)
	}
	sandbox := config.SandboxConfig{Mode: "enforce", Workspace: t.TempDir(), Inputs: []string{input}, Network: "none", MemoryMax: "2G", CPUWeight: 100, TasksMax: 512}
	_, err := buildSandboxCommand([]string{"/usr/bin/true"}, sandbox, sandboxBuildOptions{
		Tools: sandboxTools{Bwrap: "/usr/bin/bwrap", Env: "/usr/bin/env"},
	})
	if err == nil || !strings.Contains(err.Error(), "input "+input+" now resolves to "+target) {
		t.Fatalf("buildSandboxCommand(input now a symlink) = %v, want refusal", err)
	}
}
