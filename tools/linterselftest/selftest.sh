#!/usr/bin/env bash
# Plants findings in a temporary directory and requires the named scanner to fail on each,
# so the tree scan that follows it in ci.yml is proven to catch them.
# Usage: selftest.sh semgrep   (run from the repository root; reads .config/semgrep/hiss.yml)
set -euo pipefail
tool="${1:?usage: selftest.sh semgrep}"
plant="$(mktemp -d)"
trap 'rm -r "$plant"' EXIT
log="$plant/scan.log"

case "$tool" in
  semgrep)
    cat > "$plant/planted.go" <<'GO'
package planted

import (
	"context"
	"net"
	"net/http"
	"os/exec"
)

func planted(ctx context.Context) {
	_ = exec.Command("true").Run()
	_, _ = net.Dial("tcp", "127.0.0.1:1")
	_, _ = http.Get("http://127.0.0.1:1")
	_ = context.TODO()
	_ = exec.CommandContext(ctx, "sh", "-c", "true").Run()
}
GO
    printf '%s\n' '#!/usr/bin/env bash' 'cmd="ls"' "eval \"\$cmd\"" > "$plant/planted.sh"
    expected=(hiss-02-go-exec-without-context hiss-02-go-dial-without-context
      hiss-02-go-http-without-context hiss-02-go-context-todo hiss-08-go-shell-command
      hiss-08-bash-eval)
    set +e
    semgrep scan --config .config/semgrep/hiss.yml --error --metrics=off --disable-version-check \
      --json "$plant" > "$log" 2>&1
    rc=$?
    set -e
    ;;
  *)
    echo "unknown tool: $tool" >&2
    exit 2
    ;;
esac

if [[ "$rc" -ne 1 ]]; then
  echo "$tool: planted findings gave exit $rc, want 1" >&2
  cat "$log" >&2
  exit 1
fi
missing=0
for id in "${expected[@]}"; do
  if ! grep -q -- "$id" "$log"; then
    echo "$tool: planted finding $id not reported" >&2
    missing=1
  fi
done
if [[ "$missing" -ne 0 ]]; then
  exit 1
fi
echo "$tool: ${#expected[@]} planted findings reported, exit 1 as required"
