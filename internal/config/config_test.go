package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadPositiveFullFile(t *testing.T) {
	cfg, err := Load(testContext(t), writeConfig(t, `{
  "graph": {"max_depth": 24},
  "budgets": {"default": {"tokens": 300000, "wall_clock_seconds": 7200}},
  "jobs": [{
    "name": "daily-catalog-sync",
    "command": ["/bin/echo", "catalog"],
    "log_path": "/var/log/tribunus/daily-catalog-sync.log",
    "schedule": "0 2 * * *",
    "router_alias": "cordana/auto",
    "max_runtime_seconds": 1800,
    "restart": {"policy": "on-failure", "max_restarts": 3, "backoff_seconds": 2},
    "stop": {"signal": "TERM", "grace_seconds": 5},
    "sandbox": {"mode": "enforce", "workspace": "/var/tmp", "network": "none", "memory_max": "2G", "memory_high": "1536M", "cpu_weight": 100, "tasks_max": 512}
  }],
  "watches": [{"name": "catalog-source-change", "trigger": "git:internal/sources", "router_alias": "cordana/coding"}],
  "router": {"aliases": ["cordana/auto", "cordana/coding"]},
  "admission": {"probe": {"timeout_seconds": 45, "interval_seconds": 3, "ready_threshold": 4}},
  "event_log": {
    "dir": ".tribunus",
    "signing_key_path": "/run/secrets/tribunus-eventlog-seed",
    "public_key": "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
    "lock_timeout_seconds": 7,
    "max_replay_records": 1234
  }
}`))
	if err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}
	if cfg.Graph.MaxDepth != 24 || cfg.Budgets.Default.Tokens != 300000 {
		t.Fatalf("Load() = %+v, want full file values", cfg)
	}
	if len(cfg.Jobs) != 1 || cfg.Jobs[0].RouterAlias != "cordana/auto" {
		t.Fatalf("Load() jobs = %+v, want one scheduled alias declaration", cfg.Jobs)
	}
	if cfg.Jobs[0].Command[1] != "catalog" || cfg.Jobs[0].Restart.Policy != "on-failure" {
		t.Fatalf("Load() jobs = %+v, want command and restart policy", cfg.Jobs)
	}
	if cfg.Jobs[0].Sandbox.MemoryHigh != "1536M" {
		t.Fatalf("Load() jobs[0].sandbox.memory_high = %q, want 1536M", cfg.Jobs[0].Sandbox.MemoryHigh)
	}
	if cfg.Jobs[0].MaxRuntimeSeconds != 1800 {
		t.Fatalf("Load() jobs[0].max_runtime_seconds = %d, want 1800", cfg.Jobs[0].MaxRuntimeSeconds)
	}
	if cfg.EventLog.LockTimeoutSeconds != 7 || cfg.EventLog.MaxReplayRecords != 1234 {
		t.Fatalf("Load() event_log = %+v, want full event log values", cfg.EventLog)
	}
}

func TestLoadPositiveMinimalFileDefaults(t *testing.T) {
	cfg, err := Load(testContext(t), writeConfig(t, "{}\n"))
	if err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Fatalf("Load() = %+v, want defaults %+v", cfg, Default())
	}
}

