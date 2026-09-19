#!/usr/bin/env bash
#
# Contract tests for the warpbench launchers, run against a fake GitHub release
# served over localhost. This is the Phase 2 gate: it proves the launcher
# downloads, verifies, caches, refuses bad checksums, and hands off arguments
# and exit codes correctly -- without needing a real release to exist.
#
# Usage: tests/launcher_test.sh
# Exit:  0 all tests passed, 1 otherwise.

set -u

REPO_ROOT=$(cd "$(dirname "$0")/.." && pwd)
LAUNCHER_SH="$REPO_ROOT/warpbench.sh"
LAUNCHER_PS="$REPO_ROOT/warpbench.ps1"

PASS=0
FAIL=0
SKIP=0

pass() { PASS=$((PASS + 1)); printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() {
	FAIL=$((FAIL + 1))
	printf '  \033[31mFAIL\033[0m %s\n' "$1"
	[ -n "${2:-}" ] && printf '       %s\n' "$2"
}
skip() { SKIP=$((SKIP + 1)); printf '  \033[33mSKIP\033[0m %s\n' "$1"; }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }

# ---------------------------------------------------------------------------
# An exec-capable scratch directory.
#
# /tmp is mounted noexec on some hosts (including the container this was
# developed in), which makes every "run the fake binary" test fail with a
# confusing EACCES. Probe first and fall back to somewhere that works.
# ---------------------------------------------------------------------------
pick_tmpdir() {
	local candidate
	for candidate in "${WARPBENCH_TEST_TMPDIR:-}" "${TMPDIR:-}" /tmp "$HOME/.cache"; do
		[ -n "$candidate" ] || continue
		[ -d "$candidate" ] || continue
		local probe="$candidate/.warpbench-exec-probe.$$"
		printf '#!/bin/sh\nexit 0\n' >"$probe" 2>/dev/null || continue
		chmod +x "$probe" 2>/dev/null || continue
		if "$probe" 2>/dev/null; then
			rm -f "$probe"
			printf '%s' "$candidate"
			return 0
		fi
		rm -f "$probe"
	done
	return 1
}

TMPROOT=$(pick_tmpdir) || {
	printf 'no exec-capable temporary directory found; set WARPBENCH_TEST_TMPDIR\n' >&2
	exit 1
}
printf 'scratch: %s (exec-capable)\n' "$TMPROOT"

WORK=$(mktemp -d "$TMPROOT/warpbench-test.XXXXXX")
PORT=$((20000 + (RANDOM % 20000)))
SERVER_PID=""

# shellcheck disable=SC2317  # invoked indirectly, via the EXIT trap on the next line
cleanup() {
	[ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null
	rm -rf "$WORK"
}
trap cleanup EXIT

hash_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

# ---------------------------------------------------------------------------
# Fake release tree
# ---------------------------------------------------------------------------

case "$(uname -s)" in
Linux) TEST_OS=linux ;;
Darwin) TEST_OS=darwin ;;
*)
	printf 'unsupported test host: %s\n' "$(uname -s)" >&2
	exit 1
	;;
esac
case "$(uname -m)" in
x86_64 | amd64) TEST_ARCH=amd64 ;;
aarch64 | arm64) TEST_ARCH=arm64 ;;
*)
	printf 'unsupported test arch: %s\n' "$(uname -m)" >&2
	exit 1
	;;
esac
ASSET="warpbench_${TEST_OS}_${TEST_ARCH}"

RELEASE_ROOT="$WORK/release"

# make_release <tag-dir> <exit-code> [hash-mode]
#   hash-mode: good (default) | bad | missing
make_release() {
	local dir="$RELEASE_ROOT/$1" code="$2" mode="${3:-good}"

	mkdir -p "$dir"
	# The "binary" records the arguments it was handed so the test can assert on
	# argument pass-through, then exits with a known status.
	cat >"$dir/$ASSET" <<EOF
#!/bin/sh
echo "FAKE-ARGS: \$*"
echo "FAKE-STDIN-TTY: \$( [ -t 0 ] && echo yes || echo no )"
exit $code
EOF
	chmod +x "$dir/$ASSET"

	local hash
	hash=$(hash_of "$dir/$ASSET")
	case "$mode" in
	good) printf '%s  %s\n' "$hash" "$ASSET" >"$dir/SHA256SUMS" ;;
	bad) printf '%s  %s\n' "0000000000000000000000000000000000000000000000000000000000000000" "$ASSET" >"$dir/SHA256SUMS" ;;
	missing) printf '%s  %s\n' "$hash" "some-other-asset" >"$dir/SHA256SUMS" ;;
	esac
}

