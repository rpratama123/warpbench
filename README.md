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
| 3 | Server list: schema, loader, cache, `servers.json` v1 | ⬜ |
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

## Verifying the server list

[`tools/check-servers.sh`](tools/check-servers.sh) validates every endpoint in
`servers.json` (ping host resolves, download URL returns 200/206, upload endpoint
accepts a POST, iperf3 ports accept TCP). It is what produced the evidence in
`PLAN.md` §2, and the same script backs the weekly validation Action:

```sh
bash tools/check-servers.sh endpoints.tsv
```

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