func TestLoadNegativeValidation(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr []string
	}{
		{"unknown top-level", `{"extra": true}`, []string{"/", "additional properties"}},
		{"unknown nested", `{"graph": {"extra": true}}`, []string{"/graph", "additional properties"}},
		{"wrong type", `{"graph": {"max_depth": "12"}}`, []string{"/graph/max_depth", "want integer"}},
		{"out of range", `{"graph": {"max_depth": 65}}`, []string{"/graph/max_depth", "maximum"}},
		{"bad alias", `{"router": {"aliases": ["public/alias"]}}`, []string{"/router/aliases/0", "pattern"}},
		{"bad event public key", `{"event_log": {"public_key": "xyz"}}`, []string{"/event_log/public_key", "pattern"}},
		{"bad replay max", `{"event_log": {"max_replay_records": 1000001}}`, []string{"/event_log/max_replay_records", "maximum"}},
		{"bad job restart policy", `{"jobs": [
  {"name": "bad-job", "command": ["/bin/true"], "log_path": "/tmp/bad.log", "schedule": "always", "restart": {"policy": "sometimes"}}
]}`, []string{"/jobs/0/restart/policy", "valid"}},
		{"bad job stop signal", `{"jobs": [
  {"name": "bad-job", "command": ["/bin/true"], "log_path": "/tmp/bad.log", "schedule": "always", "stop": {"signal": "KILL"}}
]}`, []string{"/jobs/0/stop/signal", "valid"}},
		{"job runtime above a year", `{"jobs": [
  {"name": "bad-job", "command": ["/bin/true"], "log_path": "/tmp/bad.log", "max_runtime_seconds": 31536001, "sandbox": {"mode": "off", "reason": "test"}}
]}`, []string{"/jobs/0/max_runtime_seconds", "maximum"}},
		{"negative job runtime", `{"jobs": [
  {"name": "bad-job", "command": ["/bin/true"], "log_path": "/tmp/bad.log", "max_runtime_seconds": -1, "sandbox": {"mode": "off", "reason": "test"}}
]}`, []string{"/jobs/0/max_runtime_seconds", "minimum"}},
		{"bad job soft memory limit", `{"jobs": [
  {"name": "bad-job", "command": ["/bin/true"], "log_path": "/tmp/bad.log", "sandbox": {"mode": "off", "reason": "legacy", "memory_high": "lots"}}
]}`, []string{"/jobs/0/sandbox/memory_high", "pattern"}},
		{"bad job sandbox mode", `{"jobs": [
  {"name": "bad-job", "command": ["/bin/true"], "log_path": "/tmp/bad.log", "sandbox": {"mode": "maybe"}}
]}`, []string{"/jobs/0/sandbox/mode", "valid"}},
		{"bad job sandbox env", `{"jobs": [
  {"name": "bad-job", "command": ["/bin/true"], "log_path": "/tmp/bad.log", "sandbox": {"mode": "off", "reason": "legacy", "env_allow": ["bad-name"]}}
]}`, []string{"/jobs/0/sandbox/env_allow/0", "pattern"}},
		{"duplicate job names", `{"jobs": [
  {"name": "repeat-job", "command": ["/bin/true"], "log_path": "/tmp/a.log", "schedule": "0 1 * * *", "router_alias": "cordana/auto", "sandbox": {"mode": "off", "reason": "test"}},
  {"name": "repeat-job", "command": ["/bin/true"], "log_path": "/tmp/b.log", "schedule": "0 2 * * *", "router_alias": "cordana/auto", "sandbox": {"mode": "off", "reason": "test"}}
]}`, []string{"jobs/1/name", "duplicate"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(testContext(t), writeConfig(t, tt.body))
			if err == nil {
				t.Fatal("Load() = nil error, want validation failure")
			}
			assertErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestLoadBoundaryMaxDepth(t *testing.T) {
	for _, depth := range []int{1, MaxGraphDepth} {
		body := `{"graph": {"max_depth": ` + jsonInt(depth) + `}}`
		if _, err := Load(testContext(t), writeConfig(t, body)); err != nil {
			t.Fatalf("Load(max_depth=%d) = %v, want nil", depth, err)
		}
	}
	for _, depth := range []int{0, MaxGraphDepth + 1} {
		body := `{"graph": {"max_depth": ` + jsonInt(depth) + `}}`
		if _, err := Load(testContext(t), writeConfig(t, body)); err == nil {
			t.Fatalf("Load(max_depth=%d) = nil error, want bounds failure", depth)
		}
	}
}

func TestLoadBoundaryArrays(t *testing.T) {
	empty := `{"jobs": [], "watches": []}`
	if _, err := Load(testContext(t), writeConfig(t, empty)); err != nil {
		t.Fatalf("Load(empty arrays) = %v, want nil", err)
	}
	maxBody := declarationsJSON(t, MaxJobs, MaxWatches)
	if _, err := Load(testContext(t), writeConfig(t, maxBody)); err != nil {
		t.Fatalf("Load(max arrays) = %v, want nil", err)
	}
	tooMany := declarationsJSON(t, MaxJobs+1, 0)
	if _, err := Load(testContext(t), writeConfig(t, tooMany)); err == nil {
		t.Fatal("Load(too many jobs) = nil error, want maxItems failure")
	}
}

func TestLoadDisablesEnvironmentOverrides(t *testing.T) {
	t.Setenv("APP_GRAPH_MAX_DEPTH", "64")
	cfg, err := Load(testContext(t), writeConfig(t, "{}\n"))
	if err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}
	if cfg.Graph.MaxDepth != defaultGraphDepth {
		t.Fatalf("MaxDepth = %d, want env ignored default %d", cfg.Graph.MaxDepth, defaultGraphDepth)
	}
}

func TestSchemaFileAgreesWithEmbedded(t *testing.T) {
	docs, err := os.ReadFile(filepath.Join("..", "..", "docs", "config.schema.json"))
	if err != nil {
		t.Fatalf("ReadFile(docs schema) = %v, want nil", err)
	}
	if string(docs) != string(SchemaJSON()) {
		t.Fatal("docs/config.schema.json differs from embedded internal/config/config.schema.json")
	}
}

func TestConfigSchemaDefaultsAgreeWithGoTypes(t *testing.T) {
	doc := decodeSchema(t)
	def := Default()
	assertSchemaDefault(t, doc, def.Graph.MaxDepth, "graph", "max_depth")
	assertSchemaDefault(t, doc, def.Budgets.Default.Tokens, "budgets", "default", "tokens")
	assertSchemaDefault(t, doc, def.Budgets.Default.WallClockSeconds, "budgets", "default", "wall_clock_seconds")
	assertSchemaDefault(t, doc, def.Admission.Probe.TimeoutSeconds, "admission", "probe", "timeout_seconds")
	assertSchemaDefault(t, doc, def.Admission.Probe.IntervalSeconds, "admission", "probe", "interval_seconds")
	assertSchemaDefault(t, doc, def.Admission.Probe.ReadyThreshold, "admission", "probe", "ready_threshold")
	assertSchemaDefault(t, doc, def.EventLog.Dir, "event_log", "dir")
	assertSchemaDefault(t, doc, def.EventLog.SigningKeyPath, "event_log", "signing_key_path")
	assertSchemaDefault(t, doc, def.EventLog.PublicKey, "event_log", "public_key")
	assertSchemaDefault(t, doc, def.EventLog.LockTimeoutSeconds, "event_log", "lock_timeout_seconds")
	assertSchemaDefault(t, doc, def.EventLog.MaxReplayRecords, "event_log", "max_replay_records")
	assertSchemaDefault(t, doc, def.Router.Aliases, "router", "aliases")
	assertSchemaDefault(t, doc, def.Jobs, "jobs")
	assertSchemaDefault(t, doc, def.Watches, "watches")
}

func TestLoadJobDefaults(t *testing.T) {
	workspace := t.TempDir()
	cfg, err := Load(testContext(t), writeConfig(t, fmt.Sprintf(`{"jobs": [{
  "name": "daemon",
  "command": ["/bin/true"],
  "log_path": "/tmp/daemon.log",
  "sandbox": {"workspace": %q}
}]}`, workspace)))
	if err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}
	job := cfg.Jobs[0]
	if job.Schedule != "always" || job.RouterAlias != "cordana/auto" || job.Restart.Policy != "never" || job.Stop.Signal != "TERM" {
		t.Fatalf("job defaults = %+v, want schedule, alias, restart and stop defaults", job)
	}
	if job.Sandbox.Mode != "enforce" || job.Sandbox.Network != "none" || job.Sandbox.MemoryMax != "2G" {
		t.Fatalf("sandbox defaults = %+v, want enforce none 2G", job.Sandbox)
	}
	if job.Sandbox.MemoryHigh != "" {
		t.Fatalf("memory_high default = %q, want none", job.Sandbox.MemoryHigh)
	}
	if job.MaxRuntimeSeconds != 0 {
		t.Fatalf("max_runtime_seconds default = %d, want 0: no deadline of its own", job.MaxRuntimeSeconds)
	}
}

// TestJobRuntimeBoundIsTheSchemaBound: the Go constant and the schema's maximum are one bound.
func TestJobRuntimeBoundIsTheSchemaBound(t *testing.T) {
	at := fmt.Sprintf(`{"jobs": [{"name": "edge", "command": ["/bin/true"], "log_path": "/tmp/edge.log", "max_runtime_seconds": %d, "sandbox": {"mode": "off", "reason": "test"}}]}`, MaxJobRuntimeSeconds)
	cfg, err := Load(testContext(t), writeConfig(t, at))
	if err != nil || cfg.Jobs[0].MaxRuntimeSeconds != MaxJobRuntimeSeconds {
		t.Fatalf("Load(runtime at the bound %d) = %v, want it accepted", MaxJobRuntimeSeconds, err)
	}
	above := fmt.Sprintf(`{"jobs": [{"name": "edge", "command": ["/bin/true"], "log_path": "/tmp/edge.log", "max_runtime_seconds": %d, "sandbox": {"mode": "off", "reason": "test"}}]}`, MaxJobRuntimeSeconds+1)
	if _, err = Load(testContext(t), writeConfig(t, above)); err == nil {
		t.Fatalf("Load(runtime above the bound) = nil, want the schema to refuse it")
	}
}

func TestExampleConfigValidates(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "config.example.yaml")
	if _, err := Load(testContext(t), path); err != nil {
		t.Fatalf("Load(docs/config.example.yaml) = %v, want nil", err)
	}
}

