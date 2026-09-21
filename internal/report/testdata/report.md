# warpbench: raw ISP path vs Cloudflare WARP

| | |
|---|---|
| Measured | 2026-09-20T08:00:00+07:00 to 2026-09-20T08:17:00+07:00 |
| Timezone | WIB |
| Tool | warpbench v0.1.0 (linux/amd64, go1.24.4) |
| Server list | revision 2026-09-20, source remote |
| Mode | quick, 4 server(s), 1 stream(s) |
| Address family | IPv4 |
| Transport | HTTP/1.1 |

## Summary

download improved on 3/3 servers (median +1180%), upload improved on 1/1 servers (median +881%), latency changed by a median of -0.1 ms, jitter improved on 0/2

Charts are zero-based and share one scale within a section. A large relative change and a small absolute one can therefore look similar, so the Δ column, not the bar length, is the precise figure.

## Phases

| Phase | Started | Ended | Duration | WARP | Colo | IP |
|---|---|---|---|---|---|---|
| baseline | 08:00:00 | 08:06:00 | 6m00s | off | SIN | 203.0.113.0/24 |
| warp | 08:12:00 | 08:17:00 | 5m00s | on | SIN | 198.51.100.0/24 |

Public IPs are masked to their network prefix. Runnable with `--no-mask` to disclose them.

## Download

| Server | ISP Mbps | WARP Mbps | Δ | Δ% | Verdict |
|---|---:|---:|---:|---:|---|
| id-cf-cgk | 41.20 | 118.40 | +77.20 | +187.4% | better |
| id-myrepublic-iperf3 | 4.81 | 210.10 | +205.29 | +4268.0% | better |
| sg-linode | 2.64 | 33.80 | +31.16 | +1180.3% | better |

```text
id-cf-cgk            ISP  ###.............. 41.2 Mbps
                     WARP ##########....... 118.4 Mbps  +187.4%  better
id-myrepublic-iperf3 ISP  #................ 4.8 Mbps
                     WARP ################# 210.1 Mbps  +4268.0%  better
sg-linode            ISP  #................ 2.6 Mbps
                     WARP ###.............. 33.8 Mbps   +1180.3%  better
```

## Upload

| Server | ISP Mbps | WARP Mbps | Δ | Δ% | Verdict |
|---|---:|---:|---:|---:|---|
| id-cf-cgk | 9.80 | 96.10 | +86.30 | +880.6% | better |

```text
id-cf-cgk ISP  ###........................... 9.8 Mbps
          WARP ############################## 96.1 Mbps  +880.6%  better
```

## Latency (average)

| Server | ISP ms | WARP ms | Δ | Δ% | Verdict |
|---|---:|---:|---:|---:|---|
| id-cf-cgk | 21.6 | 21.4 | -0.20 | -0.9% | better |
| sg-linode | 22.0 | 22.1 | +0.10 | +0.5% | same |

```text
id-cf-cgk ISP  #################################. 21.6 ms
          WARP #################################. 21.4 ms  -0.9%  better
sg-linode ISP  ################################## 22.0 ms
          WARP ################################## 22.1 ms  +0.5%  same
```

## Jitter

| Server | ISP ms | WARP ms | Δ | Δ% | Verdict |
|---|---:|---:|---:|---:|---|
| id-cf-cgk | 0.4 | 0.5 | +0.10 | +25.0% | worse |
| sg-linode | 0.3 | 0.4 | +0.10 | +33.3% | worse |

```text
id-cf-cgk ISP  ############################....... 0.4 ms
          WARP ################################### 0.5 ms  +25.0%  worse
sg-linode ISP  #####################.............. 0.3 ms
          WARP ############################....... 0.4 ms  +33.3%  worse
```

## Packet loss

| Server | ISP % | WARP % | Δ | Δ% | Verdict |
|---|---:|---:|---:|---:|---|
| id-cf-cgk | 0.00 | 0.00 | +0.00 | n/a | same |
| sg-linode | 0.00 | 0.00 | +0.00 | n/a | same |