# The PowerShell launcher asks for a windows asset even when it runs on Linux
# (it is a Windows installer), so the fake release must contain one too.
# add_windows_asset <tag-dir> [good|bad]
# Appends to SHA256SUMS rather than rebuilding it, so a bad-hash fixture keeps
# its deliberately wrong digest.
add_windows_asset() {
	local dir="$RELEASE_ROOT/$1" mode="${2:-good}" exe="warpbench_windows_${TEST_ARCH}.exe"
	cat >"$dir/$exe" <<'EOF'
#!/bin/sh
echo "FAKE-WIN-ARGS: $*"
exit 0
EOF
	chmod +x "$dir/$exe"
	if [ "$mode" = bad ]; then
		printf '%s  %s\n' "0000000000000000000000000000000000000000000000000000000000000000" "$exe" >>"$dir/SHA256SUMS"
	else
		printf '%s  %s\n' "$(hash_of "$dir/$exe")" "$exe" >>"$dir/SHA256SUMS"
	fi
}

make_release "latest/download" 0 good
make_release "download/v0.1.0" 0 good
add_windows_asset "download/v0.1.0"
add_windows_asset "latest/download"
make_release "download/badhash" 0 bad
add_windows_asset "download/badhash" bad
make_release "download/nohash" 0 missing
make_release "download/v7.7.7" 7 good
mkdir -p "$RELEASE_ROOT/download/empty"

if ! command -v python3 >/dev/null 2>&1; then
	printf 'python3 is required to serve the fake release\n' >&2
	exit 1
fi
python3 -m http.server "$PORT" --directory "$RELEASE_ROOT" >/dev/null 2>&1 &
SERVER_PID=$!

BASE_URL="http://127.0.0.1:$PORT"
i=0
while [ "$i" -lt 60 ]; do
	curl -fsS -o /dev/null "$BASE_URL/download/v0.1.0/SHA256SUMS" 2>/dev/null && break
	i=$((i + 1))
	sleep 0.1
done

# run_launcher <shell> <cache-dir> [KEY=VALUE...]
#
# Output and status are written to files rather than echoed, because a command
# substitution runs in a subshell: any LAST_STATUS set inside it would be lost
# in the caller.
LAST_OUT="$WORK/last.out"
LAST_STATUS_FILE="$WORK/last.status"

run_launcher() {
	local shell_bin="$1" cache="$2"
	shift 2
	env WARPBENCH_BASE_URL="$BASE_URL" WARPBENCH_CACHE_DIR="$cache" \
		"$@" "$shell_bin" "$LAUNCHER_SH" --probe-flag >"$LAST_OUT" 2>&1 </dev/null
	printf '%s' "$?" >"$LAST_STATUS_FILE"
}

launcher_out() { cat "$LAST_OUT"; }
launcher_status() { cat "$LAST_STATUS_FILE"; }

# ---------------------------------------------------------------------------
printf '\n\033[1mwarpbench launcher contract tests\033[0m\n'
printf 'host: %s/%s   asset: %s\n' "$TEST_OS" "$TEST_ARCH" "$ASSET"
printf 'fake release: %s\n' "$BASE_URL"

section "1. download, verify, run, argument pass-through"
for shell_bin in sh dash bash; do
	if ! command -v "$shell_bin" >/dev/null 2>&1; then
		skip "$shell_bin: not installed"
		continue
	fi
	run_launcher "$shell_bin" "$WORK/cache-$shell_bin" WARPBENCH_VERSION=v0.1.0
	out=$(launcher_out)
	if printf '%s' "$out" | grep -q 'FAKE-ARGS:.*--probe-flag'; then
		pass "$shell_bin: downloaded, verified and forwarded --probe-flag"
	else
		fail "$shell_bin: argument was not forwarded" "$(printf '%s' "$out" | tr '\n' '|')"
	fi
	if [ "$(launcher_status)" -eq 0 ]; then
		pass "$shell_bin: exit status 0"
	else
		fail "$shell_bin: exit status $(launcher_status), want 0"
	fi
done

section "2. checksum verification"
run_launcher bash "$WORK/cache-bad" WARPBENCH_VERSION=badhash
out=$(launcher_out)
if printf '%s' "$out" | grep -qi 'checksum mismatch'; then
	pass "rejects a mismatched checksum"