type schemaNode struct {
	Default    any                   `json:"default"`
	Properties map[string]schemaNode `json:"properties"`
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile(config) = %v, want nil", err)
	}
	return path
}

func assertErrorContains(t *testing.T, err error, parts []string) {
	t.Helper()
	text := err.Error()
	for _, part := range parts {
		if !strings.Contains(text, part) {
			t.Fatalf("error %q does not contain %q", text, part)
		}
	}
}

func jsonInt(v int) string {
	body, err := json.Marshal(v)
	if err != nil {
		return "0"
	}
	return string(body)
}

func declarationsJSON(t *testing.T, jobs int, watches int) string {
	t.Helper()
	doc := map[string]any{
		"jobs":    jobDeclarations(jobs),
		"watches": watchDeclarations(watches),
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("Marshal(declarations) = %v, want nil", err)
	}
	return string(body)
}

func jobDeclarations(count int) []map[string]any {
	out := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, map[string]any{
			"name":         "job-" + jsonInt(i),
			"command":      []string{"/bin/true"},
			"log_path":     "/tmp/job-" + jsonInt(i) + ".log",
			"schedule":     "event",
			"router_alias": defaultAlias,
			"sandbox":      map[string]any{"mode": "off", "reason": "test fixture"},
		})
	}
	return out
}

