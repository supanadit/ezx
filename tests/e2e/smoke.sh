#!/usr/bin/env bash
# tests/e2e/smoke.sh — container end-to-end smoke test for ezx.
#
# ezx's headline promise is being a drop-in container entrypoint: PID 1 duties
# (zombie reaping, signal forwarding, graceful drain) plus a supervision graph.
# Unit tests cannot exercise any of that, so this script runs ezx for real
# inside a container and asserts:
#
#   1. supervised mode keeps ezx as PID 1 and reaps orphaned children
#   2. the explicit execDefault:false opt-out also keeps ezx as PID 1
#   3. a lone node without supervision opts into exec (ezx disappears) and warns
#   4. an exec node replaces PID 1 (the "init DAG -> exec main" pattern)
#   5. SIGTERM reaches the supervised app and ezx drains gracefully
#   6. no port is bound at all unless EZX_HEALTH_ADDR is set
#
# The binary and entry script are copied into the container rather than
# bind-mounted: rootless podman with a user namespace (or a noexec mount)
# SIGSEGVs an exec'd bind-mounted host binary, which produces a misleading
# failure instead of a real result.
#
# Usage: tests/e2e/smoke.sh [path-to-ezx-binary]
# The binary defaults to a freshly built ./ezx, built with CGO_ENABLED=0 so it
# runs in a musl base image.
set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

# Pick a container engine.
engine=""
for candidate in podman docker; do
	if command -v "$candidate" >/dev/null 2>&1; then
		engine="$candidate"
		break
	fi
done
if [ -z "$engine" ]; then
	echo "SKIP: neither podman nor docker found" >&2
	exit 0
fi

image="${EZX_E2E_IMAGE:-docker.io/library/alpine:3.20}"
if ! "$engine" image exists "$image" >/dev/null 2>&1; then
	echo "Pulling $image with $engine..."
	if ! "$engine" pull "$image" >/dev/null 2>&1; then
		echo "SKIP: cannot pull $image" >&2
		exit 0
	fi
fi

# Build the binary under test unless one was passed in.
if [ "${1:-}" != "" ]; then
	binary="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
else
	binary="$workdir/ezx"
	echo "Building ezx (CGO_ENABLED=0)..."
	(cd "$repo_root" && CGO_ENABLED=0 go build -o "$binary" ./app) || exit 1
fi

failures=0
pass() { printf '  ok   %s\n' "$1"; }
fail() {
	printf '  FAIL %s\n' "$1"
	failures=$((failures + 1))
}

# create_container <entry-script> [create args...] -> container id
# Creates the container stopped, installs the binary and the entry script, and
# leaves it ready to start.
create_container() {
	local script="$1"
	shift
	local cid
	cid="$("$engine" create "$@" "$image" /usr/local/bin/ezx bootstrap /entry.js 2>/dev/null)" || return 1
	"$engine" cp "$binary" "$cid:/usr/local/bin/ezx" >/dev/null 2>&1 || {
		"$engine" rm -f "$cid" >/dev/null 2>&1
		return 1
	}
	"$engine" cp "$script" "$cid:/entry.js" >/dev/null 2>&1
	printf '%s' "$cid"
}

# run_attached <entry-script> <timeout-seconds> :: run to completion, print output.
run_attached() {
	local script="$1" secs="$2" cid out
	cid="$(create_container "$script")" || {
		printf 'container create failed'
		return
	}
	out="$(timeout "$secs" "$engine" start -a "$cid" 2>&1)"
	"$engine" rm -f "$cid" >/dev/null 2>&1
	printf '%s' "$out"
}

# ---------------------------------------------------------------------------
# 1 + 2. Supervised mode keeps ezx as PID 1, and orphans get reaped.
# ---------------------------------------------------------------------------
for variant in restart optout; do
	if [ "$variant" = restart ]; then
		label="supervised via restart policy"
		policy='restart: { mode: "on-failure", maxRetries: 0, backoff: 1e9 },'
		exec_default='true'
	else
		label="supervised via execDefault:false"
		policy=''
		exec_default='false'
	fi

	cat >"$workdir/sup.js" <<EOF