else
	fail "did not report a checksum mismatch" "$(printf '%s' "$out" | tr '\n' '|')"
fi
if [ "$(launcher_status)" -ne 0 ]; then
	pass "exits non-zero on checksum mismatch (status $(launcher_status))"
else
	fail "exited 0 despite a checksum mismatch"
fi
if [ ! -f "$WORK/cache-bad/badhash/$ASSET" ]; then
	pass "removed the unverified binary from the cache"
else
	fail "left the unverified binary in the cache"
fi

run_launcher bash "$WORK/cache-nohash" WARPBENCH_VERSION=nohash
out=$(launcher_out)
if printf '%s' "$out" | grep -qi 'no checksum entry'; then
	pass "refuses when SHA256SUMS has no entry for the asset"
else
	fail "did not report a missing checksum entry" "$(printf '%s' "$out" | tr '\n' '|')"
fi

section "3. missing release"
run_launcher bash "$WORK/cache-empty" WARPBENCH_VERSION=empty
out=$(launcher_out)
if printf '%s' "$out" | grep -qi 'no release found\|download failed'; then
	pass "reports a clear error when the release is absent"
else
	fail "unclear error for a missing release" "$(printf '%s' "$out" | tr '\n' '|')"
fi
if [ "$(launcher_status)" -ne 0 ]; then
	pass "exits non-zero when the release is absent"
else
	fail "exited 0 when the release was absent"
fi

section "4. caching (server still up)"
cache="$WORK/cache-reuse"
run_launcher bash "$cache" WARPBENCH_VERSION=v0.1.0
run_launcher bash "$cache" WARPBENCH_VERSION=v0.1.0
out=$(launcher_out)
if printf '%s' "$out" | grep -q 'using cached'; then
	pass "second run uses the cached binary"
else
	fail "second run did not use the cache" "$(printf '%s' "$out" | tr '\n' '|')"
fi

section "5. exit-code propagation"
run_launcher bash "$WORK/cache-exit" WARPBENCH_VERSION=v7.7.7
out=$(launcher_out)
if [ "$(launcher_status)" -eq 7 ]; then
	pass "propagates the binary's exit status (7)"
else
	fail "exit status $(launcher_status), want 7"
fi

section "6. no controlling terminal"
if command -v setsid >/dev/null 2>&1; then
	out=$(setsid env WARPBENCH_BASE_URL="$BASE_URL" WARPBENCH_CACHE_DIR="$WORK/cache-notty" \
		WARPBENCH_VERSION=v0.1.0 sh "$LAUNCHER_SH" 2>&1 </dev/null || true)
	if printf '%s' "$out" | grep -q -- '--no-tty'; then
		pass "passes --no-tty when there is no controlling terminal"
	else
		fail "did not pass --no-tty without a terminal" "$(printf '%s' "$out" | tr '\n' '|')"
	fi
else
	skip "setsid not available; cannot force the no-terminal case"
fi

section "7. default (unpinned) version resolves via /latest/download"
run_launcher bash "$WORK/cache-latest"
out=$(launcher_out)
if printf '%s' "$out" | grep -q 'FAKE-ARGS:'; then
	pass "unpinned run resolved the latest release"
else
	fail "unpinned run failed" "$(printf '%s' "$out" | tr '\n' '|')"
fi

