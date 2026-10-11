//go:build linux

package supervisor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tekmora/epitrop/internal/config"
)

const (
	// rt03RanMark starts the script of every fixture with network=proxy. A refusal counts
	// only when the job's command ran: a relay that did not start refuses everything too.
	rt03RanMark = `printf 'ran\n' > ran.txt; `

	// rt03ProxyConnectScript asks the proxy the sandbox names for a tunnel to $TARGET and
	// succeeds on the answer 200 alone.
	rt03ProxyConnectScript = `proxy=${HTTPS_PROXY#http://}
exec 3<>"/dev/tcp/${proxy%:*}/${proxy##*:}" || exit 2
printf 'CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n' "$TARGET" "$TARGET" >&3
IFS= read -r -t 10 line <&3 || exit 3
printf '%s\n' "$line"
case "$line" in "HTTP/1.1 200 "*) exit 0 ;; esac
exit 1`
)

// rt03Proxy is the proxy of a fixture with network=proxy, started the way the shim starts
// it. The zero value belongs to every other fixture.
type rt03Proxy struct {
	job jobProxy
	log *os.File
}

func startRT03Proxy(t *testing.T, sandbox config.SandboxConfig) rt03Proxy {
	t.Helper()
	if sandbox.Network != "proxy" {
		return rt03Proxy{}
	}
	log, err := os.Create(filepath.Join(t.TempDir(), "proxy.log")) // #nosec G304 -- test-controlled path.
	if err != nil {
		t.Fatalf("Create(proxy log) = %v, want nil", err)
	}
	job, err := startJobProxy(t.Context(), shimConfig{Sandbox: sandbox}, log)
	if err != nil {
		t.Fatalf("startJobProxy(%v) = %v, want nil", sandbox.Destinations, err)
	}
	return rt03Proxy{job: job, log: log}
}

// judge ends the proxy and checks that the fixture's outcome is the proxy's own: the job's
// command ran, and the proxy refused exactly the attempts that went through it and were to
// be refused. A direct connection never reaches the proxy.
func (p rt03Proxy) judge(t *testing.T, fixture rt03Fixture, world rt03World) {
	t.Helper()
	if p.log == nil {
		return
	}
	if err := p.job.stop(); err != nil {
		t.Errorf("stop the proxy of %s = %v, want nil", fixture.ID, err)
	}
	body, err := os.ReadFile(p.log.Name())
	if err != nil {
		t.Fatalf("ReadFile(proxy log) = %v, want nil", err)
	}
	t.Logf("proxy log of %s: %q", fixture.ID, body)
	if err = p.log.Close(); err != nil {
		t.Errorf("Close(proxy log) = %v, want nil", err)
	}
	if !fileHas(t, filepath.Join(world.workspace, "ran.txt"), "ran\n") {
		t.Fatalf("%s: the job's command did not run behind the relay, so its outcome shows nothing", fixture.ID)
	}
	want := 0
	if fixture.Attempt.Kind == "proxy_connect" && fixture.Direction == "violation" {
		want = 1
	}
	if got := p.job.server.Refused(); got != want {
		t.Fatalf("%s: the proxy refused %d requests, want %d", fixture.ID, got, want)
	}
}
