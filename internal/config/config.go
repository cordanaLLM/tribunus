package config

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
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
)

//go:embed config.schema.json
var schemaJSON []byte

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
	}
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