section "8. PowerShell launcher (logic; runs under pwsh on this host)"
if command -v pwsh >/dev/null 2>&1; then
	# (a) cold download + checksum verification.
	ps_cache="$WORK/cache-ps"
	ps_out=$(env WARPBENCH_BASE_URL="$BASE_URL" WARPBENCH_CACHE_DIR="$ps_cache" \
		WARPBENCH_VERSION=v0.1.0 pwsh -NoProfile -File "$LAUNCHER_PS" </dev/null 2>&1 || true)
	ps_exe="$ps_cache/v0.1.0/warpbench_windows_${TEST_ARCH}.exe"
	if printf '%s' "$ps_out" | grep -q 'checksum ok' && [ -f "$ps_exe" ]; then
		pass "downloads and verifies the fake windows asset"
	else
		fail "did not download/verify the fake windows asset" "$(printf '%s' "$ps_out" | tr '\n' '|')"
	fi

	# (b) Run path. The asset must be pre-seeded with the execute bit, because
	# Invoke-WebRequest -OutFile does not set one and this host is Linux; on
	# Windows executability does not depend on the mode bit, so this is purely a
	# test-harness concern.
	run_cache="$WORK/cache-ps-run"
	mkdir -p "$run_cache/v0.1.0"
	cp -a "$RELEASE_ROOT/download/v0.1.0/." "$run_cache/v0.1.0/"
	ps_run=$(env WARPBENCH_BASE_URL="$BASE_URL" WARPBENCH_CACHE_DIR="$run_cache" \
		WARPBENCH_VERSION=v0.1.0 pwsh -NoProfile -File "$LAUNCHER_PS" </dev/null 2>&1 || true)
	if printf '%s' "$ps_run" | grep -q 'FAKE-WIN-ARGS'; then
		pass "runs the cached binary and forwards arguments"
	else
		fail "did not run the cached binary" "$(printf '%s' "$ps_run" | tr '\n' '|')"
	fi
	if printf '%s' "$ps_run" | grep -q 'using cached'; then
		pass "reuses the cached binary instead of re-downloading"
	else
		fail "did not report a cache hit" "$(printf '%s' "$ps_run" | tr '\n' '|')"
	fi

	# (c) A wrong checksum must be refused.
	ps_bad=$(env WARPBENCH_BASE_URL="$BASE_URL" WARPBENCH_CACHE_DIR="$WORK/cache-ps-bad" \
		WARPBENCH_VERSION=badhash pwsh -NoProfile -File "$LAUNCHER_PS" </dev/null 2>&1 || true)
	if printf '%s' "$ps_bad" | grep -qi 'checksum mismatch'; then
		pass "rejects a mismatched checksum"
	else
		fail "did not report a checksum mismatch" "$(printf '%s' "$ps_bad" | tr '\n' '|')"
	fi
else
	skip "pwsh not installed"
fi

section "9. cache survives the network disappearing"
kill "$SERVER_PID" 2>/dev/null
wait "$SERVER_PID" 2>/dev/null
SERVER_PID=""
run_launcher bash "$WORK/cache-latest"
out=$(launcher_out)
if printf '%s' "$out" | grep -q 'FAKE-ARGS:'; then
	pass "runs from cache with the server gone"
else
	fail "cache miss after the server stopped" "$(printf '%s' "$out" | tr '\n' '|')"
fi

section "10. truncation safety (curl | bash)"
last_line=$(grep -v '^[[:space:]]*$' "$LAUNCHER_SH" | tail -1)
if [ "$last_line" = 'main "$@"' ]; then
	pass "main \"\$@\" is the last non-blank line"
else
	fail "last non-blank line is '$last_line', want 'main \"\$@\"'"
fi

total_lines=$(wc -l <"$LAUNCHER_SH")
truncated="$WORK/truncated.sh"
head -n $((total_lines - 3)) "$LAUNCHER_SH" >"$truncated"
out=$(env WARPBENCH_BASE_URL="$BASE_URL" WARPBENCH_CACHE_DIR="$WORK/trunc-cache" \
	bash "$truncated" 2>&1 </dev/null || true)
if [ ! -d "$WORK/trunc-cache" ] && [ -z "$out" ]; then
	pass "a truncated copy downloads and runs nothing"
else
	fail "a truncated copy had side effects" "$(printf '%s' "$out" | tr '\n' '|')"
fi

section "11. static checks"
if command -v shellcheck >/dev/null 2>&1; then
	if shellcheck -s sh "$LAUNCHER_SH" >/dev/null 2>&1; then
		pass "shellcheck -s sh: clean"
	else
		fail "shellcheck -s sh reported issues"
	fi
else
	skip "shellcheck not installed"
fi

if command -v pwsh >/dev/null 2>&1; then
	# shellcheck disable=SC2016  # single quotes are deliberate: the body is PowerShell, not shell
	ps_out=$(WARPBENCH_PS_PATH="$LAUNCHER_PS" pwsh -NoProfile -Command '
		$errors = $null
		[System.Management.Automation.Language.Parser]::ParseFile($env:WARPBENCH_PS_PATH, [ref]$null, [ref]$errors) | Out-Null
		if ($errors) { $errors | ForEach-Object { $_.Message } } else { "OK" }
	' 2>&1)
	if printf '%s' "$ps_out" | grep -q '^OK$'; then
		pass "warpbench.ps1 parses cleanly"
	else
		fail "warpbench.ps1 has parse errors" "$ps_out"
	fi
else
	skip "pwsh not installed"
fi

# ---------------------------------------------------------------------------
printf '\n\033[1msummary\033[0m: %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
