package config

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	coreconfig "github.com/golusoris/golusoris/core/config"
	golusorisschema "github.com/golusoris/golusoris/jsonschema"
)

const (
	MaxGraphDepth       = 64
	MaxJobs             = 64
	MaxJobArgs          = 32
	MaxWatches          = 64
	MaxRouterAliases    = 32
	MaxReplayRecords    = 1000000
	MaxJobRestarts      = 32
	MaxRestartBackoff   = 3600
	MaxStopGraceSeconds = 300
	MaxSandboxInputs    = 32
	MaxSandboxEnvAllow  = 64
	maxConfigKeyDepth   = 8
	disabledEnvPrefix   = "\x00TRIBUNUS_CONFIG_ENV_DISABLED_"
	schemaResourceID    = "config.schema.json"
	defaultGraphDepth   = 12
	defaultTokens       = 200000
	defaultWallClockSec = 3600
	defaultAlias        = "cordana/auto"
	defaultTimeoutSec   = 30
	defaultIntervalSec  = 5
	defaultReadyCount   = 2
	defaultEventLogDir  = ".tribunus"
	defaultLockSeconds  = 5
	defaultReplayMax    = 100000
	defaultJobSchedule  = "always"
	defaultRestart      = "never"
	defaultStopSignal   = "TERM"
	defaultSandboxMode  = "enforce"
	defaultSandboxNet   = "none"
	defaultMemoryMax    = "2G"
	defaultCPUWeight    = 100
	defaultTasksMax     = 512
	minMemoryMaxBytes   = 64 * 1024 * 1024
	maxMemoryMaxBytes   = 64 * 1024 * 1024 * 1024
	minCPUWeight        = 1
	maxCPUWeight        = 10000
	minTasksMax         = 16
	maxTasksMax         = 32768
)

//go:embed config.schema.json
var schemaJSON []byte

var sandboxEnvName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// reservedSandboxEnv are names the sandbox sets (PATH, HOME, TMPDIR) or removes before the
// job starts (the systemd bus variables and INVOCATION_ID); allowlisting one would be
// overridden silently, so config refuses it.
var reservedSandboxEnv = []string{"PATH", "HOME", "TMPDIR", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "INVOCATION_ID"}

// Config is the Tribunus process configuration loaded from a schema-checked
// file.
type Config struct {
	Graph     GraphConfig     `koanf:"graph" json:"graph"`
	Budgets   BudgetsConfig   `koanf:"budgets" json:"budgets"`
	Jobs      []JobConfig     `koanf:"jobs" json:"jobs"`
	Watches   []WatchConfig   `koanf:"watches" json:"watches"`
	Router    RouterConfig    `koanf:"router" json:"router"`
	Admission AdmissionConfig `koanf:"admission" json:"admission"`
	EventLog  EventLogConfig  `koanf:"event_log" json:"event_log"`
}

type GraphConfig struct {
	MaxDepth int `koanf:"max_depth" json:"max_depth"`
}

type BudgetsConfig struct {
	Default TaskBudget `koanf:"default" json:"default"`
}

type TaskBudget struct {
	Tokens           int `koanf:"tokens" json:"tokens"`
	WallClockSeconds int `koanf:"wall_clock_seconds" json:"wall_clock_seconds"`
}

type JobConfig struct {
	Name        string           `koanf:"name" json:"name"`
	Command     []string         `koanf:"command" json:"command"`
	LogPath     string           `koanf:"log_path" json:"log_path"`
	Schedule    string           `koanf:"schedule" json:"schedule"`
	RouterAlias string           `koanf:"router_alias" json:"router_alias"`
	Restart     JobRestartConfig `koanf:"restart" json:"restart"`
	Stop        JobStopConfig    `koanf:"stop" json:"stop"`
	Sandbox     SandboxConfig    `koanf:"sandbox" json:"sandbox"`
}

type JobRestartConfig struct {
	Policy         string `koanf:"policy" json:"policy"`
	MaxRestarts    int    `koanf:"max_restarts" json:"max_restarts"`
	BackoffSeconds int    `koanf:"backoff_seconds" json:"backoff_seconds"`
}

type JobStopConfig struct {
	Signal       string `koanf:"signal" json:"signal"`
	GraceSeconds int    `koanf:"grace_seconds" json:"grace_seconds"`
}