```text
id-cf-cgk ISP  ..................................... 0.00 %
          WARP ..................................... 0.00 %  +0.00  same
sg-linode ISP  ..................................... 0.00 %
          WARP ..................................... 0.00 %  +0.00  same
```

## Not measured in both phases

These rows are absent above rather than shown as zero: not measured and measured zero are different findings.

- **Download**: eu-ls-amsterdam
- **Upload**: id-myrepublic-iperf3
- **Latency (average)**: eu-ls-amsterdam
- **Jitter**: eu-ls-amsterdam
- **Packet loss**: eu-ls-amsterdam

## Caveats

- **Cloudflare edge is not an end-to-end measurement.** These targets terminate at the nearest Cloudflare edge: `id-cf-cgk`. With WARP enabled the path never leaves Cloudflare's network at all, so they measure the ISP-to-edge hop and must not be read as international results. They are kept because they are the only upload targets available in some groups, and because the contrast with the other targets is itself informative.
- **Domestic controls do not isolate international transit.** These sit on the domestic path: `id-myrepublic-iperf3`. They are not a clean control for "international transit", because a domestic target on a different network still leaves this ISP's network and is reached over the same off-net peering as an international one. Read any improvement on them as evidence that the congestion is in off-net peering generally rather than in international transit alone; only a target served inside the ISP's own network can be expected to stay flat.
- **Best-effort targets.** Community-run or provider-run public test endpoints: `eu-ls-amsterdam` and `id-myrepublic-iperf3`. They can be busy, rate-limited or retired without notice, so a missing or anomalous row is more likely to be the target than the path.
- **Latency measured by TCP connect, not ICMP.** ICMP was unavailable on these targets, so their latency is TCP connect time to port 443: `eu-ls-amsterdam` and `id-myrepublic-iperf3`. That is a different quantity measured at a different layer, and it is not comparable with the ICMP rows above it, nor with any other run's ICMP results.
- **Latency probes that answered nothing.** These targets were probed in both phases and never replied: `id-myrepublic-iperf3`. They are absent from the latency, jitter and loss tables rather than shown as 0.0 ms or 100% loss, because a probe that receives nothing has not measured a latency. The probe is a TCP connect to port 443, and a target that does not listen there is silent for a reason unrelated to the path. Their throughput rows are unaffected.
- **Undersized throughput samples.** These transferred less than the minimum payload for the measurement window, so their rates rest on a shorter sample than the rest: `eu-ls-amsterdam`, `id-myrepublic-iperf3` and `sg-linode`. Treat those figures as indicative rather than comparable.
- **Resolved address changed between phases.** These resolved to a different address in each phase: `id-cf-cgk`. That is expected under WARP, where anycast and routing change, but it means the two rows are not necessarily the same server.
- **WARP state changed during a phase.** The trace endpoint reported a different state at the end of the warp (off to on) phase than at the start. Results from that phase may span two different paths.
- **Run warnings.** `the phases measured 4 and 3 servers`; `eu-ls-amsterdam was measured on the ISP path but not over WARP`
- **Recorded during measurement.** `baseline/eu-ls-amsterdam: timings: 1 of 4 probes failed`; `baseline/id-myrepublic-iperf3: upload: no usable samples`; `baseline: server list fetched from the network`

## Reproduce

```sh
warpbench --quick --groups id,sg,eu --phase baseline --out baseline.json
warpbench --quick --groups id,sg,eu --phase warp --out warp.json
warpbench --compare --report report.md baseline.json warp.json
```

| | |
|---|---|
| Server list revision | 2026-09-20 |
| Tool version | v0.1.0 |
| Phase order | baseline, then WARP |

The full method, including the measurement windows, the slow-start
exclusion and the fairness rules, is documented at
<https://github.com/rpratama123/warpbench/blob/main/METHODOLOGY.md>.
