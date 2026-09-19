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
| 4 | Measurement: stats, ping/timings, four throughput adapters | ⬜ |
| 5 | Runner, `--phase` / `--compare`, JSON schema | ⬜ |
| 6 | TUI + ASCII fallback | ⬜ |
| 7 | Markdown/JSON reports, `METHODOLOGY.md` | ⬜ |
| 8 | goreleaser release pipeline, `v0.1.0` | ⬜ |

## Install

Not yet available — no release exists. The intended one-liners will be:

```sh
# Linux / macOS
curl -fsSL <short-link>/sh | bash
```

```powershell
# Windows (PowerShell 5.1 or 7)
irm <short-link>/ps1 | iex
```

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