type SandboxConfig struct {
	Mode      string   `koanf:"mode" json:"mode"`
	Reason    string   `koanf:"reason" json:"reason"`
	Workspace string   `koanf:"workspace" json:"workspace"`
	Inputs    []string `koanf:"inputs" json:"inputs"`
	EnvAllow  []string `koanf:"env_allow" json:"env_allow"`
	Network   string   `koanf:"network" json:"network"`
	MemoryMax string   `koanf:"memory_max" json:"memory_max"`
	CPUWeight int      `koanf:"cpu_weight" json:"cpu_weight"`
	TasksMax  int      `koanf:"tasks_max" json:"tasks_max"`
}

type WatchConfig struct {
	Name        string `koanf:"name" json:"name"`
	Trigger     string `koanf:"trigger" json:"trigger"`
	RouterAlias string `koanf:"router_alias" json:"router_alias"`
}

type RouterConfig struct {
	Aliases []string `koanf:"aliases" json:"aliases"`
}

type AdmissionConfig struct {
	Probe ProbeConfig `koanf:"probe" json:"probe"`
}

type ProbeConfig struct {
	TimeoutSeconds  int `koanf:"timeout_seconds" json:"timeout_seconds"`
	IntervalSeconds int `koanf:"interval_seconds" json:"interval_seconds"`
	ReadyThreshold  int `koanf:"ready_threshold" json:"ready_threshold"`
}

type EventLogConfig struct {
	Dir                string `koanf:"dir" json:"dir"`
	SigningKeyPath     string `koanf:"signing_key_path" json:"signing_key_path"`
	PublicKey          string `koanf:"public_key" json:"public_key"`
	LockTimeoutSeconds int    `koanf:"lock_timeout_seconds" json:"lock_timeout_seconds"`
	MaxReplayRecords   int    `koanf:"max_replay_records" json:"max_replay_records"`
}

// SchemaJSON returns the embedded JSON Schema document used by Load.
func SchemaJSON() []byte {
	return append([]byte(nil), schemaJSON...)
}

// Default returns the Go defaults mirrored by config.schema.json.
func Default() Config {
	return Config{
		Graph: GraphConfig{MaxDepth: defaultGraphDepth},
		Budgets: BudgetsConfig{Default: TaskBudget{
			Tokens:           defaultTokens,
			WallClockSeconds: defaultWallClockSec,
		}},
		Jobs:    []JobConfig{},
		Watches: []WatchConfig{},
		Router:  RouterConfig{Aliases: []string{defaultAlias}},
		Admission: AdmissionConfig{Probe: ProbeConfig{
			TimeoutSeconds:  defaultTimeoutSec,
			IntervalSeconds: defaultIntervalSec,
			ReadyThreshold:  defaultReadyCount,
		}},
		EventLog: EventLogConfig{
			Dir:                defaultEventLogDir,
			SigningKeyPath:     "",
			PublicKey:          "",
			LockTimeoutSeconds: defaultLockSeconds,
			MaxReplayRecords:   defaultReplayMax,
		},
	}
}

