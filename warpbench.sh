#!/bin/sh
# ---------------------------------------------------------------------------
# warpbench.sh - download and run the warpbench binary.
#
# This script does exactly four things:
#   1. detect your OS and CPU architecture
#   2. download the matching release binary and SHA256SUMS into a per-user cache
#   3. verify the SHA-256 checksum, and refuse to run on a mismatch
#   4. exec the binary, passing your arguments through unchanged
#
# It never uses sudo, never writes outside the cache directory, and contacts
# only github.com and the objects.githubusercontent.com host GitHub redirects
# release downloads to.
#
# Optional environment overrides:
#   WARPBENCH_VERSION     pin a release tag, e.g. v0.1.0      (default: latest)
#   WARPBENCH_CACHE_DIR   cache location
#   WARPBENCH_BASE_URL    release base URL (for testing or self-hosting)
#
# Works under sh, dash, bash 3.2+ and busybox ash. Needs curl or wget, and
# sha256sum or shasum.
# ---------------------------------------------------------------------------

set -eu

REPO="rpratama123/warpbench"
PROG="warpbench"

log() { printf '%s: %s\n' "$PROG" "$*" >&2; }
die() { printf '%s: %s\n' "$PROG" "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# --- 1. platform -----------------------------------------------------------

# Sets ASSET (release asset filename) from the running OS and architecture.
#
# ASSET NAMING CONTRACT: goreleaser publishes bare binaries named
#   warpbench_<os>_<arch>[.exe]      os in {linux,darwin,windows}, arch in {amd64,arm64}
# with no version in the filename, so that /releases/latest/download/<asset>
# resolves without knowing the tag. .goreleaser.yaml must keep name_template in
# sync with this function; the launcher contract test covers it.
detect_platform() {
	os_name=$(uname -s 2>/dev/null) || die "could not run 'uname -s' to detect the OS"
	case "$os_name" in
	Linux) os_name=linux ;;
	Darwin) os_name=darwin ;;
	MINGW* | MSYS* | CYGWIN*)
		die "this is the Unix launcher. On Windows use warpbench.ps1 (irm ... | iex)"
		;;
	*) die "unsupported OS '$os_name'; warpbench ships linux, darwin and windows builds" ;;
	esac

	cpu_arch=$(uname -m 2>/dev/null) || die "could not run 'uname -m' to detect the architecture"
	case "$cpu_arch" in
	x86_64 | amd64) cpu_arch=amd64 ;;
	aarch64 | arm64) cpu_arch=arm64 ;;
	armv7l | armv6l | i386 | i686)
		die "unsupported architecture '$cpu_arch'; warpbench ships amd64 and arm64 only"
		;;
	*) die "unsupported architecture '$cpu_arch'" ;;
	esac

	ASSET="${PROG}_${os_name}_${cpu_arch}"
}

# --- 2. download -----------------------------------------------------------

# download <url> <destination>
#
# Deliberately does NOT die on HTTP failure: it returns non-zero so each caller
# can explain the failure in its own terms (a missing release reads very
# differently from a checksum mismatch). curl -f turns 404 into a failure.
download() {
	if have curl; then
		curl -fsSL --retry 3 --connect-timeout 15 -o "$2" "$1"
	elif have wget; then
		wget -q -O "$2" "$1"
	else
		die "neither curl nor wget is installed; install one and try again"
	fi
}

# --- 3. checksum -----------------------------------------------------------

# sha256_of <file> -> hex digest on stdout
sha256_of() {
	if have sha256sum; then
		sha256sum "$1" | awk '{print $1}'
	elif have shasum; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		die "neither sha256sum nor shasum is installed, so the download cannot be verified"
	fi
}

# expected_hash <sha256sums-file> -> prints the digest recorded for ASSET
expected_hash() {
	awk -v a="$ASSET" '$2 == a { print $1; exit }' "$1"
}