func watchDeclarations(count int) []map[string]any {
	out := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, map[string]any{
			"name":         "watch-" + jsonInt(i),
			"trigger":      "event",
			"router_alias": defaultAlias,
		})
	}
	return out
}

func decodeSchema(t *testing.T) schemaNode {
	t.Helper()
	var doc schemaNode
	if err := json.Unmarshal(SchemaJSON(), &doc); err != nil {
		t.Fatalf("Unmarshal(schema) = %v, want nil", err)
	}
	return doc
}

func assertSchemaDefault(t *testing.T, doc schemaNode, want any, path ...string) {
	t.Helper()
	got := schemaDefault(t, doc, path)
	if !reflect.DeepEqual(normalizeSchemaValue(got), normalizeSchemaValue(want)) {
		t.Fatalf("schema default %s = %#v, want %#v", strings.Join(path, "."), got, want)
	}
}

func schemaDefault(t *testing.T, doc schemaNode, path []string) any {
	t.Helper()
	node := doc
	for _, part := range path {
		next, ok := node.Properties[part]
		if !ok {
			t.Fatalf("schema path %s missing", strings.Join(path, "."))
		}
		node = next
	}
	return node.Default
}

func normalizeSchemaValue(v any) any {
	switch typed := v.(type) {
	case float64:
		return int(typed)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if ok {
				out = append(out, text)
			}
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	case []JobConfig:
		return []string{}
	case []WatchConfig:
		return []string{}
	default:
		return typed
	}
}

// TestLoadPartialFileKeepsSiblingDefaults: a file that sets one key of a
// section keeps the schema defaults of every key it leaves out.
func TestLoadPartialFileKeepsSiblingDefaults(t *testing.T) {
	cfg, err := Load(testContext(t), writeConfig(t, `{"graph":{"max_depth":5},"admission":{"probe":{"timeout_seconds":9}},"budgets":{"default":{"tokens":7}}}`+"\n"))
	if err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}
	want := Default()
	want.Graph.MaxDepth = 5
	want.Admission.Probe.TimeoutSeconds = 9
	want.Budgets.Default.Tokens = 7
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("Load() = %+v, want %+v", cfg, want)
	}
}

func TestLoadRefusesReservedSandboxEnv(t *testing.T) {
	workspace := t.TempDir()
	reserved := []string{"PATH", "HOME", "TMPDIR", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "INVOCATION_ID"}
	for i := 0; i < len(reserved); i++ {
		body := fmt.Sprintf(`{"jobs": [{"name": "daemon", "command": ["/bin/true"], "log_path": "/tmp/daemon.log", "sandbox": {"workspace": %q, "env_allow": [%q]}}]}`, workspace, reserved[i])
		if _, err := Load(testContext(t), writeConfig(t, body)); err == nil || !strings.Contains(err.Error(), reserved[i]+" is reserved") {
			t.Fatalf("Load(env_allow %s) = %v, want reserved-name error", reserved[i], err)
		}
	}
}

func TestLoadSandboxFailsClosedWithoutHome(t *testing.T) {
	workspace := t.TempDir()
	enforced := writeConfig(t, fmt.Sprintf(`{"jobs": [{"name": "daemon", "command": ["/bin/true"], "log_path": "/tmp/daemon.log", "sandbox": {"workspace": %q}}]}`, workspace))
	off := writeConfig(t, `{"jobs": [{"name": "daemon", "command": ["/bin/true"], "log_path": "/tmp/daemon.log", "sandbox": {"mode": "off", "reason": "test"}}]}`)
	t.Setenv("HOME", "")
	if _, err := Load(testContext(t), enforced); err == nil || !strings.Contains(err.Error(), "sandbox needs HOME") {
		t.Fatalf("Load(no HOME, enforced sandbox) = %v, want fail-closed HOME error", err)
	}
	if _, err := Load(testContext(t), off); err != nil {
		t.Fatalf("Load(no HOME, sandbox off) = %v, want nil", err)
	}
}

