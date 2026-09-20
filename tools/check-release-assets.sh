#!/usr/bin/env bash
#
# check-release-assets.sh - assert that release artifacts match what the
# launchers expect.
#
# warpbench.sh and warpbench.ps1 build a release URL from the platform and fetch
# a bare binary under an exact name, plus a file literally called SHA256SUMS.
# Nothing in the Go build fails if goreleaser is configured to emit different
# names: the launchers would simply 404 for every user, and only a real release
# would reveal it. This script asserts the contract against the artifacts
# actually produced.
#
# Usage: check-release-assets.sh [dist-dir]
# Exit:  0 the contract holds, 1 otherwise.

set -euo pipefail

DIST="${1:-dist}"

if [ ! -d "$DIST" ]; then
	printf 'check-release-assets: no such directory: %s\n' "$DIST" >&2
	exit 1
fi

if ! command -v python3 >/dev/null 2>&1; then
	printf 'check-release-assets: python3 is required to read the goreleaser metadata\n' >&2
	exit 1
fi

python3 - "$DIST" <<'PY'
import hashlib
import json
import os
import sys

dist = sys.argv[1]

# The exact names the launchers construct, per os/arch.
EXPECTED = [
    "warpbench_linux_amd64",
    "warpbench_linux_arm64",
    "warpbench_darwin_amd64",
    "warpbench_darwin_arm64",
    "warpbench_windows_amd64.exe",
    "warpbench_windows_arm64.exe",
]

failures = []

# --- the checksum file must be named exactly SHA256SUMS --------------------
#
# goreleaser's default is checksums.txt. The launchers fetch SHA256SUMS, so the
# default would 404 even though every binary was fine.
sums_path = os.path.join(dist, "SHA256SUMS")
if not os.path.exists(sums_path):
    present = sorted(os.listdir(dist))
    failures.append(
        f"missing {sums_path}: the launchers fetch a file named SHA256SUMS "
        f"(goreleaser defaults to checksums.txt). Found: {present}"
    )

# --- every expected asset must be produced under that exact name -----------
meta_path = os.path.join(dist, "artifacts.json")
if not os.path.exists(meta_path):
    failures.append(f"missing {meta_path}: cannot verify the asset names")
    meta = []
else:
    with open(meta_path) as fh:
        meta = json.load(fh)

# Only Binary artifacts are uploaded as bare files; an Archive would be a
# tarball, which a launcher cannot exec.
binaries = {a.get("name"): a.get("path") for a in meta if a.get("type") == "Binary"}
# goreleaser records both the build output and the uploadable copy; either name
# may carry the final asset name, so accept any Binary whose name matches.
found = {name for name in binaries if name in EXPECTED}

for name in EXPECTED:
    if name not in found:
        failures.append(
            f"missing release asset {name!r}. The launchers request this exact "
            f"name at /releases/latest/download/{name}"
        )

# --- every checksum must match the file it describes -----------------------
#
# This is what the launcher verifies before it will execute anything, so a
# mismatch here means every install fails closed.
recorded = {}
if os.path.exists(sums_path):
    with open(sums_path) as fh:
        for line in fh:
            parts = line.split()
            if len(parts) == 2:
                recorded[parts[1].lstrip("*")] = parts[0]

if os.path.exists(sums_path):
    for name in EXPECTED:
        digest = recorded.get(name)
        if digest is None:
            failures.append(f"{name} has no entry in SHA256SUMS")
            continue

        path = binaries.get(name)
        if not path or not os.path.exists(path):
            # The metadata may name the file but the path can be relative to a
            # different root in some goreleaser versions.
            failures.append(f"{name}: cannot find the built file to verify (path={path})")
            continue

        with open(path, "rb") as fh:
            actual = hashlib.sha256(fh.read()).hexdigest()

        if actual != digest:
            failures.append(
                f"{name}: SHA256SUMS records {digest} but the file hashes to {actual}"
            )

# --- report ----------------------------------------------------------------
if failures:
    print("release asset contract FAILED:", file=sys.stderr)
    for f in failures:
        print(f"  - {f}", file=sys.stderr)
    sys.exit(1)

print(f"release asset contract holds: {len(EXPECTED)} assets, names and checksums verified")
for name in EXPECTED:
    print(f"  ok {name}")
PY
