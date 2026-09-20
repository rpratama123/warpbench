# Methodology

How warpbench measures, why it measures that way, and what the numbers do and
do not support.

This document is the method. If a published result disagrees with it, the result
is wrong.

---

## 1. The question

Some ISPs have congested international uplinks: overseas browsing and downloads
degrade at certain hours while domestic traffic stays fine. Turning on Cloudflare
WARP appears to help, but that claim is usually anecdotal.

warpbench answers it by measuring the **same targets, the same way, twice**: once
on the raw ISP path, once with WARP enabled. Everything below exists to make
those two halves comparable and to make the comparison auditable.

## 2. The short version

1. Run one phase on the raw path, save it.
2. Turn WARP on. The tool verifies from the network that it is really on.
3. Run the same phase again, save it.
4. Compare.

Nothing is interleaved, because WARP is toggled by hand. That is a deliberate
limitation, and §8 explains how its cost is bounded.

## 3. Targets

Targets come from [`servers.json`](servers.json), grouped Indonesia → Singapore
→ Tokyo → Europe → United States. Each declares a protocol, an explicit set of
capabilities, and a tier.

Four protocols are used:

| Protocol | Upload | Why it is here |
|---|---|---|
| `librespeed` | yes | Open-source backend, trivial to implement correctly, community-run hosts on several continents |
| `http-file` | no | Static files from provider speedtest hosts. The most stable targets, used to cross-check the others |
| `cloudflare` | yes | Cloudflare's own speed endpoints. **Not an end-to-end measurement** — see §7 |
| `iperf3` | yes | The reference implementation, run as a real subprocess. Carries upload coverage in APAC, where LibreSpeed has no usable server |

Every entry is revalidated weekly by a scheduled job, which opens an issue when
a target stops responding. Every result records the list `revision` it was
measured against, so a result can always be attributed to an exact set of
targets.

## 4. Metrics

Per target, per phase, measured **sequentially**: no two targets are measured at
the same time. The only concurrency is the explicit multi-stream run described
in §4.5, which is reported as its own series.

### 4.1 Latency

ICMP echo, 200 ms between probes, 2 s per-probe timeout. **The first reply is
discarded**: it includes cold-path costs — ARP, route lookup, first-hop queueing
— that later replies do not.

Reported: min, mean, median, max, p95, standard deviation, and jitter. Counts
are 10 probes in quick mode and 30 in extended.

p95 uses the **nearest-rank** definition. With small sample counts,
interpolation can report a latency that was never observed; nearest-rank cannot.

**Loss counts every probe.** Because the cold first reply is discarded, loss is
computed from probes sent against replies received, never from the trimmed set.
Deriving it from the trimmed set would report 25% loss for a perfect four-probe
run, since a discarded sample and a lost one look identical.

**If ICMP is unavailable**, latency falls back to TCP connect time to port 443
and is labelled `tcp` in every result. This is a different quantity measured at a
different layer; it is never presented as, or compared with, an ICMP round trip.
On Linux, unprivileged ICMP requires `net.ipv4.ping_group_range` to include your
group; the tool prints the fix and does not apply it.

### 4.2 Jitter

Two definitions are reported, because they disagree in an informative way:

- **Mean absolute consecutive difference** (RFC 3550 style).
- **Population standard deviation.**

Which one is larger depends on the shape of the disturbance. For a single spike
the standard deviation dominates (196 ms vs 122.5 ms on the reference fixture);
for even oscillation jitter dominates (40 ms vs 19.6 ms). A report that showed
only one of them could rank two very different links the wrong way round.

Standard deviation is the **population** figure: these samples are the whole
measurement window, not a sample drawn from a larger population.

### 4.3 Packet loss

Probes sent against replies received, as a percentage. A run where every probe
was lost is a successful measurement of 100% loss, not an error — total loss is a
finding, and is reported as one.

### 4.4 Connection setup

DNS, TCP connect, TLS handshake and time-to-first-byte, taken from Go's
`httptrace` on a small ranged GET (1 KiB by default), reported as the median of N
samples (3 quick, 7 extended).

Each probe **forces a fresh connection**, because connection reuse would make the
setup phases vanish from the measurement rather than measure them.

Two caveats are recorded rather than hidden:

- A resolver or OS-level DNS cache means the DNS phase can measure as zero.
- A phase that did not happen (no TLS on a plaintext URL) and a phase that
  finished faster than the platform clock resolves **both measure as zero**. The
  result file therefore records whether each phase *ran*, separately from how
  long it took, so the two can be told apart.

### 4.5 Throughput

Download streams to a discard sink; upload POSTs a random payload that never
touches disk. Both are **time-governed**: the duration is the authoritative
measurement window.

| | Quick | Extended |
|---|---|---|
| Download | 1 × 12 s | median of 3 × 15 s |
| Upload | 1 × 8 s | median of 3 × 10 s |

Three details matter:

**Bytes are a guard, not the window.** A byte cap alone would end a fast link's
sample after a fraction of a second, which measures almost nothing. The cap
exists only to stop a runaway transfer, and a sample that hits it is flagged as
`truncated` rather than being presented as a clean result.

**Slow start is excluded.** Averages taken from the first byte mix the TCP
ramp-up with steady-state capacity and systematically understate fast links. The
headline rate excludes the **first 1.0 s**; the whole-sample average is reported
alongside it, so the exclusion is auditable rather than invisible. iperf3
performs the same exclusion natively via `-O 1`.