func TestLoadRefusesHomeReachedThroughSymlink(t *testing.T) {
	home := t.TempDir()
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(home, link); err != nil {
		t.Fatalf("Symlink(home) = %v, want nil", err)
	}
	t.Setenv("HOME", link)
	body := fmt.Sprintf(`{"jobs": [{"name": "daemon", "command": ["/bin/true"], "log_path": "/tmp/daemon.log", "sandbox": {"workspace": %q}}]}`, home)
	if _, err := Load(testContext(t), writeConfig(t, body)); err == nil || !strings.Contains(err.Error(), "refuse HOME") {
		t.Fatalf("Load(workspace = HOME behind a symlink) = %v, want HOME refusal", err)
	}
}

func TestValidateSandboxRules(t *testing.T) {
	tooMany := make([]string, MaxSandboxEnvAllow+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("NAME_%d", i)
	}
	cases := []struct {
		name string
		edit func(*SandboxConfig)
		want string
	}{
		{"enforce", func(*SandboxConfig) {}, ""},
		{"off with reason", func(s *SandboxConfig) { s.Mode, s.Reason = "off", "legacy" }, ""},
		{"off without reason", func(s *SandboxConfig) { s.Mode, s.Reason = "off", "  " }, "reason: required"},
		{"enforce with reason", func(s *SandboxConfig) { s.Reason = "why" }, "reason: forbidden"},
		{"unknown mode", func(s *SandboxConfig) { s.Mode = "audit" }, "mode: unknown"},
		{"unknown network", func(s *SandboxConfig) { s.Network = "host" }, "network: unknown"},
		{"memory at floor", func(s *SandboxConfig) { s.MemoryMax = "64M" }, ""},
		{"memory below floor", func(s *SandboxConfig) { s.MemoryMax = "65535K" }, "memory_max: must be 64M..64G"},
		{"memory at ceiling", func(s *SandboxConfig) { s.MemoryMax = "64G" }, ""},
		{"memory above ceiling", func(s *SandboxConfig) { s.MemoryMax = "65537M" }, "memory_max: must be 64M..64G"},
		{"memory wraps int64", func(s *SandboxConfig) { s.MemoryMax = "17179869189G" }, "memory_max: is too large"},
		{"memory not a size", func(s *SandboxConfig) { s.MemoryMax = "2X" }, "memory_max: must be positive"},
		{"no soft limit", func(s *SandboxConfig) { s.MemoryHigh = "" }, ""},
		{"soft limit below the hard one", func(s *SandboxConfig) { s.MemoryHigh = "1536M" }, ""},
		{"soft limit equal to the hard one", func(s *SandboxConfig) { s.MemoryHigh = "2G" }, ""},
		{"soft limit one byte above the hard one", func(s *SandboxConfig) { s.MemoryHigh = "2147483649" }, "memory_high: must be 64M..memory_max"},
		{"soft limit at floor", func(s *SandboxConfig) { s.MemoryHigh = "64M" }, ""},
		{"soft limit below floor", func(s *SandboxConfig) { s.MemoryHigh = "65535K" }, "memory_high: must be 64M..memory_max"},
		{"soft limit not a size", func(s *SandboxConfig) { s.MemoryHigh = "1.5G" }, "memory_high: must be positive"},
		{"soft limit wraps int64", func(s *SandboxConfig) { s.MemoryHigh = "17179869189G" }, "memory_high: is too large"},
		{"cpu at floor", func(s *SandboxConfig) { s.CPUWeight = 1 }, ""},
		{"cpu below floor", func(s *SandboxConfig) { s.CPUWeight = -1 }, "cpu_weight"},
		{"cpu at ceiling", func(s *SandboxConfig) { s.CPUWeight = 10000 }, ""},
		{"cpu above ceiling", func(s *SandboxConfig) { s.CPUWeight = 10001 }, "cpu_weight"},
		{"tasks at floor", func(s *SandboxConfig) { s.TasksMax = 16 }, ""},
		{"tasks below floor", func(s *SandboxConfig) { s.TasksMax = 15 }, "tasks_max"},
		{"tasks at ceiling", func(s *SandboxConfig) { s.TasksMax = 32768 }, ""},
		{"tasks above ceiling", func(s *SandboxConfig) { s.TasksMax = 32769 }, "tasks_max"},
		{"env name invalid", func(s *SandboxConfig) { s.EnvAllow = []string{"lower"} }, "invalid name"},
		{"env names at limit", func(s *SandboxConfig) { s.EnvAllow = tooMany[:MaxSandboxEnvAllow] }, ""},
		{"env names over limit", func(s *SandboxConfig) { s.EnvAllow = tooMany }, "env_allow: exceeds"},
	}
	for _, tc := range cases {
		sandbox := SandboxConfig{Mode: "enforce", Network: "none", MemoryMax: "2G", CPUWeight: 100, TasksMax: 512}
		tc.edit(&sandbox)
		err := ValidateSandbox(sandbox)
		if tc.want == "" && err != nil {
			t.Fatalf("%s: ValidateSandbox() = %v, want nil", tc.name, err)
		}
		if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Fatalf("%s: ValidateSandbox() = %v, want error containing %q", tc.name, err, tc.want)
		}
	}
}