const { chain } = require("ezx");
chain.run({
  execDefault: ${exec_default},
  nodes: [{
    name: "app",
    // Leaves a short-lived orphan behind: unreaped, it stays a zombie.
    process: { binaryPath: "/bin/sh", arguments: ["-c", "(sleep 0.2 &) ; sleep 5"] },
    $policy
  }],
});
EOF

	cid="$(create_container "$workdir/sup.js")" || {
		fail "$label: container create failed"
		continue
	}
	"$engine" start "$cid" >/dev/null 2>&1
	"$engine" exec "$cid" sh -c 'sleep 1.5' >/dev/null 2>&1 || true
	report="$("$engine" exec "$cid" sh -c '
		cat /proc/1/comm 2>/dev/null || echo "?"
		awk "\$3 == \"Z\"" /proc/[0-9]*/stat 2>/dev/null | wc -l
	' 2>/dev/null | tr "\n" " ")"
	"$engine" rm -f "$cid" >/dev/null 2>&1 || true

	p1="$(printf '%s' "$report" | awk '{print $1}')"
	zombies="$(printf '%s' "$report" | awk '{print $2}')"
	if [ "$p1" = "ezx" ]; then
		pass "$label: ezx stayed PID 1"
	else
		fail "$label: PID 1 = '$p1', want 'ezx'"
	fi
	if [ "${zombies:-1}" = "0" ]; then
		pass "$label: orphaned children reaped (0 zombies)"
	else
		fail "$label: $zombies zombie(s) present, want 0"
	fi
done

# ---------------------------------------------------------------------------
# 3. A lone node with no restart/health/scheduler opts into exec: ezx warns and
#    hands PID 1 to the app.
# ---------------------------------------------------------------------------
cat >"$workdir/lone.js" <<'EOF'
const { chain } = require("ezx");
chain.run({ nodes: [{ name: "app", process: { binaryPath: "/bin/sleep", arguments: ["3"] } }] });
EOF

out="$(run_attached "$workdir/lone.js" 30)"
if printf '%s' "$out" | grep -qi "supervision disabled"; then
	pass "implicit exec warns that supervision is disabled"
else
	fail "implicit exec did not warn (got: $(printf '%s' "$out" | tail -2 | tr '\n' ' '))"
fi
if printf '%s' "$out" | grep -q "exec'ing to become PID 1"; then
	pass "implicit exec hands PID 1 to the app"
else
	fail "implicit exec log line missing"
fi

# ---------------------------------------------------------------------------
# 4. init-DAG -> exec main: oneshot deps run to completion, then the app execs.
# ---------------------------------------------------------------------------
cat >"$workdir/initdag.js" <<'EOF'
const { chain } = require("ezx");
chain.run({
  nodes: [
    { name: "init", oneshot: true, process: { binaryPath: "/bin/sh", arguments: ["-c", "echo initialized > /tmp/init-done"] } },
    { name: "main", exec: true, dependsOn: ["init"], process: { binaryPath: "/bin/cat", arguments: ["/tmp/init-done"] } },
  ],
});
EOF

out="$(run_attached "$workdir/initdag.js" 30)"
if printf '%s' "$out" | grep -q "initialized"; then
	pass "init-DAG oneshot ran before the exec'd main process"
else
	fail "init-DAG output missing 'initialized' (got: $(printf '%s' "$out" | tail -3 | tr '\n' ' '))"
fi

# ---------------------------------------------------------------------------
# 5. SIGTERM is forwarded and the supervised app drains gracefully.
# ---------------------------------------------------------------------------
cat >"$workdir/sig.js" <<'EOF'
const { chain } = require("ezx");
chain.run({
  nodes: [{
    name: "app",
    // Foreground loop: the trap runs as soon as SIGTERM lands, with no race
    // against a `wait` returning. The shell prints its own readiness marker
    // once the trap is installed, so the harness never signals into a window
    // where TERM would kill the shell before the trap exists.
    process: { binaryPath: "/bin/sh", arguments: ["-c", "trap 'echo GOT_SIGTERM; exit 0' TERM; echo 'ready for SIGTERM'; while true; do sleep 0.2; done"] },
    restart: { mode: "on-failure" },
    forwardSignals: ["SIGTERM"],
  }],
});
EOF

