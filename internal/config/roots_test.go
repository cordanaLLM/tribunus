package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// plantedLogins are login files as shells, forges and coding clients keep them below HOME.
var plantedLogins = []string{
	".ssh/id_ed25519", ".netrc", ".git-credentials", ".config/gh/hosts.yml", ".docker/config.json",
	".aws/credentials", ".claude/.credentials.json", ".claude.json", ".codex/auth.json",
	".gemini/oauth_creds.json", ".config/opencode/auth.json", ".local/share/opencode/auth.json",
	".local/share/keyrings/login.keyring",
}

// plantLogins writes every planted login file below the world's HOME, and a directory beside
// them under .config and .local/share that holds no login.
func (w sandboxPathWorld) plantLogins(t *testing.T) {
	t.Helper()
	for _, rel := range append([]string{".config/tool/settings.json", ".local/share/tool/data"}, plantedLogins...) {
		path := filepath.Join(w.home, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) = %v, want nil", path, err)
		}
		if err := os.WriteFile(path, []byte("planted\n"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) = %v, want nil", path, err)
		}
	}
}

// runtimeDir is the user's runtime directory below /run/user, where session buses and agent
// sockets live. A host without one gets a stand-in outside every root.
func (w sandboxPathWorld) runtimeDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	dir = filepath.Join(w.root, "run", "user")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll(%s) = %v, want nil", dir, err)
	}
	return dir
}

type rootCase struct {
	name   string
	roots  []string
	ws     string
	inputs []string
	want   string
}

func runRootCases(t *testing.T, w sandboxPathWorld, cases []rootCase) {
	t.Helper()
	for _, tc := range cases {
		err := w.loadWithRoots(t, tc.roots, tc.ws, tc.inputs)
		if tc.want == "" && err != nil {
			t.Errorf("%s: Load() = %v, want nil", tc.name, err)
		}
		if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: Load() = %v, want an error holding %q", tc.name, err, tc.want)
		}
	}
}

// The workspace and every input lie in a declared root or the job is refused: lying outside
// HOME is no pass, and with no root nothing runs sandboxed.
func TestLoadSandboxPathsLieInADeclaredRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sandbox does not run on Windows")
	}
	w := newSandboxPathWorld(t)
	ws := filepath.Join(w.root, "ws")
	outside := filepath.Join(w.root, "outside")
	sibling := filepath.Join(w.root, "ws-other")
	for _, dir := range []string{outside, sibling, filepath.Join(ws, "in")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) = %v, want nil", dir, err)
		}
	}
	runtimeDir := w.runtimeDir(t)
	runRootCases(t, w, []rootCase{
		{"workspace in its root", []string{ws}, ws, []string{}, ""},
		{"input in the workspace's root", []string{ws}, ws, []string{filepath.Join(ws, "in")}, ""},
		{"no roots", []string{}, ws, []string{}, "workspace: " + ws + " lies in no sandbox_input_roots entry"},
		{"input outside HOME and every root", []string{ws}, ws, []string{outside}, "inputs/0: " + outside + " lies in no sandbox_input_roots entry"},
		{"input in the user's runtime directory", []string{ws}, ws, []string{runtimeDir}, "inputs/0:"},
		{"input that shares only a name prefix with a root", []string{ws}, ws, []string{sibling}, "lies in no sandbox_input_roots entry"},
		{"input that climbs out of its root", []string{ws}, ws, []string{filepath.Join(ws, "in", "..", "..", "outside")}, "lies in no sandbox_input_roots entry"},
		{"root /", []string{"/"}, ws, []string{}, "sandbox_input_roots/0: refuse /"},
		{"root HOME", []string{ws, w.home}, ws, []string{}, "sandbox_input_roots/1: refuse " + w.home},
		{"root containing HOME", []string{ws, filepath.Dir(w.home)}, ws, []string{}, "contains HOME"},
		{"relative root", []string{"ws"}, ws, []string{}, "sandbox_input_roots/0: ws is not absolute"},
		{"missing root", []string{filepath.Join(w.root, "missing")}, ws, []string{}, "sandbox_input_roots/0:"},
	})
}