type sandboxPathWorld struct {
	root, events, keys, key, confDir, home, file, exeDir string
}

func newSandboxPathWorld(t *testing.T) sandboxPathWorld {
	t.Helper()
	root := t.TempDir()
	w := sandboxPathWorld{root: root, events: filepath.Join(root, "state", "events"), keys: filepath.Join(root, "keys"),
		confDir: filepath.Join(root, "conf"), home: filepath.Join(root, "home", "user"), file: filepath.Join(root, "file")}
	w.key = filepath.Join(w.keys, "seed")
	for _, dir := range []string{w.events, filepath.Join(w.events, "sub"), w.keys, w.confDir, filepath.Join(w.home, "jobs"), filepath.Join(root, "ws")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) = %v, want nil", dir, err)
		}
	}
	for _, file := range []string{w.key, w.file} {
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) = %v, want nil", file, err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() = %v, want nil", err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		t.Fatalf("EvalSymlinks(executable) = %v, want nil", err)
	}
	w.exeDir = filepath.Dir(exe)
	t.Setenv("HOME", w.home)
	return w
}

func (w sandboxPathWorld) load(t *testing.T, workspace string, inputs []string) error {
	t.Helper()
	sandbox, err := json.Marshal(map[string]any{"workspace": workspace, "inputs": inputs})
	if err != nil {
		t.Fatalf("marshal sandbox: %v", err)
	}
	body := fmt.Sprintf(`{"event_log": {"dir": %q, "signing_key_path": %q}, "jobs": [{"name": "daemon", "command": ["/bin/true"], "log_path": "/tmp/daemon.log", "sandbox": %s}]}`, w.events, w.key, sandbox)
	path := filepath.Join(w.confDir, "config.json")
	if err = os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile(config) = %v, want nil", err)
	}
	_, err = Load(testContext(t), path)
	return err
}

func TestLoadRefusesSandboxPathsReachingTheControlPlane(t *testing.T) {
	w := newSandboxPathWorld(t)
	ws := filepath.Join(w.root, "ws")
	cases := []struct {
		name      string
		workspace string
		inputs    []string
		want      string
	}{
		{"own workspace", ws, []string{}, ""},
		{"workspace below HOME", filepath.Join(w.home, "jobs"), []string{}, ""},
		{"no workspace", "", []string{}, "workspace: required"},
		{"root", "/", []string{}, "refuse /"},
		{"relative", "ws", []string{}, "is not absolute"},
		{"missing", filepath.Join(w.root, "missing"), []string{}, "workspace:"},
		{"not a directory", w.file, []string{}, "is not a directory"},
		{"HOME", w.home, []string{}, "refuse HOME"},
		{"directory containing HOME", filepath.Dir(w.home), []string{}, "refuse HOME"},
		{"contains the event log", filepath.Dir(w.events), []string{}, "overlaps protected path"},
		{"inside the event log", filepath.Join(w.events, "sub"), []string{}, "overlaps protected path"},
		{"contains the signing key", w.keys, []string{}, "overlaps protected path"},
		{"contains the config", w.confDir, []string{}, "overlaps protected path"},
		{"contains the executable", w.exeDir, []string{}, "overlaps protected path"},
		{"input contains the signing key", ws, []string{w.keys}, "inputs/0: overlaps protected path"},
		{"input contains HOME", ws, []string{filepath.Dir(w.home)}, "inputs/0: refuse HOME"},
		{"input relative", ws, []string{"rel"}, "inputs/0:"},
	}
	for _, tc := range cases {
		err := w.load(t, tc.workspace, tc.inputs)
		if tc.want == "" && err != nil {
			t.Fatalf("%s: Load() = %v, want nil", tc.name, err)
		}
		if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Fatalf("%s: Load() = %v, want error containing %q", tc.name, err, tc.want)
		}
	}
}