**Upload payloads are random**, not zeros. A transparent compressor on the path
would otherwise inflate the measured rate — and the congested international
links this tool exists to measure are exactly where that is most likely.

`Mbps` is bytes × 8 / seconds / 1e6: decimal, because network rates are decimal.
A "100 Mbps" link that reported 104.9 Mbps through binary megabits would look
like a measurement error.

**Fairness rules, applied to every sample:**

- A new connection per sample; no keep-alive reuse.
- HTTP/2 is not attempted, so a sample cannot be silently multiplexed. The
  protocol actually used is recorded per sample.
- Redirects are not followed, so a sample cannot silently change host.
- Proxies are ignored. Measuring through a proxy measures the proxy, not the
  ISP path, and a stray `HTTP_PROXY` would silently invalidate a result.
- Multi-stream runs (`--parallel N`) are reported as their own series and are
  never merged into the single-stream headline.

### 4.6 iperf3

Run as the real reference binary, pinned by version and SHA-256. Untranslated
JSON output (`-J`) is used, so the figures come from iperf3's own accounting
rather than from a wall clock outside it. Public iperf3 servers advertise a
range of ports precisely because individual ports are often busy, so a few ports
are tried in order.

A target that **refuses** an upload — some LibreSpeed deployments answer
`413 Payload Too Large` above roughly a megabyte — produces an error rather than
a rate. Reporting a rate from bytes the server discarded would be a fabricated
number.

ESnet publishes no binaries, so the pin uses a third-party static build. That
build publishes no checksums either, which is why the SHA-256 is compiled into
warpbench: a hash fetched from the same place as the binary would prove nothing.
The cached copy is re-verified before every run.

## 5. WARP state verification

The two phases only mean something if they really were the raw path and a WARP
tunnel. Rather than trusting that a switch was flipped, the state is read from
Cloudflare's connection-trace endpoint at four points per run.

`warp=off`, `on` and `plus` are recognised. **Anything else is reported as
unknown**, which is deliberately distinct from `off`: "could not tell" and "it is
off" are different findings.

A `baseline` run **refuses to start while WARP is on**, and a `warp` run refuses
while it is off. `--force` overrides, and the override is recorded in the result
file and in the report.

The refusal also explains the most likely cause of being stuck: a WARP client in
DNS-only (DoH) mode encrypts DNS but does not tunnel traffic, and reports
`warp=off`.

## 6. Address family

**IPv4 is the default and is forced**, not merely preferred. Several targets
publish AAAA records and Go's dialer would otherwise use IPv6 silently, making
an "IPv4" claim untrue. `--ipv6` opts in. The family and the resolved address are
recorded per target per phase, so an anycast or routing change between phases is
visible rather than inferred.

## 7. What a result does not tell you

These are printed in the report itself, next to the numbers, not buried here.

- **The Cloudflare edge is not an end-to-end measurement.** With WARP enabled,
  a connection to Cloudflare's speed endpoints never leaves Cloudflare's network.
  It measures the ISP-to-edge hop. It is kept because it is the only upload
  target available in some groups, and because the contrast with the other
  targets is informative — but it must not be read as an international result.
- **Best-effort targets.** Community-run and provider-run public endpoints can be
  busy, rate-limited or retired without notice. A missing or anomalous row is
  more likely to be the target than the path.
- **Domestic controls are not expected to improve.** Targets on the domestic
  path are included precisely so a reader can see that an improvement elsewhere
  is specific to international transit rather than a general uplift.
- **Public IPs are masked by default** to their network prefix. `--no-mask`
  discloses them.
- **A single run is a single sample of a path that varies.** Nothing here
  measures whether an improvement persists across hours, days or congestion
  events.

## 8. The two phases and time-of-day drift

WARP is toggled by hand, so the phases cannot be interleaved. That is honest but
it costs something: an ISP path is not the same at 08:00 and 20:00, and a
difference between the phases may be time-of-day rather than WARP.

What warpbench does about it:

- Every phase records its wall-clock start and end.
- A gap longer than **10 minutes** between the phases is reported as a caveat in
  the report itself.
- `--phase baseline|warp` and `--compare` let the halves be run at the *same*
  time of day on different days.
- Comparing two files measured against different server-list revisions is
  refused unless forced, because a list change invalidates a like-for-like
  comparison.

## 9. Reproducing a published run

Every result file is versioned JSON containing:

- the effective configuration, complete enough to repeat the run;
- the server-list revision and where it was loaded from;
- every **raw sample**, not just the summary, so a headline can be recomputed
  rather than trusted;
- every WARP-state reading taken during the run;
- every warning, including substitutions such as the TCP latency fallback.

Every Markdown report ends with the exact command line that produced it. Given
the JSON files and that command, a run can be repeated and the summary
recomputed independently.

To reproduce a published result:

```sh
# Fetch the two result files it links to, then:
warpbench --compare --report regenerated.md baseline.json warp.json
```

## 10. Deliberate limitations

- **No automatic WARP toggling.** It requires privileges, and the tool requires
  none.
- **No continuous monitoring.** A result describes one window.
- **No central result collection.** Nothing is uploaded anywhere.
- **No third-party geolocation.** The only network calls are the targets, the
  trace endpoint, and the server list.
- **Public targets only.** No Ookla/Speedtest.net endpoints are used, for terms
  of service reasons.