// Below HOME a login location stays shut behind a root that only contains it; a root that
// names one location exactly opens that one and no other.
func TestLoadSandboxInputsRefuseLoginLocations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sandbox does not run on Windows")
	}
	w := newSandboxPathWorld(t)
	w.plantLogins(t)
	ws := filepath.Join(w.root, "ws")
	home := func(rel string) string { return filepath.Join(w.home, filepath.FromSlash(rel)) }
	ownClient := []string{ws, home(".claude")}
	cases := []rootCase{
		{"own client folder named as a root", ownClient, ws, []string{home(".claude")}, ""},
		{"a file in the own client folder", ownClient, ws, []string{home(".claude/.credentials.json")}, ""},
		{"a non-login folder below a broad root", []string{ws, home(".config")}, ws, []string{home(".config/tool")}, ""},
		{"the forge login below a broad root", []string{ws, home(".config")}, ws, []string{home(".config/gh")}, "overlaps the login location " + home(".config/gh")},
		{"a broad folder holding a login", []string{ws, home(".config")}, ws, []string{home(".config")}, "overlaps the login location"},
		{"a workspace on a login location", []string{home(".config")}, home(".config/gh"), []string{}, "workspace: " + home(".config/gh") + " overlaps the login location"},
		{"keyring below a broad root", []string{ws, home(".local")}, ws, []string{home(".local/share/keyrings")}, "overlaps the login location"},
	}
	// Every planted login outside the own client folder stays shut to that root.
	for _, rel := range plantedLogins {
		if strings.HasPrefix(rel, ".claude/") {
			continue
		}
		cases = append(cases, rootCase{"planted " + rel, ownClient, ws, []string{home(rel)}, "inputs/0:"})
	}
	runRootCases(t, w, cases)
}

// A symlink is followed before any check: a link inside a root that leads to a login
// location is that login location.
func TestLoadSandboxInputsFollowSymlinksToLogins(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sandbox does not run on Windows")
	}
	w := newSandboxPathWorld(t)
	w.plantLogins(t)
	ws := filepath.Join(w.root, "ws")
	claude := filepath.Join(w.home, ".claude")
	links := map[string]string{
		filepath.Join(ws, "keys"):        filepath.Join(w.home, ".ssh"),
		filepath.Join(claude, "keys"):    filepath.Join(w.home, ".ssh"),
		filepath.Join(ws, "credentials"): filepath.Join(w.home, ".codex", "auth.json"),
	}
	cases := []rootCase{}
	for link, target := range links {
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("Symlink(%s) = %v, want nil", link, err)
		}
		cases = append(cases, rootCase{"link " + link, []string{ws, claude}, ws, []string{link}, "inputs/0: " + target})
	}
	runRootCases(t, w, cases)
}

// The bound on roots holds at the bound and refuses one more, in the schema and in the
// resolution behind it.
func TestSandboxInputRootsBound(t *testing.T) {
	w := newSandboxPathWorld(t)
	ws := filepath.Join(w.root, "ws")
	roots := make([]string, MaxSandboxInputRoots)
	for i := range roots {
		roots[i] = ws
	}
	if err := w.loadWithRoots(t, roots, ws, []string{}); err != nil {
		t.Fatalf("Load(%d roots) = %v, want nil", len(roots), err)
	}
	if err := w.loadWithRoots(t, append(roots, ws), ws, []string{}); err == nil {
		t.Fatalf("Load(%d roots) = nil, want refused", len(roots)+1)
	}
	if _, err := resolveInputRoots(append(roots, ws), w.home); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("resolveInputRoots(%d roots) = %v, want the bound", len(roots)+1, err)
	}
}