func TestLoadSandboxFailsClosedWithoutExecutable(t *testing.T) {
	old := sandboxExecutable
	t.Cleanup(func() { sandboxExecutable = old })
	sandboxExecutable = func() (string, error) { return "", errors.New("no /proc") }
	workspace := t.TempDir()
	body := fmt.Sprintf(`{"jobs": [{"name": "daemon", "command": ["/bin/true"], "log_path": "/tmp/daemon.log", "sandbox": {"workspace": %q}}]}`, workspace)
	if _, err := Load(testContext(t), writeConfig(t, body)); err == nil || !strings.Contains(err.Error(), "sandbox needs the executable path") {
		t.Fatalf("Load(no executable path) = %v, want fail-closed error", err)
	}
}

func TestReleaseWatchPositive(t *testing.T) {
	body := `{
		"release_watch": {
			"state_dir": "/var/tmp/tribunus",
			"rate_limit_floor": 150,
			"max_actions_per_run": 25,
			"per_source_cap": 5,
			"routes": [{
				"name": "route-one",
				"sources": [
					{"github": "test-org/test-upstream"},
					{"huggingface_org": "TestHFOrg"}
				],
				"sinks": [
					{"github_issue": {"repo": "test-org/test-consumer", "labels": ["bug", "release"]}},
					{"ntfy": {"topic": "my-topic", "priority": 4}}
				]
			}]
		}
	}`
	cfg, err := Load(testContext(t), writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}
	rw := cfg.ReleaseWatch
	if rw.StateDir != "/var/tmp/tribunus" || rw.RateLimitFloor != 150 || rw.MaxActionsPerRun != 25 || rw.PerSourceCap != 5 {
		t.Fatalf("ReleaseWatch = %+v, want configured values", rw)
	}
	if len(rw.Routes) != 1 || rw.Routes[0].Name != "route-one" {
		t.Fatalf("Routes = %+v, want route-one", rw.Routes)
	}
	if len(rw.Routes[0].Sources) != 2 || len(rw.Routes[0].Sinks) != 2 {
		t.Fatalf("Route sources/sinks len mismatch: %+v", rw.Routes[0])
	}
}

func TestReleaseWatchNegative(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "route with empty sinks",
			body:    `{"release_watch": {"state_dir": "/tmp", "routes": [{"name": "route-a", "sources": [{"github": "owner/repo"}], "sinks": []}]}}`,
			wantErr: "sinks",
		},
		{
			name:    "github_issue without repo",
			body:    `{"release_watch": {"state_dir": "/tmp", "routes": [{"name": "route-a", "sources": [{"github": "owner/repo"}], "sinks": [{"github_issue": {"repo": ""}}]}]}}`,
			wantErr: "repo",
		},
		{
			name:    "repo not owner/name",
			body:    `{"release_watch": {"state_dir": "/tmp", "routes": [{"name": "route-a", "sources": [{"github": "owner/repo"}], "sinks": [{"github_issue": {"repo": "no-slash-here"}}]}]}}`,
			wantErr: "pattern",
		},
		{
			name:    "sink repo of dot segments",
			body:    `{"release_watch": {"state_dir": "/tmp", "routes": [{"name": "route-a", "sources": [{"github": "owner/repo"}], "sinks": [{"github_issue": {"repo": "../.."}}]}]}}`,
			wantErr: "invalid repository",
		},
		{
			name:    "source repo name ..",
			body:    `{"release_watch": {"state_dir": "/tmp", "routes": [{"name": "route-a", "sources": [{"github": "owner/.."}], "sinks": [{"github_issue": {"repo": "owner/consumer"}}]}]}}`,
			wantErr: "invalid repository",
		},
		{
			name:    "source owner .",
			body:    `{"release_watch": {"state_dir": "/tmp", "routes": [{"name": "route-a", "sources": [{"github": "./repo"}], "sinks": [{"github_issue": {"repo": "owner/consumer"}}]}]}}`,
			wantErr: "invalid repository",
		},
		{
			name:    "huggingface org ..",
			body:    `{"release_watch": {"state_dir": "/tmp", "routes": [{"name": "route-a", "sources": [{"huggingface_org": ".."}], "sinks": [{"github_issue": {"repo": "owner/consumer"}}]}]}}`,
			wantErr: "invalid org",
		},
		{
			name:    "source with zero kinds",
			body:    `{"release_watch": {"state_dir": "/tmp", "routes": [{"name": "route-a", "sources": [{}], "sinks": [{"github_issue": {"repo": "owner/consumer"}}]}]}}`,
			wantErr: "exactly one of github or huggingface_org",
		},
		{
			name:    "source with two kinds",
			body:    `{"release_watch": {"state_dir": "/tmp", "routes": [{"name": "route-a", "sources": [{"github": "owner/repo", "huggingface_org": "SomeOrg"}], "sinks": [{"github_issue": {"repo": "owner/consumer"}}]}]}}`,
			wantErr: "exactly one of github or huggingface_org",
		},
		{
			name: "duplicate route names",
			body: `{"release_watch": {"state_dir": "/tmp", "routes": [
				{"name": "route-dup", "sources": [{"github": "owner/repo"}], "sinks": [{"github_issue": {"repo": "owner/consumer"}}]},
				{"name": "route-dup", "sources": [{"github": "owner/other"}], "sinks": [{"github_issue": {"repo": "owner/consumer"}}]}
			]}}`,
			wantErr: "duplicate route name",
		},
		{
			name:    "state_dir relative when routes non-empty",
			body:    `{"release_watch": {"state_dir": "relative/state", "routes": [{"name": "route-a", "sources": [{"github": "owner/repo"}], "sinks": [{"github_issue": {"repo": "owner/consumer"}}]}]}}`,
			wantErr: "state_dir",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(testContext(t), writeConfig(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Load(%s) = %v, want error containing %q", tc.name, err, tc.wantErr)
			}
		})
	}
}

