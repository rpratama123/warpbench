# warpbench — Phase 1 Plan

**Status:** draft for approval. No code has been written. `tools/check-servers.sh` exists and was used to produce the evidence in §2.

**Version:** plan v1 · 2026-09-20

---

## 0. TL;DR

The architecture you decided in the brief (§3) holds up: thin launchers + one static Go binary. I recommend adopting it as-is.

Six things in the brief are **factually wrong or unachievable**, and this plan corrects them. All were verified against live endpoints from this host; evidence is in §2.

| # | Brief says | Reality | Plan response |
|---|---|---|---|
| 1 | LibreSpeed is the primary target, "exists on every continent" | The canonical list has **22 servers — all EU/US except one Tokyo entry, which is dead (403)**. Effectively **zero APAC coverage.** | Demote LibreSpeed to *primary where it exists* (EU/US). Use **iperf3 as the APAC upload source** (you approved this). |
| 2 | "Download an official static iperf3 binary" | **ESnet publishes no binaries at all.** Only third-party static builds exist; the best one has **no `windows/arm64` asset and publishes no checksums.** | Pin a third-party build by SHA256 compiled into our binary; self-build in CI is the alternative (§9.4). windows/arm64 degrades gracefully. |
| 3 | DigitalOcean speedtest hosts as EU/US/SG targets | **All four are dead** (DNS NXDOMAIN). | Dropped entirely. |
| 4 | Vultr `ewr-ping.vultr.com` "(or current NJ host)" | `ewr-ping` is dead; **`nj-us-ping.vultr.com` is live**. | Use `nj-us-ping.vultr.com`. |
| 5 | Quick ≈ 4–6 min, Extended ≈ 20–30 min total | Arithmetically impossible with the caps in §6 once you run **two phases**. Quick lands ~14 min, Extended ~64 min. | Explicit duration budget in §5.7 that reconciles caps with the targets. |
| 6 | `speed.cloudflare.com/meta` implied usable | **403.** Also `cdn-cgi/trace` returns **404 on HEAD** — needs GET. | `/meta` not used; trace always via GET. |

Two additions I recommend beyond the brief, both cheap and both things a reviewer will ask for:

- **Slow-start exclusion.** Throughput averaged over the whole sample understates fast links and overstates slow ones. Report steady-state (excluding the first 1.0 s) as the headline, and the whole-sample average alongside it.
- **Honest duration display.** Never promise a wall-clock total; compute it from the live selection and show it (the brief already asks for this — §5.7 makes it the single source of truth).

---

## 1. Architecture

**Thin platform launchers + one static Go binary.** Unchanged from §3 of the brief.

```
user → warpbench.sh / warpbench.ps1   (detect, download, verify, exec — ~80 lines each)
         └─ exec → warpbench (static Go binary)
                    ├─ server list  (remote → cache → embedded)
                    ├─ measurement  (ping / timings / throughput adapters)
                    ├─ TUI          (bubbletea)
                    └─ report       (Markdown + JSON)
```

### 1.1 Launcher contract

The launchers do exactly four things and nothing else. This is the entire security story for `curl | bash`, so the constraint is hard:

1. Detect OS + arch → map to a release asset name.
2. Resolve version (`WARPBENCH_VERSION` override, else latest release) and download binary + `SHA256SUMS` into the per-user cache.
3. Verify SHA256; **refuse to execute on mismatch** (delete the artifact and exit non-zero).
4. `exec` the binary, passing the user's arguments through unchanged.

Explicitly *not* in the launcher: no server-list parsing, no JSON, no test logic, no `sudo`, no writes outside the cache dir.

**Cache locations**

| OS | Path |
|---|---|
| Linux | `${XDG_CACHE_HOME:-$HOME/.cache}/warpbench` |
| macOS | `${XDG_CACHE_HOME:-$HOME/.cache}/warpbench` (respects XDG; falls back to `~/.cache`) |
| Windows | `$env:LOCALAPPDATA\warpbench` |

Windows would conventionally use `%LOCALAPPDATA%` — correct as written. On macOS I deliberately do **not** use `~/Library/Caches`, so that one code path and one set of docs covers both Unixes.

**Dependency floor.** Unix: needs `curl` *or* `wget`, plus `sha256sum` *or* `shasum -a 256`. If absent → print the exact install command for the detected distro and exit 1. Windows: `Invoke-WebRequest -UseBasicParsing` + `Get-FileHash` only, both native to PS 5.1. Set `[Net.ServicePointManager]::SecurityProtocol` to include `Tls12` (PS 5.1 on older Windows defaults below TLS 1.2 and the download fails with an opaque error).

**Pipe-safety (`curl … | bash`).**

- Entire script wrapped so the entrypoint is `main "$@"` on the **last line**. A truncated download therefore defines functions and never executes anything.
- When stdin is not a TTY, exec with `</dev/tty`. If `/dev/tty` cannot be opened (no controlling terminal — CI, cron, some SSH setups), exec with `</dev/null` **and append `--no-tty`** to the argument list so the binary selects non-interactive mode instead of hanging or drawing garbage.
- Must work under **bash 3.2** (macOS ships it) — so: no `mapfile`, no associative arrays, no `${var,,}`, no `==` inside `[ ]`. POSIX `sh` where practical, but `local` and `$(...)` are fine and assumed.
- `set -eu` plus an `ERR` trap that reports the failing line. Never `set -x` globally (would leak the URL/token into logs in some setups).

**Pipe-safety (`irm … | iex`).** PowerShell already buffers the whole string before executing, so there is no truncation hazard; the binary gets a real console. The *binary* must nonetheless verify it owns an interactive console before drawing a TUI (§6), because `irm | iex` from a non-interactive host still yields `stdout` redirected.

**Vetting.** Both launchers must be readable end-to-end by a cautious user in under two minutes. Enforced by keeping them under ~80 significant lines and adding a comment block at the top stating exactly the four things they do, the two hosts they contact (GitHub Releases + `raw.githubusercontent.com`), and how to verify the checksum by hand.

### 1.2 Why Go (and alternatives considered)

| Option | Verdict |
|---|---|
| **Go, static, `CGO_ENABLED=0`** | **Chosen.** Cross-compiles to all six targets from one runner; precise `time.Duration` timers; `net/http/httptrace` gives DNS/connect/TLS/TTFB directly; `io.Discard` streaming; goroutines for parallel streams; a real TUI library; single-file, no runtime. |
| Rust | Equivalent capability, but cross-compiling static musl + macOS + Windows from CI is more moving parts, and the TUI ecosystem is less batteries-included for this. |
| Python + venv/uv | Violates "zero pre-installed dependencies"; interpreter bootstrap is a second download and a second trust story. |
| Node | Same objection, plus a much larger download. |
| Pure shell | No precise timers, no TLS timing, no multiplexing, and bash 3.2/PS 5.1 parity would dominate the codebase. This is exactly what the brief's §3 rejects. |

