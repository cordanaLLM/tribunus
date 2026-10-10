package config

import (
	"context"
	"encoding/json"
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
  "jobs": [{"name": "daily-catalog-sync", "schedule": "0 2 * * *", "router_alias": "cordana/auto"}],
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
		{"duplicate job names", `{"jobs": [
  {"name": "repeat-job", "schedule": "0 1 * * *", "router_alias": "cordana/auto"},
  {"name": "repeat-job", "schedule": "0 2 * * *", "router_alias": "cordana/auto"}
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
		"jobs":    declarations("job", "schedule", jobs),
		"watches": declarations("watch", "trigger", watches),
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("Marshal(declarations) = %v, want nil", err)
	}
	return string(body)
}

func declarations(prefix string, field string, count int) []map[string]string {
	out := make([]map[string]string, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, map[string]string{
			"name":         prefix + "-" + jsonInt(i),
			field:          "event",
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