func TestReleaseWatchBoundaryNumericCaps(t *testing.T) {
	for _, maxActions := range []int{1, 100} {
		body := fmt.Sprintf(`{"release_watch": {"state_dir": "/tmp", "max_actions_per_run": %d, "routes": [{"name": "r", "sources": [{"github": "a/b"}], "sinks": [{"github_issue": {"repo": "c/d"}}]}]}}`, maxActions)
		if _, err := Load(testContext(t), writeConfig(t, body)); err != nil {
			t.Fatalf("Load(max_actions_per_run=%d) = %v, want nil", maxActions, err)
		}
	}
	for _, maxActions := range []int{0, 101} {
		body := fmt.Sprintf(`{"release_watch": {"state_dir": "/tmp", "max_actions_per_run": %d, "routes": [{"name": "r", "sources": [{"github": "a/b"}], "sinks": [{"github_issue": {"repo": "c/d"}}]}]}}`, maxActions)
		if _, err := Load(testContext(t), writeConfig(t, body)); err == nil {
			t.Fatalf("Load(max_actions_per_run=%d) = nil, want boundary error", maxActions)
		}
	}
	for _, perSource := range []int{1, 20} {
		body := fmt.Sprintf(`{"release_watch": {"state_dir": "/tmp", "per_source_cap": %d, "routes": [{"name": "r", "sources": [{"github": "a/b"}], "sinks": [{"github_issue": {"repo": "c/d"}}]}]}}`, perSource)
		if _, err := Load(testContext(t), writeConfig(t, body)); err != nil {
			t.Fatalf("Load(per_source_cap=%d) = %v, want nil", perSource, err)
		}
	}
	for _, perSource := range []int{0, 21} {
		body := fmt.Sprintf(`{"release_watch": {"state_dir": "/tmp", "per_source_cap": %d, "routes": [{"name": "r", "sources": [{"github": "a/b"}], "sinks": [{"github_issue": {"repo": "c/d"}}]}]}}`, perSource)
		if _, err := Load(testContext(t), writeConfig(t, body)); err == nil {
			t.Fatalf("Load(per_source_cap=%d) = nil, want boundary error", perSource)
		}
	}
}

func TestReleaseWatchBoundaryArrayCaps(t *testing.T) {
	labels8 := `["l1","l2","l3","l4","l5","l6","l7","l8"]`
	body8 := fmt.Sprintf(`{"release_watch": {"state_dir": "/tmp", "routes": [{"name": "r", "sources": [{"github": "a/b"}], "sinks": [{"github_issue": {"repo": "c/d", "labels": %s}}]}]}}`, labels8)
	if _, err := Load(testContext(t), writeConfig(t, body8)); err != nil {
		t.Fatalf("Load(labels=8) = %v, want nil", err)
	}
	labels9 := `["l1","l2","l3","l4","l5","l6","l7","l8","l9"]`
	body9 := fmt.Sprintf(`{"release_watch": {"state_dir": "/tmp", "routes": [{"name": "r", "sources": [{"github": "a/b"}], "sinks": [{"github_issue": {"repo": "c/d", "labels": %s}}]}]}}`, labels9)
	if _, err := Load(testContext(t), writeConfig(t, body9)); err == nil {
		t.Fatal("Load(labels=9) = nil error, want error")
	}
	emptyRoutes := `{"release_watch": {"state_dir": "", "routes": []}}`
	if _, err := Load(testContext(t), writeConfig(t, emptyRoutes)); err != nil {
		t.Fatalf("Load(empty routes and empty state_dir) = %v, want nil", err)
	}
}