// Load reads path through golusoris/core/config, validates the merged document
// against the embedded schema, then decodes into typed config.
func Load(ctx context.Context, path string) (Config, error) {
	if err := readyContext(ctx); err != nil {
		return Config{}, err
	}
	if path == "" {
		return Config{}, errors.New("config: path is required")
	}
	if _, err := os.Stat(path); err != nil {
		return Config{}, fmt.Errorf("config: stat %s: %w", path, err)
	}
	loaded, err := coreconfig.New(coreconfig.Options{
		Delimiter: ".",
		Files:     []string{path},
		Watch:     false,
		// core/config has no switch for its environment layer, and environment
		// values arrive as strings the schema would refuse or the weak decoder
		// would coerce. A prefix that starts with NUL matches no variable name,
		// so only the file is read (TestLoadDisablesEnvironmentOverrides).
		EnvPrefix: disabledEnvPrefix,
	})
	if err != nil {
		return Config{}, fmt.Errorf("config: load %s: %w", path, err)
	}
	flat := loaded.All()
	if err := validateFlat(flat); err != nil {
		return Config{}, err
	}
	cfg := Default()
	if err = loaded.Unmarshal("", &cfg); err != nil {
		return Config{}, err
	}
	applyJobDefaults(cfg.Jobs)
	if err = validateNames(cfg); err != nil {
		return Config{}, err
	}
	if err = validateJobSandboxes(&cfg, path); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyJobDefaults(jobs []JobConfig) {
	for i := 0; i < len(jobs); i++ {
		if jobs[i].Schedule == "" {
			jobs[i].Schedule = defaultJobSchedule
		}
		if jobs[i].RouterAlias == "" {
			jobs[i].RouterAlias = defaultAlias
		}
		if jobs[i].Restart.Policy == "" {
			jobs[i].Restart.Policy = defaultRestart
		}
		if jobs[i].Stop.Signal == "" {
			jobs[i].Stop.Signal = defaultStopSignal
		}
		applySandboxDefaults(&jobs[i].Sandbox)
	}
}

func applySandboxDefaults(sandbox *SandboxConfig) {
	if sandbox.Mode == "" {
		sandbox.Mode = defaultSandboxMode
	}
	if sandbox.Network == "" {
		sandbox.Network = defaultSandboxNet
	}
	if sandbox.MemoryMax == "" {
		sandbox.MemoryMax = defaultMemoryMax
	}
	if sandbox.CPUWeight == 0 {
		sandbox.CPUWeight = defaultCPUWeight
	}
	if sandbox.TasksMax == 0 {
		sandbox.TasksMax = defaultTasksMax
	}
}

func NormalizeSandbox(sandbox SandboxConfig) SandboxConfig {
	applySandboxDefaults(&sandbox)
	return sandbox
}

func ValidateSandbox(sandbox SandboxConfig) error {
	applySandboxDefaults(&sandbox)
	if err := validateSandboxMode(sandbox); err != nil {
		return err
	}
	if err := validateSandboxLimits(sandbox); err != nil {
		return err
	}
	return validateSandboxEnv(sandbox.EnvAllow)
}

func readyContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("config: nil context")
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("config: Load requires a context deadline")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("config: context: %w", err)
	}
	return nil
}

func validateFlat(flat map[string]any) error {
	doc, err := nestedDocument(flat)
	if err != nil {
		return err
	}
	schema, err := golusorisschema.Compile(schemaResourceID, schemaJSON)
	if err != nil {
		return fmt.Errorf("config: compile schema: %w", err)
	}
	if err = schema.ValidateValue(doc); err != nil {
		return fmt.Errorf("config: validate %s: %w", validationPath(err), err)
	}
	return nil
}

func nestedDocument(flat map[string]any) (map[string]any, error) {
	doc := map[string]any{}
	if len(flat) == 0 {
		return doc, nil
	}
	for key, value := range flat {
		parts := strings.Split(key, ".")
		if err := insertPath(doc, parts, value); err != nil {
			return nil, err
		}
	}
	return doc, nil
}

func insertPath(root map[string]any, parts []string, value any) error {
	if len(parts) == 0 || len(parts) > maxConfigKeyDepth {
		return fmt.Errorf("config: invalid key path depth %d", len(parts))
	}
	cursor := root
	last := len(parts) - 1
	for i := 0; i < last; i++ {
		next, ok := cursor[parts[i]]
		if !ok {
			child := map[string]any{}
			cursor[parts[i]] = child
			cursor = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("config: key path conflict at %s", strings.Join(parts[:i+1], "."))
		}
		cursor = child
	}
	cursor[parts[last]] = value
	return nil
}

func validateNames(cfg Config) error {
	if err := validateJobNames(cfg.Jobs); err != nil {
		return err
	}
	return validateWatchNames(cfg.Watches)
}

func validateJobSandboxes(cfg *Config, configPath string) error {
	if !anySandboxEnforced(cfg.Jobs) {
		for i := 0; i < len(cfg.Jobs); i++ {
			if err := validateJobSandbox(&cfg.Jobs[i], protectedPaths{}); err != nil {
				return fmt.Errorf("config: job %q sandbox.%w", cfg.Jobs[i].Name, err)
			}
		}
		return nil
	}
	guard, err := sandboxPathGuard(*cfg, configPath)
	if err != nil {
		return err
	}
	for i := 0; i < len(cfg.Jobs); i++ {
		if err = validateJobSandbox(&cfg.Jobs[i], guard); err != nil {
			return fmt.Errorf("config: job %q sandbox.%w", cfg.Jobs[i].Name, err)
		}
	}
	return nil
}

