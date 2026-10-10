//go:build linux

package supervisor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cordanaLLM/tribunus/internal/config"
)

const (
	legacyTIOCSTIPath = "/proc/sys/dev/tty/legacy_tiocsti"
	resolvedConfPath  = "/run/systemd/resolve/resolv.conf"

	// The egress namespace gets its own address from TEST-NET-1 (RFC 5737), which is never
	// routed on a real network, so it cannot shadow a real destination. With pasta's
	// default of copying the host's addresses, the host's own LAN address would point at
	// the namespace itself.
	sandboxNetAddress = "192.0.2.2"
	sandboxNetMask    = "24"
	sandboxNetGateway = "192.0.2.1"
)

// busEnvNames are what systemd-run --user needs to reach the user manager. The shim passes
// them to systemd-run, and bwrap unsets them, with INVOCATION_ID that systemd adds for the
// scope, before the job starts.
var busEnvNames = []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"}

var (
	sandboxLookPath = exec.LookPath
	sandboxReadFile = os.ReadFile
)

func buildSandboxCommand(command []string, sandbox config.SandboxConfig, opts sandboxBuildOptions) (jobCommand, error) {
	tools, err := sandboxToolchain(sandbox, opts.Tools)
	if err != nil {
		return jobCommand{}, err
	}
	resolvedCommand, err := resolveExecutable(command[0])
	if err != nil {
		return jobCommand{}, err
	}
	if err = validateSandboxRuntimePaths(sandbox, opts.Paths); err != nil {
		return jobCommand{}, err
	}
	inner := bwrapArgs(resolvedCommand, command, sandbox, tools)
	if sandbox.Network == "egress" {
		inner = append(pastaArgs(tools.Pasta), inner...)
	}
	// Stop sends SIGTERM to the whole process group, which includes bwrap and pasta. Both
	// would die on it, and bwrap's --die-with-parent would then SIGKILL the job before its
	// own TERM handling ran. They start with TERM ignored (inherited across exec), and the
	// job gets TERM back at its default just before it starts. SIGKILL, used for escalation
	// and on shim death, cannot be ignored.
	inner = append([]string{tools.Env, "--ignore-signal=TERM"}, inner...)
	env := sandboxEnv(sandbox, opts.Env)
	if !tools.Systemd {
		return jobCommand{Argv: inner, Env: env}, nil
	}
	return jobCommand{Argv: append(systemdRunArgs(tools.SystemdRun, sandbox), inner...), Env: env}, nil
}

func sandboxToolchain(sandbox config.SandboxConfig, tools sandboxTools) (sandboxTools, error) {
	if tools.Bwrap != "" {
		return tools, nil
	}
	var err error
	tools.Systemd = true
	tools.SystemdRun, err = resolveExecutable("systemd-run")
	if err != nil {
		return sandboxTools{}, fmt.Errorf("sandbox: systemd-run: %w", err)
	}
	tools.Bwrap, err = resolveExecutable("bwrap")
	if err != nil {
		return sandboxTools{}, fmt.Errorf("sandbox: bwrap: %w", err)
	}
	if tools.Env, err = resolveSignalEnv(); err != nil {
		return sandboxTools{}, err
	}
	if sandbox.Network == "egress" {
		tools.Pasta, err = resolveExecutable("pasta")
		if err != nil {
			return sandboxTools{}, fmt.Errorf("sandbox: pasta: %w", err)
		}
		if err = requireReadable(resolvedConfPath); err != nil {
			return sandboxTools{}, err
		}
	}
	if err = requireTIOCSTIDisabled(); err != nil {
		return sandboxTools{}, err
	}
	return tools, nil
}