### 1.3 Dependency choices

Versions resolved live from `proxy.golang.org` on 2026-09-20 (pinned in `go.mod`):

| Module | Version | Purpose | Alternatives / why not |
|---|---|---|---|
| `charmbracelet/bubbletea` | v1.3.10 | TUI event loop | **v1, not v2** — v2 is still pre-release; v1 is what `bubbles`/`lipgloss` are stable against. `tview` is heavier and its widgets fight custom inline bar rendering. |
| `charmbracelet/bubbles` | v1.0.0 | spinner, progress, viewport, key bindings | — |
| `charmbracelet/lipgloss` | v1.1.0 | layout/colour, `NO_COLOR` + TTY detection | Lipgloss degrades to plain text automatically when colour is disabled. |
| `prometheus-community/pro-bing` | **v0.8.0** (plan said v0.9.1) | ICMP, unprivileged where possible. v0.9.x requires Go 1.25 and would have forced a toolchain bump on every build, so v0.8.0 keeps the Go 1.24 floor. | `golang.org/x/net/icmp` alone means hand-rolling sequence/timing; `go-ping` is less maintained. |
| `charmbracelet/x/term` | v0.2.2 | TTY size, raw mode, Windows VT enablement | bubbletea dependency anyway. |
| `goreleaser` | v2.18.2 | cross-build, checksums, releases | Not installed on this host; runs in CI. |

**No iperf3 Go library.** `BGrewell/go-iperf` is a *wrapper around the external binary*, not an implementation, so it buys nothing over `os/exec` plus `-J` JSON parsing. See §9.4 for why I rejected a hand-written pure-Go iperf3 client.

---

## 2. Verified findings (evidence)

All probes run from this host on 2026-09-20 using `tools/check-servers.sh` (HEAD first, ranged `GET` with `--max-filesize` fallback, curl exit code recorded so "server ignored Range" is distinguishable from "unreachable").

### 2.1 Download / upload endpoints

**Live — ping + download**

| Target | Endpoint | Status | Notes |
|---|---|---|---|
| SG Linode | `speedtest.singapore.linode.com/100MB-singapore.bin` | 200 | `10MB-singapore.bin` → **404**; filenames must be verified per host, never assumed |
| SG Vultr | `sgp-ping.vultr.com/vultr.com.100MB.bin` | 200 | |
| SG Hetzner | `sin-speed.hetzner.com/100MB.bin` | 206 | `total=104857600` |
| JP Linode | `speedtest.tokyo2.linode.com/100MB-tokyo.bin` | 200 | |
| JP Vultr | `hnd-jp-ping.vultr.com/vultr.com.100MB.bin` | 200 | |
| EU Hetzner ×3 | `fsn1-`, `hel1-`, `nbg1-speed.hetzner.com/100MB.bin` | 206 | all honour `Range` |
| EU Linode | `speedtest.frankfurt.linode.com/100MB-frankfurt.bin` | 200 | |
| EU Vultr ×2 | `fra-de-ping`, `ams-nl-ping.vultr.com` | 200 | |
| US Linode ×2 | `speedtest.newark`, `speedtest.fremont.linode.com` | 200 | |
| US Vultr | `lax-ca-us-ping.vultr.com`, **`nj-us-ping.vultr.com`** | 200 | `nj-us` replaces the dead `ewr-ping` |
| US Hetzner ×2 | `ash-speed`, `hil-speed.hetzner.com` | 206 | |
| Cloudflare | `speed.cloudflare.com/__down?bytes=N` | 200 | |
| **ID domestic** | `kartolo.sby.datautama.net.id/ubuntu/ls-lR.gz` | 200 | 38.5 MB, resolves 123.255.202.74 |

**Upload — `POST` with a 1 KiB finite body**

| Target | Result |
|---|---|
| `speed.cloudflare.com/__up` | 200, `POST_1024B` |
| LibreSpeed `empty.php` (Amsterdam, Chicago, Helsinki, Prague) | 200, `POST_1024B` |

**Dead — drop from the brief's seed list**

| Target | Failure |
|---|---|
| `speedtest-sgp1/-nyc3/-lon1/-nyc1.digitalocean.com` | **DNS NXDOMAIN (curl exit 6)** — the whole DigitalOcean family is gone |
| `ewr-ping.vultr.com` | DNS NXDOMAIN |
| `speed.cloudflare.com/meta` | **403** — unusable for anything |
| ID `mirror.telkomuniversity.ac.id`, `buaya.klas.or.id` | timeout (exit 28) |
| ID `ubuntu.indika.net.id`, `mirror.ui.ac.id` | DNS NXDOMAIN |
| ID `repo.ugm.ac.id` | TLS failure (exit 35) |

### 2.2 LibreSpeed community list — the decisive finding

Canonical source, found by reading `librespeed-cli`'s source (`speedtest/speedtest.go:29`):
`https://librespeed.org/backend-servers/servers.php` → array of 22 entries with
`name, server, id, dlURL, ulURL, pingURL, getIpURL, sponsorName, sponsorURL`.

**All 22, by geography:** Amsterdam ×2, Argalasti, Atlanta, Belgrade, Chicago, Denver, Frankfurt ×2, Grand Rapids, Helsinki, Las Vegas, London, Los Angeles ×2, New York, Novi Sad, Poznan, Prague ×2, Roma, **Tokyo**.

- **APAC coverage is one server.** No Singapore, no Indonesia, no Hong Kong, no Korea, no Australia.
- **That one server is dead:** `librespeed.a573.net` returns **403 on `garbage.php`, `empty.php`, and `POST`**, served by bare `nginx` with no Cloudflare headers — a server-side block (likely JP-only geofence or datacenter-IP rejection), not a transient error. It may work from your residential Indonesian line; it cannot be relied on.

Liveness sampling of the rest: Amsterdam (Clouvider), Chicago (Sharktech), Helsinki (`librespeed.fi`), Prague (CESNET) all returned **200** for `garbage.php?ckSize=1`, `empty.php` GET, and `empty.php` POST.

**Consequence.** LibreSpeed cannot supply an upload target in ID, SG, or (reliably) JP. This is what makes iperf3 first-class rather than optional.

### 2.3 iperf3 — APAC targets exist and are live

