# warpbench

Reproducible measurements of what Cloudflare WARP actually changes on a congested
ISP uplink — latency, jitter, packet loss, connection setup, and **download and
upload throughput** — against public servers grouped by geography
(Indonesia → Singapore → Tokyo → Europe → United States).

Run one command, get the same test suite over your raw ISP path and over WARP,
side by side.

> **Status: pre-alpha.** The design is settled and published in [`PLAN.md`](PLAN.md);
> no measurement code exists yet. This repository is being built phase by phase.

## Why

Some Indonesian ISPs have congested international uplinks, so overseas browsing and
downloads degrade at certain hours while domestic traffic stays fine. Turning on WARP
appears to fix it — but that is anecdotal. This project produces real, reproducible
numbers so the method and the results can be published and independently checked.

## Design in one paragraph

Thin platform launchers ([`warpbench.sh`](warpbench.sh), [`warpbench.ps1`](warpbench.ps1))
do four things only: detect OS/arch, download the matching release binary plus
`SHA256SUMS` into a per-user cache, verify the checksum, and `exec` the binary.
Everything else — the TUI, all measurement, and report generation — lives in a single
static Go binary, so every platform shares one codebase and one report format.
No admin/root, no pre-installed dependencies beyond the OS.

## Plan

| Phase | Deliverable | Status |
|---|---|---|
| 1 | [`PLAN.md`](PLAN.md) — architecture, methodology, schema, risks | ✅ approved |
| 2 | Repo scaffold, `go.mod`, launchers, CI | ✅ |
| 3 | Server list: schema, loader, cache, `servers.json` v1 | ✅ |
| 4 | Measurement: stats, ping/timings, four throughput adapters | ✅ |
| 5 | Runner, `--phase` / `--compare`, JSON schema | ✅ |
| 6 | TUI + ASCII fallback | ✅ |
| 7 | Markdown/JSON reports, `METHODOLOGY.md` | ✅ |
| 8 | goreleaser release pipeline, `v0.1.0` | ✅ |

## Use

Run it with no arguments on a terminal and it walks you through the whole
thing: choose how much to measure, pick targets, watch the phase run, flip WARP
on when it asks, and see the before/after bars. It is the same runner and the
same result files as the scripted path below, so the two agree by construction.

Measure the ISP path, turn WARP on, measure again, then compare:

```sh
warpbench --quick --phase baseline --out baseline.json
# turn Cloudflare WARP on
warpbench --quick --phase warp --out warp.json
warpbench --compare baseline.json warp.json                 # plain table
warpbench --compare --report report.md baseline.json warp.json   # Markdown
```

The Markdown report is the publishable artefact: metadata, a per-metric table
and ASCII chart, a list of servers that could not be compared, the caveats that
stop the numbers being over-read, and the exact command line that produced it.
`--report -` writes it to stdout instead.

Each phase writes a versioned JSON file holding **every raw sample**, not just
the summary, so the headline can be recomputed rather than trusted and a later
version can re-render without re-measuring. The public IP is masked by default;
`--no-mask` disables that.

`--quick` measures 10 servers (2 per group, one download-led and one
upload-led) in about 8 minutes for both phases. `--extended` measures the whole
list, which is roughly 75 minutes for both phases, so it is worth narrowing with
`--groups`. Both figures are computed from your actual selection and printed
before the run starts:

```sh
warpbench --extended --groups id,sg --phase baseline --out baseline.json
```

Two safety checks make the comparison mean something. A `baseline` run refuses
to start while WARP is on, and a `warp` run refuses while it is off — because a
"baseline" measured through WARP looks perfectly fine and means nothing.
`--force` overrides, and the override is recorded in the result. Comparing two
files measured against different server-list revisions is likewise refused
unless forced.

## What the numbers do and do not support

The method is documented in full in [`METHODOLOGY.md`](METHODOLOGY.md). Two
points are worth stating up front, because they are the ones most easily gotten
wrong when reading a result:

- **The Cloudflare edge target is not an end-to-end measurement.** With WARP on,
  that path never leaves Cloudflare's network, so it measures the ISP-to-edge
  hop. It is footnoted in every report it appears in.
- **Writing a report is not a licence to publish it.** Every report leads with
  its own caveats: which targets are best-effort, where ICMP was unavailable and
  TCP connect time was substituted, which samples were undersized, and how far
  apart the two phases were.

## Install

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/rpratama123/warpbench/main/warpbench.sh | bash
```

```powershell
# Windows (Windows PowerShell 5.1 or PowerShell 7)
irm https://raw.githubusercontent.com/rpratama123/warpbench/main/warpbench.ps1 | iex
```

Those URLs are stable. They are served from `main`, not from a release tag, and
the launcher resolves the binary version itself — so a short link built on one
of them keeps working across every future release, and the launcher can be fixed
without anyone re-issuing a link.

The launcher does four things and nothing else: detect the platform, download
the matching binary and `SHA256SUMS` into a per-user cache, verify the checksum,
and execute it. No `sudo`, nothing written outside the cache, and it refuses to
run a binary whose checksum does not match. Reading it takes about two minutes:
[warpbench.sh](warpbench.sh) is under 150 lines, most of it comments.

To pin a version rather than take the latest:

```sh
curl -fsSL .../warpbench.sh | WARPBENCH_VERSION=v0.1.0 bash
```

### Verify a download yourself

Every release ships `SHA256SUMS`, and `SHA256SUMS` is signed with Sigstore in
keyless mode, which proves it came from this repository's release workflow:

```sh
sha256sum --check --ignore-missing SHA256SUMS

cosign verify-blob \
  --bundle SHA256SUMS.bundle \
  --certificate-identity-regexp '^https://github.com/rpratama123/warpbench/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS
```

There is no signing key to trust out of band, because there is no signing key:
the certificate is issued to the workflow run that built the release.

## The server list

[`servers.json`](servers.json) is the curated set of measurement targets,
grouped Indonesia → Singapore → Tokyo → Europe → United States. It is fetched at
runtime from this repository's `main` branch, so adding or retiring a target
needs no release. Resolution order is **remote → cache → embedded**, with
`--servers <path|url>` overriding all three; the embedded copy is compiled into
the binary so warpbench works with no network at all. Every report records which
source won and the list `revision`.

Each entry declares a `protocol` (`librespeed`, `http-file`, `cloudflare` or
`iperf3`), an explicit `capabilities` array, and a `tier` of `quick` or
`extended`. The shape is defined once in
[`schema/servers.schema.json`](schema/servers.schema.json), which is enforced at
runtime against the embedded copy — so the schema is the actual contract, not
documentation that can drift.

Two deliberate quirks worth knowing:

- **Upload coverage in APAC comes from iperf3**, not LibreSpeed. The canonical
  LibreSpeed community list has one APAC server and it returns 403, so iperf3 is
  a first-class protocol rather than an optional extra.
- **`id-cf-cgk` is footnoted.** With WARP on, that path never leaves Cloudflare's
  network, so it measures ISP-to-nearest-edge only and must not be read as an
  end-to-end international result. It is also the reason `id-datautama` — a
  domestic Ubuntu mirror, marked as a control — is in the list: it is what shows
  the effect is specific to international transit.

A bad entry is skipped individually with a warning naming it and the reason, so
one stale host cannot take the whole list down. Structural problems fail hard.

### Validating the list

```sh
go run ./tools/validate-servers -v
```

This loads the list through the same code path the binary uses — so a schema
violation or any dropped entry fails the run — then probes every target: DNS for
`ping_host`, a bounded HTTP request for download and upload endpoints, and a TCP
connect for iperf3 ports. It exits non-zero if anything fails, which is what
[the weekly workflow](.github/workflows/validate-servers.yml) uses to open an
issue when a target rots.

[`tools/check-servers.sh`](tools/check-servers.sh) is the ad-hoc shell prober,
useful for testing candidate hosts that are not in the list yet:

```sh
printf 'my-host\tdownload\thttps://example.com/100MB.bin\nmy-iperf\tiperf3\t1.2.3.4:5201\n' \
  | bash tools/check-servers.sh
```

### Editing the list

`go:embed` cannot reach outside its own package, so
[`internal/serverlist/embedded/`](internal/serverlist/embedded) holds generated
copies. After editing `servers.json` or the schema:

```sh
go generate ./...
```

A test fails if the copies drift, so forgetting this is caught locally and in CI.

## Reports

A report looks like this (abridged):

```markdown
# warpbench: raw ISP path vs Cloudflare WARP

| Measured | 2026-09-20T08:00:00+07:00 to 2026-09-20T08:17:00+07:00 |
| Server list | revision 2026-09-20, source remote |

## Summary

download improved on 2/2 servers (median +684%), upload improved on 1/1
servers (median +881%), latency changed by a median of -0.1 ms

## Download

| Server | ISP Mbps | WARP Mbps | Δ | Δ% | Verdict |
|---|---:|---:|---:|---:|---|
| id-cf-cgk | 41.20 | 118.40 | +77.20 | +187.4% | better |

```text
id-cf-cgk ISP  ##########.................. 41.2 Mbps
          WARP ############################ 118.4 Mbps  +187.4%  better
```

## Caveats

- **Cloudflare edge is not an end-to-end measurement.** ...
```

The full rendered example lives in
[`internal/report/testdata/report.md`](internal/report/testdata/report.md), which
is also the golden file the test suite compares against — so the documented
format cannot drift from the produced one.

## Development

Requires Go (see `go.mod`) plus `shellcheck`, and `pwsh` with `PSScriptAnalyzer`
for the PowerShell launcher. Runs on Linux, macOS and Windows.

```sh
go build ./...
go test ./...
golangci-lint run ./...      # v2 config; `golangci-lint config verify` first
shellcheck -s sh warpbench.sh
bash tests/launcher_test.sh  # launcher contract tests against a fake release
```

The measurement layer also has opt-in tests that hit real servers, including
downloading and running the pinned iperf3 binary:

```sh
WARPBENCH_INTEGRATION=1 go test ./internal/netprobe/ ./internal/throughput/ -run Integration -v
```

They need network access and take a couple of minutes, so they are skipped by
default.

The release pipeline is checked the same way. `tools/check-release-assets.sh`
asserts the launcher's asset contract against a real goreleaser snapshot — that
the binaries are named exactly `warpbench_<os>_<arch>[.exe]`, and that the
checksum file is named `SHA256SUMS` rather than goreleaser's default
`checksums.txt`. Nothing in the Go build fails if that drifts; the launchers
would simply 404 for every user, and only a real release would reveal it.

To reproduce locally:

```sh
goreleaser release --snapshot --skip=publish --skip=sign --clean
bash tools/check-release-assets.sh dist
```

The launcher contract tests spin up a local HTTP server that impersonates a
GitHub release, then assert the launchers download, verify, cache, refuse bad
checksums, forward arguments, and propagate exit codes. They need `python3` and
an **exec-capable** scratch directory — if `/tmp` is mounted `noexec`, point
them elsewhere with `WARPBENCH_TEST_TMPDIR`.

The binary itself is a scaffold at present; `warpbench --doctor` reports the
environment facts (platform, console interactivity, cache directory) that the
launchers and the binary must agree on.

## Licence

MIT — see [`LICENSE`](LICENSE).