cid="$(create_container "$workdir/sig.js")"
if [ -z "$cid" ]; then
	fail "SIGTERM: container create failed"
else
	"$engine" start "$cid" >/dev/null 2>&1
	# Signal only once the app has installed its TERM trap. Fixed sleeps are a
	# race: a TERM delivered before `trap` runs kills the shell with no
	# GOT_SIGTERM, which would flake the assertion below instead of exercising
	# it. Wait for the app's own readiness line ("ready for SIGTERM").
	for _ in $(seq 1 40); do
		"$engine" logs "$cid" 2>&1 | grep -q "ready for SIGTERM" && break
		sleep 0.25
	done
	"$engine" kill -s TERM "$cid" >/dev/null 2>&1 || true

	# ezx must drain the app and exit, stopping the container without a kill.
	for _ in $(seq 1 20); do
		[ "$("$engine" inspect -f '{{.State.Running}}' "$cid" 2>/dev/null)" = "false" ] && break
		sleep 0.5
	done
	running="$("$engine" inspect -f '{{.State.Running}}' "$cid" 2>/dev/null || echo false)"
	exitcode="$("$engine" inspect -f '{{.State.ExitCode}}' "$cid" 2>/dev/null || echo '?')"
	logs="$("$engine" logs "$cid" 2>&1 || true)"
	"$engine" rm -f "$cid" >/dev/null 2>&1 || true

	if [ "$running" = "false" ]; then
		pass "SIGTERM drained the chain: ezx exited and stopped the container"
	else
		fail "ezx did not exit after SIGTERM (container still running)"
	fi
	if printf '%s' "$logs" | grep -q "GOT_SIGTERM"; then
		pass "SIGTERM reached the supervised app (forwarded to its process group)"
	else
		fail "app never saw SIGTERM (logs: $(printf '%s' "$logs" | tail -3 | tr '\n' ' '))"
	fi
	# A clean shutdown is a success, not a failure: the app exited 0 on the
	# signal, so ezx must report completion rather than an error. (It used to
	# exit 1 with "run script: context cancelled", because the scripting
	# engine's context interrupt unwound the still-draining chain.)
	if [ "$exitcode" = "0" ]; then
		pass "graceful SIGTERM exits 0 (clean shutdown reported as success)"
	else
		fail "graceful SIGTERM exited $exitcode, want 0 (logs: $(printf '%s' "$logs" | tail -2 | tr '\n' ' '))"
	fi
fi

# ---------------------------------------------------------------------------
# 6. No port is bound unless EZX_HEALTH_ADDR is set. With the default
#    environment the container must have zero listening sockets: the app is a
#    plain sleep, and ezx must not open one either. /proc/net/tcp state 0A is
#    TCP_LISTEN regardless of address family or port.
# ---------------------------------------------------------------------------
cid="$(create_container "$workdir/sup.js")"
if [ -z "$cid" ]; then
	fail "health port: container create failed"
else
	"$engine" start "$cid" >/dev/null 2>&1
	"$engine" exec "$cid" sh -c 'sleep 1.5' >/dev/null 2>&1 || true
	listeners="$("$engine" exec "$cid" sh -c \
		'awk "\$4 == \"0A\"" /proc/net/tcp /proc/net/tcp6 2>/dev/null | wc -l' 2>/dev/null || echo "?")"
	"$engine" rm -f "$cid" >/dev/null 2>&1 || true
	if [ "${listeners:-1}" = "0" ]; then
		pass "no listener by default (EZX_HEALTH_ADDR unset)"
	else
		fail "$listeners listening socket(s) present without EZX_HEALTH_ADDR"
	fi
fi

echo
if [ "$failures" -eq 0 ]; then
	echo "container smoke test: all checks passed ($engine, $image)"
	exit 0
fi
echo "container smoke test: $failures check(s) failed ($engine, $image)" >&2
exit 1