func validateJobSandbox(job *JobConfig, guard protectedPaths) error {
	applySandboxDefaults(&job.Sandbox)
	if err := validateSandboxMode(job.Sandbox); err != nil {
		return err
	}
	if err := validateSandboxLimits(job.Sandbox); err != nil {
		return err
	}
	if err := validateSandboxEnv(job.Sandbox.EnvAllow); err != nil {
		return err
	}
	if job.Sandbox.Mode == "off" {
		return nil
	}
	workspace, err := resolveSandboxWorkspace(job.Sandbox.Workspace)
	if err != nil {
		return err
	}
	if err = guard.check("workspace", workspace); err != nil {
		return err
	}
	inputs, err := resolveSandboxInputs(job.Sandbox.Inputs, guard)
	if err != nil {
		return err
	}
	job.Sandbox.Workspace, job.Sandbox.Inputs = workspace, inputs
	return nil
}

func validateSandboxMode(sandbox SandboxConfig) error {
	switch sandbox.Mode {
	case "enforce":
		if sandbox.Reason != "" {
			return fmt.Errorf("reason: forbidden unless mode=off")
		}
	case "off":
		if strings.TrimSpace(sandbox.Reason) == "" {
			return fmt.Errorf("reason: required when mode=off")
		}
	default:
		return fmt.Errorf("mode: unknown value %q", sandbox.Mode)
	}
	if sandbox.Network != "none" && sandbox.Network != "egress" {
		return fmt.Errorf("network: unknown value %q", sandbox.Network)
	}
	return nil
}

func validateSandboxLimits(sandbox SandboxConfig) error {
	bytes, err := parseMemoryMax(sandbox.MemoryMax)
	if err != nil {
		return fmt.Errorf("memory_max: %w", err)
	}
	if bytes < minMemoryMaxBytes || bytes > maxMemoryMaxBytes {
		return fmt.Errorf("memory_max: must be 64M..64G")
	}
	if sandbox.CPUWeight < minCPUWeight || sandbox.CPUWeight > maxCPUWeight {
		return fmt.Errorf("cpu_weight: must be 1..10000")
	}
	if sandbox.TasksMax < minTasksMax || sandbox.TasksMax > maxTasksMax {
		return fmt.Errorf("tasks_max: must be 16..32768")
	}
	return nil
}

func validateSandboxEnv(names []string) error {
	if len(names) > MaxSandboxEnvAllow {
		return fmt.Errorf("env_allow: exceeds %d", MaxSandboxEnvAllow)
	}
	for i := 0; i < len(names); i++ {
		if !sandboxEnvName.MatchString(names[i]) {
			return fmt.Errorf("env_allow/%d: invalid name %q", i, names[i])
		}
		for j := 0; j < len(reservedSandboxEnv); j++ {
			if names[i] == reservedSandboxEnv[j] {
				return fmt.Errorf("env_allow/%d: %s is reserved by the sandbox", i, names[i])
			}
		}
	}
	return nil
}

func resolveSandboxWorkspace(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("workspace: required when mode=enforce")
	}
	resolved, err := resolveExistingAbs(path)
	if err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("workspace: stat %s: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace: %s is not a directory", resolved)
	}
	return resolved, nil
}

func resolveSandboxInputs(paths []string, guard protectedPaths) ([]string, error) {
	if len(paths) > MaxSandboxInputs {
		return nil, fmt.Errorf("inputs: exceeds %d", MaxSandboxInputs)
	}
	out := make([]string, 0, len(paths))
	for i := 0; i < len(paths); i++ {
		resolved, err := resolveExistingAbs(paths[i])
		if err != nil {
			return nil, fmt.Errorf("inputs/%d: %w", i, err)
		}
		if err = guard.check(fmt.Sprintf("inputs/%d", i), resolved); err != nil {
			return nil, err
		}
		out = append(out, resolved)
	}
	return out, nil
}

type protectedPaths struct {
	paths []string
	home  string
}