# matches <binary> <sha256sums-file> -> 0 when the binary matches, silently
#
# Silent because it also answers "is the cached copy still the current
# release?", where a mismatch is not an error -- it just means there is
# something newer to fetch.
matches() {
	want=$(expected_hash "$2")
	[ -n "$want" ] || return 1
	[ "$(sha256_of "$1")" = "$want" ]
}

# verify <binary> <sha256sums-file> -> 0 if the binary matches, explaining
# itself when it does not. Used after a download, where a mismatch is fatal.
verify() {
	want=$(expected_hash "$2")
	if [ -z "$want" ]; then
		log "no checksum entry for $ASSET in SHA256SUMS; refusing to run it"
		return 1
	fi
	got=$(sha256_of "$1")
	if [ "$want" != "$got" ]; then
		log "checksum mismatch for $ASSET"
		log "  expected $want"
		log "  actual   $got"
		return 1
	fi
	return 0
}

# --- 4. run ----------------------------------------------------------------

main() {
	detect_platform

	cache_dir="${WARPBENCH_CACHE_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/warpbench}"
	tag="${WARPBENCH_VERSION:-latest}"
	base="${WARPBENCH_BASE_URL:-https://github.com/$REPO/releases}"

	if [ "$tag" = latest ]; then
		url_base="$base/latest/download"
	else
		url_base="$base/download/$tag"
	fi

	dest_dir="$cache_dir/$tag"
	binary="$dest_dir/$ASSET"
	sums="$dest_dir/SHA256SUMS"
	mkdir -p "$dest_dir" || die "could not create cache directory $dest_dir"
	chmod 700 "$cache_dir" "$dest_dir" 2>/dev/null || true

	# Decide whether the cached copy is still current.
	#
	# A pinned tag is immutable, so a copy whose checksum matches is final.
	# `latest` moves with every release, so its checksum file has to be re-read
	# on every run -- a few hundred bytes -- and the binary fetched again when
	# the release has changed. Treating `latest` as immutable means a user runs
	# whatever they first downloaded, forever, including past a bug fix.
	use_cache=0
	sums_fresh=0

	if [ -f "$binary" ] && [ -f "$sums" ]; then
		if [ "$tag" = latest ]; then
			if download "$url_base/SHA256SUMS" "$sums.part" 2>/dev/null; then
				mv "$sums.part" "$sums"
				sums_fresh=1
				matches "$binary" "$sums" && use_cache=1
			else
				# Offline. A cached copy that still matches its own recorded
				# checksum is better than refusing to run.
				log "could not reach the release; using the cached copy"
				matches "$binary" "$sums" && use_cache=1
			fi
		elif matches "$binary" "$sums"; then
			use_cache=1
		fi
	fi

	if [ "$use_cache" -eq 1 ]; then
		log "using cached $binary"
	else
		log "platform:   $ASSET"
		log "release:    $url_base"

		if [ "$sums_fresh" -eq 0 ]; then
			log "downloading SHA256SUMS"
			download "$url_base/SHA256SUMS" "$sums.part" ||
				die "no release found at $url_base (has a release been published yet?)"
			mv "$sums.part" "$sums"
		fi

		log "downloading $ASSET"
		download "$url_base/$ASSET" "$binary.part" || die "download failed: $url_base/$ASSET"
		chmod 700 "$binary.part" 2>/dev/null || true
		mv "$binary.part" "$binary"

		if ! verify "$binary" "$sums"; then
			rm -f "$binary" "$sums"
			die "refusing to run $ASSET: checksum verification failed"
		fi
		log "checksum ok"
	fi

	# Pipe-safe handoff. With 'curl | bash', stdin is this script, so the binary
	# must not inherit it. Prefer a real terminal; if there is no controlling
	# terminal at all, fall back to non-interactive mode rather than hanging.
	if [ -t 0 ]; then
		exec "$binary" "$@"
	fi

	if (exec </dev/tty) 2>/dev/null; then
		exec "$binary" "$@" </dev/tty
	fi

	log "no terminal available; running non-interactively (--no-tty)"
	exec "$binary" --no-tty "$@" </dev/null
}

# Invoked on the last line so a truncated download defines functions but never
# runs anything.
main "$@"