// resolveSignalEnv finds an env that supports --ignore-signal and --default-signal (GNU
// coreutils 8.31 or later) under /usr, the one tree every sandbox binds, so the same binary
// runs outside and inside bwrap.
func resolveSignalEnv() (string, error) {
	path, err := resolveExecutable("env")
	if err != nil {
		return "", fmt.Errorf("sandbox: env: %w", err)
	}
	if !strings.HasPrefix(path, "/usr/") {
		return "", fmt.Errorf("sandbox: env %s is outside /usr, so the sandbox cannot run it", path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// #nosec G204 -- fixed probe of the resolved env binary.
	if out, err := exec.CommandContext(ctx, path, "--ignore-signal=TERM", "--default-signal=TERM", "true").CombinedOutput(); err != nil {
		return "", fmt.Errorf("sandbox: env %s lacks --ignore-signal/--default-signal: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	return path, nil
}

func resolveExecutable(name string) (string, error) {
	path, err := sandboxLookPath(name)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("eval symlinks %s: %w", path, err)
	}
	return resolved, nil
}

func validateSandboxRuntimePaths(sandbox config.SandboxConfig, paths sandboxPaths) error {
	if !filepath.IsAbs(sandbox.Workspace) {
		return fmt.Errorf("sandbox: workspace %s is not absolute", sandbox.Workspace)
	}
	if err := requireDirectory(sandbox.Workspace); err != nil {
		return err
	}
	if err := requireUnchangedResolution("workspace", sandbox.Workspace); err != nil {
		return err
	}
	protected := []string{paths.EventLogDir, paths.SigningKey}
	for i := 0; i < len(protected); i++ {
		if protected[i] != "" && pathOverlapsSandbox(sandbox.Workspace, protected[i]) {
			return fmt.Errorf("sandbox: workspace overlaps protected path %s", protected[i])
		}
	}
	return validateSandboxInputs(sandbox.Inputs, protected)
}

func validateSandboxInputs(inputs []string, protected []string) error {
	for i := 0; i < len(inputs); i++ {
		if !filepath.IsAbs(inputs[i]) {
			return fmt.Errorf("sandbox: input %s is not absolute", inputs[i])
		}
		if err := requireExists(inputs[i]); err != nil {
			return err
		}
		if err := requireUnchangedResolution("input", inputs[i]); err != nil {
			return err
		}
		for j := 0; j < len(protected); j++ {
			if protected[j] != "" && pathOverlapsSandbox(inputs[i], protected[j]) {
				return fmt.Errorf("sandbox: input %s overlaps protected path %s", inputs[i], protected[j])
			}
		}
	}
	return nil
}

// requireUnchangedResolution refuses a path that no longer resolves to itself. Config load
// stores workspace and inputs fully resolved, so a component swapped for a symlink since
// then would make bwrap bind wherever the link points.
func requireUnchangedResolution(field string, path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("sandbox: resolve %s %s: %w", field, path, err)
	}
	if resolved != filepath.Clean(path) {
		return fmt.Errorf("sandbox: %s %s now resolves to %s; refusing a path changed since config load", field, path, resolved)
	}
	return nil
}

func requireTIOCSTIDisabled() error {
	body, err := sandboxReadFile(legacyTIOCSTIPath)
	if err != nil {
		return fmt.Errorf("sandbox: read %s: %w", legacyTIOCSTIPath, err)
	}
	if strings.TrimSpace(string(body)) != "0" {
		return fmt.Errorf("sandbox: %s must be 0 before starting a job", legacyTIOCSTIPath)
	}
	return nil
}

func requireReadable(path string) error {
	f, err := os.Open(path) // #nosec G304 -- fixed platform preflight path.
	if err != nil {
		return fmt.Errorf("sandbox: %s readable: %w", path, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("sandbox: close %s: %w", path, err)
	}
	return nil
}

func requireExists(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("sandbox: stat %s: %w", path, err)
	}
	return nil
}

func requireDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("sandbox: stat workspace %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("sandbox: workspace %s is not a directory", path)
	}
	return nil
}

func pathOverlapsSandbox(a string, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	return a == b || strings.HasPrefix(a, b+string(os.PathSeparator)) || strings.HasPrefix(b, a+string(os.PathSeparator))
}

func systemdRunArgs(path string, sandbox config.SandboxConfig) []string {
	return []string{
		path, "--user", "--scope", "--quiet", "--collect",
		"-p", "MemoryMax=" + sandbox.MemoryMax,
		"-p", "CPUWeight=" + strconv.Itoa(sandbox.CPUWeight),
		"-p", "TasksMax=" + strconv.Itoa(sandbox.TasksMax),
		"--",
	}
}

// pastaArgs gives the job egress without the host's loopback: every port forward off
// (pasta's default "auto" forwards host-listening ports into the namespace), no gateway
// mapping to host loopback, and an address of its own.
func pastaArgs(path string) []string {
	return []string{
		path, "--config-net",
		"--address", sandboxNetAddress, "--netmask", sandboxNetMask, "--gateway", sandboxNetGateway,
		"--map-host-loopback", "none",
		"-t", "none", "-u", "none", "-T", "none", "-U", "none",
		"--quiet", "--",
	}
}

func bwrapArgs(commandPath string, command []string, sandbox config.SandboxConfig, tools sandboxTools) []string {
	args := bwrapBaseArgs(tools.Bwrap, sandbox)
	args = append(args, "--bind", sandbox.Workspace, sandbox.Workspace)
	args = appendInputBinds(args, sandbox.Inputs)
	args = append(args, "--ro-bind", commandPath, commandPath, "--chdir", sandbox.Workspace)
	args = append(args, "--setenv", "PATH", "/usr/bin:/bin", "--setenv", "HOME", sandbox.Workspace, "--setenv", "TMPDIR", "/tmp")
	for i := 0; i < len(busEnvNames); i++ {
		args = append(args, "--unsetenv", busEnvNames[i])
	}
	args = append(args, "--unsetenv", "INVOCATION_ID")
	jobCommand := append([]string{tools.Env, "--default-signal=TERM", commandPath}, command[1:]...)
	return append(append(args, "--"), jobCommand...)
}

func bwrapBaseArgs(path string, sandbox config.SandboxConfig) []string {
	args := []string{
		path, "--die-with-parent", "--unshare-all",
	}
	if sandbox.Network == "egress" {
		args = append(args, "--share-net")
	}
	args = append(args,
		"--ro-bind", "/usr", "/usr",
		"--symlink", "usr/bin", "/bin",
		"--symlink", "usr/lib", "/lib",
		"--symlink", "usr/lib64", "/lib64",
		"--ro-bind", "/etc/ssl", "/etc/ssl",
		"--ro-bind", "/etc/ca-certificates", "/etc/ca-certificates",
	)
	if sandbox.Network == "egress" {
		args = append(args, "--ro-bind", resolvedConfPath, "/etc/resolv.conf")
	}
	return append(args, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/var/tmp")
}

func appendInputBinds(args []string, inputs []string) []string {
	for i := 0; i < len(inputs); i++ {
		args = append(args, "--ro-bind", inputs[i], inputs[i])
	}
	return args
}

// sandboxEnv is the whole environment of the sandbox chain: the fixed PATH, HOME and TMPDIR,
// the allowlisted variables that are set, and the bus variables systemd-run needs (bwrap
// unsets those before the job starts). Values travel here, never on argv.
func sandboxEnv(sandbox config.SandboxConfig, lookup func(string) (string, bool)) []string {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + sandbox.Workspace, "TMPDIR=/tmp"}
	for i := 0; i < len(sandbox.EnvAllow); i++ {
		if value, ok := lookup(sandbox.EnvAllow[i]); ok {
			env = append(env, sandbox.EnvAllow[i]+"="+value)
		}
	}
	for i := 0; i < len(busEnvNames); i++ {
		if value, ok := lookup(busEnvNames[i]); ok {
			env = append(env, busEnvNames[i]+"="+value)
		}
	}
	return env
}