Source: the community-maintained [public-iperf3-servers list](https://github.com/R0GGER/public-iperf3-servers) (207 entries). APAC candidates, TCP-connect verified from this host:

| Group | Host | Port range | Flags | TCP |
|---|---|---|---|---|
| **ID** | `speedtest.tangerang2.myrepublic.net.id` | **9201–9240** | `-R, -u` | **OPEN** (both ends; 157.66.210.199) |
| SG | `sgp.proof.ovh.net` | 5201–5210 | `-R, -6, -u` | OPEN |
| SG | `speedtest.sin1.sg.leaseweb.net` | 5201–5210 | `-R, -6` | OPEN |
| JP | `speedtest.tyo11.jp.leaseweb.net` | 5201–5210 | `-R, -6` | OPEN |
| HK | `speedtest.hkg12.hk.leaseweb.net` | 5201–5210 | `-R, -6` | OPEN |

The **MyRepublic Tangerang** host is a genuine Indonesian domestic iperf3 endpoint advertising both directions — strictly better than my Ubuntu-mirror fallback for the `id` group's upload leg. Leaseweb and OVH hosts are provider-operated, which is a better ToS footing than anonymous IPs.

iperf3 direction semantics: **default = client→server = upload**; **`-R` = server→client = download**. Matches §5 of the brief.

### 2.4 WARP trace endpoint

`GET https://cloudflare.com/cdn-cgi/trace` → 200, `content-type: text/plain`, 15 fields:

```
fl h ip ts visit_scheme uag colo sliver http loc tls sni warp gateway rbi kex
```

Sample: `colo=SIN loc=ID warp=off gateway=off tls=TLSv1.3 http=http/2 kex=X25519MLKEM768`

Every field §9 of the brief wants is present, plus useful extras (`gateway`, `rbi`, `kex`, `sliver`). **HEAD returns 404 — must use GET.**

### 2.5 iperf3 static binaries

`esnet/iperf` latest release: **zero binary assets** — confirmed. The brief's "official static iperf3 binary" does not exist.

Best third-party option — [`userdocs/iperf3-static`](https://github.com/userdocs/iperf3-static) tag **3.21**, 18 assets:

| Our target | Asset | Size |
|---|---|---|
| linux/amd64 | `iperf3-amd64` | 7.4 MB |
| linux/arm64 | `iperf3-arm64v8` | 7.5 MB |
| darwin/amd64 | `iperf3-amd64-osx-15` | 4.9 MB |
| darwin/arm64 | `iperf3-arm64-osx-14` / `-15` | 4.5 / 4.6 MB |
| windows/amd64 | `iperf3-amd64-win.zip` | 1.5 MB |
| **windows/arm64** | **MISSING** | — |

Two consequences: **no upstream checksums are published** (we must pin our own), and **windows/arm64 has no iperf3** (degrade gracefully, §5.6).

---

## 3. Repository layout

```
warpbench/
├── PLAN.md                       ← this file
├── METHODOLOGY.md                ← Phase 2, split out from §5
├── README.md                     ← one-liners, screenshots, asciinema
├── LICENSE                       ← MIT (see open question Q5)
├── servers.json                  ← curated list, served from main
├── schema/servers.schema.json    ← JSON Schema, used by CI and at runtime
├── warpbench.sh
├── warpbench.ps1
├── .goreleaser.yaml
├── .golangci.yml
├── go.mod  go.sum
├── cmd/warpbench/main.go         ← parse flags, pick TTY/non-TTY, run
├── internal/
│   ├── cli/          flag parsing, non-interactive mode, exit codes
│   ├── config/       cache dirs, per-OS paths
│   ├── serverlist/   fetch → cache → embed, schema validation
│   ├── trace/        WARP state verification (§7)
│   ├── netprobe/     ping (ICMP + TCP fallback), httptrace timings
│   ├── throughput/   adapter interface + registry
│   │   ├── librespeed.go
│   │   ├── httpfile.go
│   │   ├── cloudflare.go
│   │   └── iperf3.go     ← + binary download/verify/exec
│   ├── stats/        jitter, p95, median, loss, slow-start trim
│   ├── runner/       phase orchestration, ordering, fairness, ETA
│   ├── chart/        ASCII + box-drawing bar renderers
│   ├── report/       Markdown + JSON, IP masking, --compare merge
│   ├── tui/          bubbletea models: select, progress, results, pause
│   └── version/
├── testdata/         golden files: charts, reports, servers.json fixtures
├── tools/check-servers.sh        ← exists; reused by the weekly Action
└── .github/workflows/
    ├── ci.yml            lint + test on push
    ├── release.yml       goreleaser on tag
    └── validate-servers.yml   weekly revalidation, opens an issue on failure
```

### 3.1 Throughput adapter interface

One interface, four implementations. Adding a protocol must not touch the runner, TUI, or report.

**As built (one deviation from the sketch below).** Latency and HTTP timings live in
`internal/netprobe`, keyed off the server's `ping_host` and a `TimingsURL()`
provided by the adapter, rather than being methods on every adapter. Three of the four
protocols would have shared byte-identical implementations, and iperf3 has no HTTP
surface at all, so putting them on the interface would have meant duplication plus a
method that always returns "unsupported". The adapter is now only about throughput.

```go
type Caps struct{ Ping, Download, Upload, Timings bool }

type Sample struct {
    Metric      string        // "download" | "upload"
    Bytes       int64
    Elapsed     time.Duration
    SteadyBytes int64         // the window the headline came from
    SteadyFor   time.Duration
    SteadyMbps  float64       // slow-start excluded  (headline)
    OverallMbps float64       // whole sample, reported alongside
    Parallel    int
    Proto       string        // "HTTP/1.1" | "HTTP/2.0" | "iperf3"
    RemoteIP    string
    Undersized  bool          // below MinBytes: too short to trust
    Truncated   bool          // the byte guard ended it, not the clock
    Partial     bool          // an error cut the window short after data moved
}

type Adapter interface {
    ID() string
    Caps() Caps
    TimingsURL() string       // small GET target, or "" when there is no HTTP surface
    Download(ctx context.Context, o Opts) (Sample, error)
    Upload(ctx context.Context, o Opts) (Sample, error)
}
```

The runner is protocol-agnostic: it asks for caps, skips unsupported metrics, and records `N/A` with a reason rather than a zero.

**Four things the real endpoints taught us during Phase 4**, each now enforced by a test:

1. **Cloudflare refuses `bytes >= 100,000,000` with a bare 403** — undocumented, found by
   bisection (99,999,999 works, 100,000,000 does not). The payload is now 90 MB.
2. **A finite payload cannot fill a time-governed window.** Cloudflare's ceiling and the
   100 MB static files both end a fast link's sample in a fraction of the window. Downloads now
   repeat the request until the clock expires, each still on a fresh connection, bounded by
   `maxRequestsPerSample`. Live proof: the Cloudflare target moved 112 MB in a 4 s window.
3. **Two LibreSpeed deployments cannot upload at all.** `ams` and `fra`
   (`*.speedtest.clouvider.net`) answer **413 Payload Too Large** above roughly 1 MB — verified
   at 1 MB accepted / 20 MB rejected — while `librespeed.fi`, CESNET and Turris accept 20 MB
   fine. Both Clouvider entries lost the upload capability, and EU's quick-mode upload leg moved
   to CESNET.
4. **A refused upload is not a slow upload.** A non-2xx response to an upload is now a hard
   error rather than a partial sample: reporting a rate from bytes the server threw away would
   be a fabricated number. Transport errors *after* data moved still yield a usable partial
   sample, flagged `Partial` so a shortened window is never presented as a full one.

---

## 4. Server-list schema

Schema bumped to **`2`** to admit iperf3 as a first-class protocol (the brief's `schema: 1` had no place for ports, direction flags, or the binary's trust anchor).

```json
{
  "schema": 2,
  "revision": "2026-09-20",
  "groups": [
    {"id": "id", "name": "Indonesia"},
    {"id": "sg", "name": "Singapore"},
    {"id": "jp", "name": "Tokyo"},
    {"id": "eu", "name": "Europe"},
    {"id": "us", "name": "United States"}
  ],
  "servers": [
    {
      "id": "sg-linode",
      "group": "sg",
      "name": "Linode Singapore",
      "provider": "Linode",
      "city": "Singapore", "country": "SG",
      "protocol": "http-file",
      "ping_host": "speedtest.singapore.linode.com",
      "download_url": "https://speedtest.singapore.linode.com/100MB-singapore.bin",
      "upload_url": null,
      "capabilities": ["ping", "download", "timings"],
      "tier": "quick",
      "adapter_opts": {"min_bytes": 25000000, "max_bytes": 500000000},
      "notes": ""
    },
    {
      "id": "id-myrepublic-iperf3",
      "group": "id",
      "name": "MyRepublic Tangerang (iperf3)",
      "provider": "MyRepublic",
      "city": "Tangerang", "country": "ID",
      "protocol": "iperf3",
      "ping_host": "speedtest.tangerang2.myrepublic.net.id",
      "download_url": null,
      "upload_url": null,
      "capabilities": ["ping", "download", "upload"],
      "tier": "quick",
      "iperf3": {"host": "speedtest.tangerang2.myrepublic.net.id", "ports": [9201, 9240], "reverse_download": true},
      "notes": "Domestic ID path. Advertises -R and -u. Community-listed; treat as best-effort."
    },
    {
      "id": "id-cf-cgk",
      "group": "id",
      "name": "Cloudflare edge (CGK) [footnoted]",
      "provider": "Cloudflare",
      "city": "Jakarta", "country": "SG",
      "protocol": "cloudflare",
      "ping_host": "speed.cloudflare.com",
      "download_url": "https://speed.cloudflare.com/__down?bytes=250000000",
      "upload_url": "https://speed.cloudflare.com/__up",
      "capabilities": ["ping", "download", "upload", "timings"],
      "tier": "quick",
      "flags": ["footnote:cloudflare-edge"],
      "notes": "With WARP on this path never leaves Cloudflare; measures ISP->nearest-edge only."
    }
  ]
}
```

**Field rules**

- `protocol` ∈ `librespeed | http-file | cloudflare | iperf3`. Required.
- `capabilities` is authoritative; adapters assert it at load time and the loader rejects a server whose declared protocol cannot deliver a declared capability.
- `tier` ∈ `quick | extended`. Quick mode runs `quick` servers only.
- Groups are **data, not code** — the TUI derives its grouping from the file, so a new group needs no release.
- `flags[]` drives report footnotes and TUI badges; `footnote:cloudflare-edge` is the only one required in v1.
- `ping_host` may differ from the download host (Cloudflare, iperf3) — that is why it is separate rather than derived.

**Loading order:** remote (`raw.githubusercontent.com/rpratama123/warpbench/main/servers.json`, 30 s timeout, `If-None-Match`) → on-disk cache → **embedded copy compiled into the binary**. `--servers <path|url>` overrides all three. Every load is validated against `schema/servers.schema.json`; invalid entries are skipped with a warning naming the entry and the reason, never silently dropped. The report records which source won and the `revision` string.

### 4.1 Curated v1 targets

**`id`** — Cloudflare CGK edge (footnoted), MyRepublic Tangerang iperf3, datautama Ubuntu mirror (download-only domestic control).

I am deliberately including the datautama mirror even though it is an Ubuntu mirror rather than a speedtest service: it is the **only verified-alive domestic non-Cloudflare path**, and it is what proves WARP is not simply "faster everywhere". Its use is bounded — one ranged request per sample, byte cap applied, `User-Agent: warpbench/<version>` identifying ourselves, and no retry storms. It is marked `tier: quick` and flagged so the report explains what it is.

**`sg`** — Linode, Vultr, Hetzner (download+ping); `sgp.proof.ovh.net` and `speedtest.sin1.sg.leaseweb.net` iperf3 (upload).
**`jp`** — Linode, Vultr (download+ping); `speedtest.tyo11.jp.leaseweb.net` iperf3 (upload); LibreSpeed Tokyo only if a residential-connection probe passes, else omitted.
**`eu`** — Hetzner ×3, Linode Frankfurt, Vultr ×2 (download+ping); LibreSpeed Amsterdam/Chicago-class hosts for upload; Prague/Helsinki as extra LibreSpeed upload sources.
**`us`** — Linode Newark/Fremont, Vultr LAX/NJ, Hetzner Ashburn/Hillsboro (download+ping); LibreSpeed Sharktech Chicago/LA/Denver/Las Vegas, Clouvider Atlanta/LA/NY for upload.

This yields **upload coverage in every group**, which the brief wanted and which the original seed list could not deliver.

---

## 5. Test methodology

Written to be extractable verbatim into `METHODOLOGY.md` in Phase 2.

### 5.1 Per server, per phase — sequential

Never two servers concurrently. The only concurrency is the explicit multi-stream run within a single server (`--parallel N`), which is reported as a separate labelled series and never mixed into the single-stream headline.

| Metric | Method | Quick | Extended |
|---|---|---|---|
| ICMP RTT min/avg/max/p95 | pro-bing, 200 ms interval, 2 s per-packet timeout; first sample discarded | 10 | 30 |
| Jitter | **Both** mean absolute consecutive difference (RFC 3550 style) **and** standard deviation | same samples | same |
| Packet loss % | sent vs received | same | same |
| DNS / TCP connect / TLS / TTFB | `httptrace` on a small GET; median of N; record resolved IP and TLS version | 3 | 7 |
| Download Mbps | Stream to `io.Discard`, wall-clock governed; single stream | 1 × 12 s | median of 3 × 15 s |
| Upload Mbps | `POST` from a `crypto/rand`-seeded reader (no disk), chunked, wall-clock governed | 1 × 8 s | median of 3 × 10 s |

**ICMP fallback.** If ICMP is unavailable (blocked, or `ping_group_range` excludes the user on Linux), fall back to **TCP connect RTT to port 443**, explicitly labelled `TCP RTT` in every report cell and chart axis. Never present a TCP RTT as an ICMP result. On Linux, print the one-line `sysctl` fix but **do not apply it** (would require root).

### 5.2 Throughput measurement — two corrections to §6 of the brief

**(a) Time governs, bytes are only a safety valve.** The brief specifies both a duration *and* a byte cap ("10 s or ~25 MB"). As an either/or that is unworkable on fast links: 25 MB at 300 Mbps is 0.7 s — far too short to mean anything. So: the **duration is authoritative**, the byte cap is a high runaway guard (default 500 MB), and a `min_bytes` floor (default 25 MB) marks a sample as undersized so the report can flag it rather than quietly publishing a 0.7 s average.

**(b) Exclude TCP slow start.** Averaging from the first byte mixes slow-start ramp with steady-state capacity, systematically understating fast links. Report **steady-state excluding the first 1.0 s** as the headline, with the whole-sample average shown alongside as `overall`. This is a methodology improvement that a reviewer will otherwise ask for, and it costs nothing to capture both.

Mbps = `bytes × 8 / seconds / 1e6`. MB/s shown alongside. Every throughput sample opens a **new HTTP connection** (no keep-alive across samples). HTTP/2 is disabled for throughput samples unless the server offers only h2; the protocol actually used is recorded per sample.

iperf3 samples are run with `-J` and parsed from JSON (`end.sum_received.bytes`, `end.sum_sent.bytes`, `end.sum_received.bits_per_second`) so numbers come from iperf3's own accounting rather than our wall clock. `--connect-timeout`, a per-port retry walk, and `--omit 1` (iperf3's native slow-start exclusion — this is exactly the same correction as (b), done by the tool itself).

### 5.3 Fairness rules

- Identical server order in both phases.
- Identical payload caps and sample counts in both phases.
- New connection per sample; no connection reuse across samples.
- The target address family is pinned per run (§5.5).
- Any server that fails in one phase and succeeds in the other is **reported**, not dropped — that asymmetry is itself a result about WARP.

### 5.4 Phase structure and time-of-day drift

Full baseline → **pause** → full WARP. Interleaving is impossible because toggling is manual, which is exactly why drift must be handled:

- Print wall-clock start/end and local timezone for each phase.
- **Warn if the inter-phase gap exceeds 10 minutes** — and print the gap in the report's caveats regardless.
- Support `--phase baseline|warp` + `--compare a.json b.json` so halves can be run at the same time of day on different days. `--compare` merges any two JSON files, including a three-file baseline→WARP→baseline sandwich.
- Record the server-list `revision` per phase; refuse to compare two files with different revisions unless `--force` (a changed server list invalidates the comparison).

### 5.5 Address family

**IPv4 default, `--ipv6` opt-in.** This requires forcing the dialer to `tcp4` — merely not passing `--ipv6` is not enough, because several targets are AAAA-first: `speedtest.singapore.linode.com`, `speedtest.frankfurt.linode.com`, `speedtest.newark.linode.com`, `speedtest.fremont.linode.com`, `fsn1-/hel1-/nbg1-/sin-/ash-/hil-speed.hetzner.com` all returned an IPv6 address first in `getent` on this host. Go's dialer would silently use IPv6 and the "IPv4 default" claim would be false. The family actually used is recorded per server per phase, and the resolved IP is displayed in both phases so anycast/route changes under WARP are visible.

### 5.6 Capability gaps as first-class output

When a group has no upload target, the TUI and report say **`N/A (no upload target in this group)`** — never `0`, and never omitted silently. windows/arm64 has no iperf3 binary (§2.5) and therefore no iperf3-derived APAC upload; the binary detects this, explains it once, and marks affected cells `N/A (iperf3 unavailable on windows/arm64)`.

### 5.7 Duration budget — reconciling §6 with the brief's targets

The brief's caps cannot produce its stated totals across **two** phases. Arithmetic, per server, with ping + timings included:

| Mode | Scope | Per server per phase | Phase total | **Both phases** |
|---|---|---|---|---|
| Brief's Quick (2 samples, 10 s + 8 s) | 10 servers | ~39 s | ~6.5 min | **~14 min** (brief says 4–6) |
| Brief's Extended (3 × 30 s + 3 × 20 s) | 12 servers | ~160 s | ~32 min | **~64 min** (brief says 20–30) |
| **Plan Quick, as shipped** (1 sample, 12 s + 8 s) | 2/group = 10 | ~20 s | ~3.3 min | **~7 min** ✓ |
| **Plan Extended, as shipped** (3 × 15 s + 3 × 10 s) | whole list = 32 | ~70 s | ~38 min | **~75 min** |

The plan's caps are therefore: **Quick = 2 servers per group, 1 download sample (12 s), 1 upload sample (8 s), 10 pings. Extended = 2 servers per group, median of 3 (15 s / 10 s), 30 pings.**

**Corrections made as the phases landed.**

*Phase 3 — quick tier.* Quick was specified as 1 server per group, which cannot satisfy the simultaneous requirement of an upload target per group: no single server covers both the download and upload legs in most groups. Quick therefore ships 2 servers per group — one download-led, one upload-led — giving 10 servers and ~7.6 minutes for both phases, against the original 5–6 minute estimate.

*Phase 5 — extended tier.* The 29-minute figure assumed 2 servers per group. Extended mode actually runs the **whole list** (32 servers), because that is what the `tier` field means: it marks which targets suffice for a fast signal, not which a thorough run may not touch. That is **~75 minutes for both phases**, not 29. Both figures above are now computed from the shipped list rather than assumed, and the runner advertises the real number from the live selection before it starts, so a user is never surprised mid-run. If a shorter thorough run is wanted, select groups explicitly or lower the caps; the trade-off is visible rather than hidden. `--extended` on a custom selection of 20 servers will legitimately take ~55 min — so the estimate is **computed live from the actual selection** and displayed, never hard-coded. If you would rather keep the brief's 30 s/100 MB extended caps, Extended becomes a ~60 min run; that is a real trade-off and I have defaulted to the faster one.

### 5.8 Once per phase

Trace result (§7), OS and version, binary version, server-list revision, local time with timezone, wall-clock start/end, and the full effective configuration (so a report can be reproduced from the file alone).

---

## 6. TUI flow

bubbletea. Verified interactive console required before any TUI is drawn — otherwise fall back to non-interactive automatically (this is the `irm | iex` and redirected-stdout case).

1. **Header** — name, version, OS/arch, terminal capabilities, one-line non-blocking update notice.
2. **Pre-flight** — fetch server list showing **source** (remote/cache/embedded) and revision; trace check showing `warp=`, colo, masked IP; ICMP capability probe with fallback notice.
3. **Mode** — Quick / Extended / Custom, with live total-duration estimate (§5.7) that updates as servers are toggled.
4. **Server selection** — checklist grouped by geo. `↑/↓` or `j/k` move; `Space` toggle server; `a` toggle group under cursor; `A` all; `n` none; `←/→` collapse/expand; `Enter` continue; `q` quit. Per-server capability badges (`ping dl ul`), per-group `[selected/total]`. At least one server required.
5. **Baseline phase** — **refuses to start if `warp=on|plus`**; explicit override allowed and recorded in the report. Progress table: one row per server, live cells (`ping 42 ms · jit 3.1 · dl 41.2 Mbps · ul 9.8 Mbps`), overall progress bar, ETA, current action line.
6. **Pause** — "Turn on Cloudflare WARP now, then press Enter." Then poll the trace endpoint up to 5 times, 3 s apart, until `warp=on|plus`. Show colo and new masked IP. Refuse to proceed while `warp=off` unless explicitly overridden. Mode hints: `plus` = WARP+, `gateway=` present = Zero Trust, **DoH-only mode also reports `warp=off`** — explain that specifically, because it is the most likely cause of a user being stuck here.
7. **WARP phase** — identical run; re-check trace at the end and **flag results if state changed mid-run**.
8. **Results** — one block per metric; two horizontal bars per server (ISP, WARP) with values and Δ/Δ%. Δ coloured green when WARP is better **according to the metric's direction** (lower is better: latency, jitter, loss, timings; higher is better: throughput). Then a summary, e.g. *"WARP improved download on 7/9 servers (median +138%), upload on 6/8 (median +95%), latency worsened by median +6 ms, jitter improved on 8/9."* Both trace lines and phase timestamps shown. Tabs/paging when the terminal is short.
9. **Save prompt** — `Save report? [Y/n]` → `./warpbench-YYYYMMDD-HHMM.md` + `.json`. `--out` overrides. Paths printed.

**Terminal handling.** Width < 80 → shrink bars, **never wrap a bar line**. `--no-color`, `NO_COLOR`, `TERM=dumb`, or non-TTY stdout → plain-ASCII mode. Windows conhost on Windows 10 needs VT mode; enable it via `x/term` and **verify on conhost, not only Windows Terminal**. Ctrl-C restores the terminal and offers to save partial results.

**Non-interactive flags.** `--quick|--extended`, `--groups`, `--servers`, `--yes`, `--phase`, `--compare`, `--out`, `--no-color`, `--no-tty`, `--ipv6`, `--parallel N`, `--no-update-check`, `--no-mask`, `--json`, plus two this plan adds: `--iperf` (force-enable the iperf3 module) and `--no-iperf` (force-disable it, e.g. air-gapped or policy-blocked).

---

## 7. WARP state verification

`GET https://cloudflare.com/cdn-cgi/trace` — follow redirects, 10 s timeout, **no caching**, **GET (not HEAD — HEAD returns 404)**.

Parse `warp=`, `colo=`, `ip=`, `gateway=`, `loc=`, `ts=`, `http=`, `tls=` and additionally `kex=` and `rbi=` (free, and `kex` is genuinely informative about whether WARP is intercepting). Values other than `off|on|plus` are treated as **unknown** and reported as such rather than coerced to `off`.

Sampled at: pre-flight, immediately before baseline, immediately before WARP, end of WARP phase. Every result is recorded in the JSON, not just the last.

**Not used:** `speed.cloudflare.com/meta` — verified **403** (§2.1).

---

## 8. Report format

**Markdown** — title; versions; OS; phase timestamps; per-phase trace summary (IP masked unless `--no-mask`); one table per metric (server, ISP, WARP, Δ, Δ%); one ASCII bar chart per metric; summary paragraph; caveats (Cloudflare-edge footnote, iperf3 best-effort targets, domestic-mirror control, overrides, ICMP fallback, state changes, inter-phase gap); methodology link; and a **reproduce block** with the exact command line and server-list revision.

Bars are plain ASCII so they paste into GitHub and a blog without mangling:

```
download (Mbps)                    ISP        WARP        Δ
sg-linode                    ████████ 41.2  ████████████████ 118.4  +187%
jp-vultr                     ██████ 33.8    █████████████ 96.1      +184%
id-myrepublic-iperf3         ████████████████ 92.4  ██████████████ 88.7  -4%
eu-hetzner-fsn1              ██ 12.1        ████████████████ 84.0  +594%
```

**JSON** — `schema` field, all raw samples (not just medians), both trace sequences, effective config, and server-list revision, so later versions can re-render and `--compare` can merge two `--phase` files.

**Privacy** — public IP masked by default (`--no-mask` to disable). ASN/ISP name only from trace-endpoint fields; **no third-party geolocation API**.

---

## 9. Release pipeline

### 9.1 goreleaser

Tag `v0.1.0`… → per-OS/arch binaries (`linux/{amd64,arm64}`, `darwin/{amd64,arm64}`, `windows/{amd64,arm64}`), `SHA256SUMS`, optional cosign signatures, auto-generated changelog. All builds `CGO_ENABLED=0`, `-trimpath`, and `-ldflags "-s -w -X …/version.Version={{.Version}}"` so the binary can report and self-check its own version.

### 9.2 Launcher hosting

`raw.githubusercontent.com/rpratama123/warpbench/main/warpbench.sh` and `.ps1`, fronted by your short link. README one-liners:

```sh
# Linux / macOS
curl -fsSL <short-link>/sh | bash

# Windows
irm <short-link>/ps1 | iex
```

Plus a documented **"download, inspect, verify, run"** path and direct binary downloads. Launchers default to the latest release, honour `WARPBENCH_VERSION` / `$env:WARPBENCH_VERSION`, and skip re-download when the cached binary's checksum already matches.

### 9.3 Server-list updates need no release

Served from `main`; the report records the revision. Weekly `validate-servers.yml` runs `tools/check-servers.sh` over every entry (ping host resolves, download URL 200, upload endpoint accepts a 1 KiB POST, iperf3 port accepts TCP) and opens an issue on failure. The same script validated §2, so the weekly job tests the code path that actually ships.

### 9.4 iperf3 binary: trust model — **decided: option A** (2026-09-20)

ESnet ships no binaries, so the choice is:

| Option | Pros | Cons |
|---|---|---|
| **A. Pin `userdocs/iperf3-static` 3.21 by SHA256 compiled into our binary** | Works today, all platforms we need except win/arm64, hashes cannot be tampered with via `servers.json`, downloaded **lazily** only when iperf3 is used | Third-party binary trust; upstream publishes no checksums, so we own the hashes; no win/arm64 |
| **B. Build iperf3 from esnet source in our own release workflow, publish as our own artifact** | Removes third-party trust entirely; we control and publish checksums; closes the win/arm64 gap | Adds a C toolchain + static-link step to CI; larger release surface |
| C. Hand-written pure-Go iperf3 client | No external binary at all | **Rejected.** A benchmark's credibility rests on using the reference implementation; numbers produced by a bespoke client are exactly what a skeptical reviewer will question. |

**Decision (2026-09-20): option A.** Third-party pre-compiled binary is acceptable. Ship v1 pinned to `userdocs/iperf3-static` 3.21 by SHA256 compiled into our own binary; revisit option B only if the win/arm64 gap or upstream trust becomes a problem.

### 9.5 Signing and platform trust

Unsigned binaries trip SmartScreen for **browser** downloads but not for launcher downloads; document it anyway, publish checksums, and add cosign signatures if feasible. Binaries fetched via `curl` on macOS **do not** get `com.apple.quarantine`, so Gatekeeper is not an issue — document notarization as future work. On Windows, antivirus may sandbox a freshly downloaded binary for a few seconds: **retry launch once** before reporting failure.

---

## 10. Quality bar and verification

**Before declaring done** (unchanged from §13 of the brief, which is the right bar):

- Both launchers end-to-end, quick mode, on real networks: Windows PS 5.1 **and** PS 7, macOS, Linux — Markdown reports pasted into the PR.
- Simulated failures: release download fails; checksum mismatch; server list unreachable; one server dies mid-run; ICMP blocked; `--no-tty`; 60-column terminal; Ctrl-C during upload; WARP not on at the pause; WARP dropping during phase 2; `--compare` of two `--phase` files.
- Every `servers.json` entry verified by script (reused by the weekly Action).
- `go test ./...` with coverage for stats, chart rendering, report rendering, and JSON schema round-trip.
- `shellcheck`, `PSScriptAnalyzer`, `golangci-lint`, `go vet` clean in CI on every push.

**Test methodology for the tool itself.** Table-driven unit tests with golden files in `testdata/`: chart rendering at fixed widths (40/60/80/120 columns) asserting no wrapped bar lines; report rendering from fixture JSON; `--compare` merges including mismatched revisions; stats against hand-computed fixtures (jitter both definitions, p95, median, slow-start trim). Network code is tested against `httptest.Server` and a fake adapter registered in the `throughput` registry, so the runner's ordering, fairness, and `N/A` handling are testable without a network. `tools/check-servers.sh` is exercised in CI against `httptest`-style local fixtures.

---

## 11. Risks

| # | Risk | Impact | Mitigation |
|---|---|---|---|
| R1 | **LibreSpeed APAC absence** (verified) | No upload numbers in ID/SG without iperf3 | iperf3 first-class (§2.3); `N/A` is explicit (§5.6) |
| R2 | **Public iperf3 servers are best-effort** and often busy | Missing/erratic upload samples | Multi-port retry walk, `--connect-timeout`, per-sample error capture, `N/A` over zeros, targets labelled best-effort in the report |
| R3 | **Third-party iperf3 binary** (no upstream checksums, no win/arm64) | Supply-chain trust; platform gap | Pin SHA256 in-binary, lazy download, graceful degrade, option B in §9.4 |
| R4 | **Provider ToS drift** on Linode/Vultr/Hetzner speedtest hosts | Targets disappear or rate-limit | Weekly validation job, `User-Agent` identifying us, no retry storms, `servers.json` updatable without a release |
| R5 | **Domestic mirror use is impolite** (datautama) | Reputational; possible blocking | Single ranged request per sample, byte cap, identifying UA, documented purpose, first to be removed if it becomes unreliable |
| R6 | **Time-of-day drift** between phases | Confounds the headline result | Phase timestamps, >10 min warning, `--phase`/`--compare`, reproducible sandwich runs |
| R7 | **Cloudflare `__up` with WARP never leaves Cloudflare's network** | A Cloudflare upload number is not evidence about your ISP | Footnoted in the report; iperf3 is the primary APAC upload source wherever available |
| R8 | **Unprivileged ICMP unavailable** (esp. Linux `ping_group_range`) | Latency series silently becomes a different metric | Detect, fall back to TCP RTT, label every cell, print the sysctl fix without applying it |
| R9 | **IPv6 silently preferred** on AAAA-first hosts | "IPv4 default" claim is false | Force `tcp4` unless `--ipv6`; record and display family + resolved IP per phase |
| R10 | **WARP states that are not `on`** (WARP+, Zero Trust, DoH-only) | User stuck at the pause, or wrong conclusion | Explain `plus`/`gateway=`/DoH-only explicitly; refuse unless overridden; record the override |
| R17 | **The measurement HTTP client refuses redirects, which breaks a release download** | The iperf3 binary could not be fetched (bare 302) | Asset downloads use a separate redirect-following client; the measurement client keeps refusing redirects so a sample cannot silently change host. Both are now pinned by tests. |
| R19 | **A release created by `GITHUB_TOKEN` starts no further workflows** | A post-release verification workflow keyed on `on: release` would silently never run, so nothing would ever confirm the install path works | The release workflow calls the verification explicitly as a reusable workflow. It also means a release that fails verification reports as a failed release workflow. |
| R21 | **Treating `latest` as an immutable tag** | A user who ran the launcher once kept running that build forever, so a released bug fix never reached them; found by asking, and reproduced against a fake release whose contents changed | The launcher re-reads `SHA256SUMS` on every run for `latest` (a few hundred bytes) and re-downloads only when it changed. A pinned tag stays immutable. An unreachable release still falls back to a verified cached copy. |
| R22 | **A ping that received nothing was treated as a measurement of zero** | Targets that never answered appeared as `0.0 ms` latency and `100%` loss rows, and their `+0.00` deltas were counted in the summary's median, which understated WARP's real latency penalty by roughly 3.8× on the first real Windows run (`+1.3 ms` reported against `+4.8 ms` over the targets that replied) | Presence is now "at least one reply" rather than "the ping object exists", applied to latency, jitter and loss together. A target silent in both phases is named in the caveats instead of charted; a target silent in only one phase is still reported, as the asymmetry it is. |
| R23 | **The Windows console *code page*, not its VT support, destroys the charts** | Go writes console output by converting to UTF-16 and calling `WriteConsoleW`, so Windows re-encodes it into the active code page with best-fit mapping. U+2588 and U+2591 have no mapping in a legacy page and both best-fit to `¦`, so every bar became an identical run: the chart looked like data while carrying none. The arrow glyphs became `?`. Found on the first real Windows 10 run | The glyph set follows `GetConsoleOutputCP()`: a console that is not UTF-8 gets ASCII bars, `^`/`v` arrows and ASCII progress markers, chosen as a set because they fail as a set. The code page is read and never set, so the user's console is left as it was found; `chcp 65001` opts in to the block charts, and the Markdown report is ASCII end to end regardless. |
| R20 | **Third-party CLI flags change under us** | cosign deprecated `--output-signature`/`--output-certificate` for `--bundle`, which broke a release, and goreleaser's `signature` template defaults to a filename cosign no longer writes | The release workflow preflights the flags it depends on before building, and actionlint runs in CI so a malformed workflow fails on push rather than on a tag. |
| R18 | **Reporting measurement noise as a finding** | A 0.01 ms latency change reported as a regression destroys a published report's credibility | Deltas below a 0.5% relative threshold (0.05 pp for loss) read as "same", and the headline and the table share one `Verdict` so they cannot disagree. |
| R11 | **`servers.json` is remote input** | Supply-chain / DoS | Schema validation, strict size limit, no code execution, embedded fallback, HTTPS only |
| R12 | **PS 5.1 TLS defaults below 1.2** | Opaque launcher failure on older Windows | Explicit `SecurityProtocol` including `Tls12` |
| R13 | **`curl \| bash` truncation** | Partially-executed installer | `main "$@"` on the last line; `set -eu`; ERR trap |
| R14 | **Windows conhost lacks VT** (Win10) | Garbled TUI | `x/term` enablement + explicit conhost testing, not just Windows Terminal. The console's character set is a separate failure mode that survives VT being enabled — see R23 |
| R15 | **Measuring a public path distorts it** (large repeated transfers can be shaped or throttled) | Results not representative | Time-governed caps (§5.7), slow-start exclusion, ≥2 samples in Extended, full raw samples published for independent scrutiny |
| R16 | ~~Repo owner/name unconfirmed~~ | — | **Resolved: `rpratama123/warpbench`.** |

---

## 12. Decisions and remaining questions

**Answered by you (2026-09-20), now baked in:**

| # | Decision |
|---|---|
| — | Indonesian target = Cloudflare CGK edge + datautama Ubuntu mirror (download-only control) |
| — | iperf3 = **first-class in v1**, used as the APAC upload source |
| — | brew/scoop = deferred to v1.1 |
| **Q1** | Repo = **`rpratama123/warpbench`** |
| **Q2** | iperf3 trust model = **option A**, pin the third-party pre-compiled binary by SHA256 |
| **Q4** | Extended caps = **plan's tuned caps** (3 × 15 s download, 3 × 10 s upload, 2 servers/group, ~29 min for both phases) |

**Still open — with my recommended default so none of these blocks Phase 2:**

| # | Question | Recommendation |
|---|---|---|
| Q3 | windows/arm64 has no iperf3 — acceptable to degrade, or must option B land to close it? | Acceptable to degrade with an explicit `N/A`; B closes it later. |
| Q5 | Licence — MIT or Apache-2.0? | **MIT** (matches the audience; Apache-2.0 only if you want a patent grant). |
| Q6 | Keep the datautama Ubuntu mirror, given R5? | **Keep** — it is the only verified domestic non-Cloudflare control, and it is what makes the "WARP helps international, not domestic" claim testable. |
| Q7 | ~~Short-link service for the README one-liners~~ | **Answered: `rullypratama.com/warpbench.{sh,ps1}`**, a single 301 to the canonical files. Both are exercised by the release verification workflow. |

---

## 13. Non-goals (v1)

Unchanged from §15: no automatic WARP toggling, no root/admin, no Ookla/Speedtest.net, no scheduled or continuous monitoring, no GUI, no central result upload, no third-party geolocation APIs. Plus, given §2.5: **no pure-Go iperf3 reimplementation** (§9.4 option C).

---

## 14. Proposed phases

| Phase | Deliverable | Gate |
|---|---|---|
| **1** | `PLAN.md` | **done — approved 2026-09-20** |
| 2 | Repo scaffold, `go.mod`, launchers (`sh` + `ps1`), CI lint/test workflows | **done — 25/25 launcher checks green, CI green on 3 OSes** |
| 3 | Server list: schema, loader, cache, embedded fallback, `servers.json` v1, validation Action | **done — 74/74 checks green** |
| 4 | Measurement: `stats`, `netprobe`, four throughput adapters incl. iperf3 download/verify/exec | **done — 6 packages tested, live smoke run passed** |
| 5 | Runner + `--phase`/`--compare` + JSON schema | Deterministic ordering; fairness assertions in tests |
| 6 | TUI (selection, progress, pause, results) + ASCII fallback | **done — 60-column and `--no-tty` tests; live pty check** |
| 7 | Reports (MD + JSON), masking, footnotes, `METHODOLOGY.md`, README | **done — golden-file test; live report produced** |
| 8 | goreleaser, cosign, signing docs, `v0.1.0` | **done — snapshot contract checked in CI; published release verified** |

---

## 15. What I need from you

1. ~~Approval of this plan~~ — **approved 2026-09-20**, with Q1/Q2/Q4 answered.
2. ~~Q1, Q2, Q4~~ — **answered**; see §12.
3. Remaining optional choices **Q3** (windows/arm64 iperf3), **Q5** (licence), **Q6** (datautama mirror), **Q7** (short link). None blocks Phase 2.

**All eight phases are complete, and `v0.1.0` is published and verified.** The tool measures, compares, reports and
releases. Further work is listed under §12 (open questions Q3, Q6, Q7) and the
v1.1 candidates: self-built iperf3 (option B in §9.4, which would close the
windows/arm64 gap), `brew`/`scoop` install paths, and macOS notarization.