func anySandboxEnforced(jobs []JobConfig) bool {
	for i := 0; i < len(jobs); i++ {
		if jobs[i].Sandbox.Mode != "off" {
			return true
		}
	}
	return false
}

// sandboxPathGuard fails closed when HOME cannot be resolved: without it the guard could
// not refuse $HOME as a workspace.
func sandboxPathGuard(cfg Config, configPath string) (protectedPaths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return protectedPaths{}, fmt.Errorf("config: sandbox needs HOME to guard it: %w", err)
	}
	if home, err = filepath.EvalSymlinks(home); err != nil {
		return protectedPaths{}, fmt.Errorf("config: sandbox resolve HOME: %w", err)
	}
	paths, err := protectedPathList(cfg, configPath)
	if err != nil {
		return protectedPaths{}, err
	}
	return protectedPaths{paths: paths, home: home}, nil
}

func protectedPathList(cfg Config, configPath string) ([]string, error) {
	candidates := []string{cfg.EventLog.Dir, cfg.EventLog.SigningKeyPath, configPath}
	out := make([]string, 0, len(candidates))
	for i := 0; i < len(candidates); i++ {
		if candidates[i] == "" {
			continue
		}
		resolved, err := resolvePossiblyMissingAbs(candidates[i])
		if err != nil {
			return nil, err
		}
		out = append(out, resolved)
	}
	return out, nil
}

func (guard protectedPaths) check(field string, path string) error {
	if path == "/" {
		return fmt.Errorf("%s: refuse /", field)
	}
	if guard.home != "" && samePath(path, guard.home) {
		return fmt.Errorf("%s: refuse HOME %s", field, guard.home)
	}
	for i := 0; i < len(guard.paths); i++ {
		if pathOverlaps(path, guard.paths[i]) {
			return fmt.Errorf("%s: overlaps protected path %s", field, guard.paths[i])
		}
	}
	return nil
}

func resolveExistingAbs(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s is not absolute", path)
	}
	return filepath.EvalSymlinks(path)
}

func resolvePossiblyMissingAbs(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("config: abs %s: %w", path, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	return filepath.Clean(abs), nil
}

func pathOverlaps(a string, b string) bool {
	return samePath(a, b) || pathContains(a, b) || pathContains(b, a)
}

func samePath(a string, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func pathContains(parent string, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == child || parent == "/" {
		return true
	}
	return strings.HasPrefix(child, parent+string(os.PathSeparator))
}

func parseMemoryMax(value string) (int64, error) {
	if value == "" {
		return 0, fmt.Errorf("is required")
	}
	unit := value[len(value)-1:]
	digits := value
	multiplier := int64(1)
	switch unit {
	case "K", "M", "G", "T":
		digits = value[:len(value)-1]
		multiplier = memoryUnitMultiplier(unit)
	}
	number, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || number <= 0 {
		return 0, fmt.Errorf("must be positive systemd size")
	}
	return number * multiplier, nil
}

func memoryUnitMultiplier(unit string) int64 {
	switch unit {
	case "K":
		return 1024
	case "M":
		return 1024 * 1024
	case "G":
		return 1024 * 1024 * 1024
	case "T":
		return 1024 * 1024 * 1024 * 1024
	}
	return 1
}

func validateJobNames(jobs []JobConfig) error {
	seen := make(map[string]struct{}, len(jobs))
	for i, job := range jobs {
		if _, ok := seen[job.Name]; ok {
			return fmt.Errorf("config: jobs/%d/name: duplicate name %q", i, job.Name)
		}
		seen[job.Name] = struct{}{}
	}
	return nil
}

func validateWatchNames(watches []WatchConfig) error {
	seen := make(map[string]struct{}, len(watches))
	for i, watch := range watches {
		if _, ok := seen[watch.Name]; ok {
			return fmt.Errorf("config: watches/%d/name: duplicate name %q", i, watch.Name)
		}
		seen[watch.Name] = struct{}{}
	}
	return nil
}

func validationPath(err error) string {
	text := err.Error()
	start := strings.Index(text, "'/")
	if start < 0 {
		return "/"
	}
	rest := text[start+1:]
	end := strings.Index(rest, "'")
	if end < 0 {
		return rest
	}
	return rest[:end]
}
