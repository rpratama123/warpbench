package report

import (
	"time"

	"github.com/rpratama123/warpbench/internal/results"
)

// fixture returns a comparison rich enough to exercise every section: edge and
// best-effort targets, a domestic control, an ICMP substitution, a one-sided
// metric, a phase whose WARP state changed, and a target whose latency probe
// never answers.
func fixture() *results.Comparison {
	at := time.Date(2026, 9, 20, 8, 0, 0, 0, time.FixedZone("WIB", 7*3600))
	warpAt := at.Add(12 * time.Minute)

	cfg := results.Configuration{
		Mode: "quick", Groups: []string{"id", "sg", "eu"},
		ServerIDs: []string{"id-cf-cgk", "id-myrepublic-iperf3", "sg-linode", "eu-ls-amsterdam"},
		Parallel:  1, Masked: true,
		DownloadSecs: 12, UploadSecs: 8, DownloadRuns: 1, UploadRuns: 1,
		PingCount: 10, TimingRuns: 3, WarmupSecs: 1,
	}
	env := results.Environment{OS: "linux", Arch: "amd64", Go: "go1.24.4",
		LocalTime: at.Format(time.RFC3339), Timezone: "WIB"}

	baselineServers := []results.Server{
		{
			ID: "id-cf-cgk", Name: "Cloudflare edge", Group: "id", Protocol: "cloudflare",
			Country: "ID", ResolvedIP: "162.159.140.220",
			Flags: []string{"footnote:cloudflare-edge"},
			Ping:  &results.Ping{Method: "icmp", Sent: 10, Received: 10, AvgMs: 21.6, JitterMs: 0.4, P95Ms: 22.4},
			Download: &results.Series{Metric: "download", Parallel: 1, MedianSteadyMbps: 41.2,
				Samples: []results.Sample{{Bytes: 1, ElapsedMs: 12000, SteadyMbps: 41.2, Proto: "HTTP/1.1"}}},
			Upload: &results.Series{Metric: "upload", Parallel: 1, MedianSteadyMbps: 9.8,
				Samples: []results.Sample{{Bytes: 1, ElapsedMs: 8000, SteadyMbps: 9.8, Proto: "HTTP/1.1"}}},
			Warnings: []string{},
		},
		{
			ID: "id-myrepublic-iperf3", Name: "MyRepublic Jakarta", Group: "id", Protocol: "iperf3",
			City: "Jakarta", Country: "ID", ResolvedIP: "103.10.60.5:5201",
			Flags: []string{"best-effort", "control:domestic"},
			// This target answers on 5201, not on the 443 the latency probe
			// dials, so it replies to nothing in either phase. That is the case
			// the report must show as unmeasured rather than as 0.0 ms.
			Ping: &results.Ping{Method: "tcp", Target: "speedtest.myrepublic.invalid:443",
				Sent: 10, Received: 0, LossPct: 100,
				Warning: "ICMP unavailable, so latency is TCP connect time to port 443."},
			Download: &results.Series{Metric: "download", Parallel: 1, MedianSteadyMbps: 4.81,
				Samples: []results.Sample{{Bytes: 1, ElapsedMs: 12000, SteadyMbps: 4.81, Undersized: true, Proto: "HTTP/1.1"}}},
			Warnings: []string{"upload: no usable samples"},
		},
		{
			ID: "sg-linode", Name: "Linode Singapore", Group: "sg", Protocol: "http-file",
			City: "Singapore", Country: "SG", ResolvedIP: "139.162.23.4",
			Ping: &results.Ping{Method: "icmp", Sent: 10, Received: 10, AvgMs: 22.0, JitterMs: 0.3, P95Ms: 22.4},
			Download: &results.Series{Metric: "download", Parallel: 1, MedianSteadyMbps: 2.64,
				Samples: []results.Sample{{Bytes: 1, ElapsedMs: 12000, SteadyMbps: 2.64, Undersized: true, Proto: "HTTP/1.1"}}},
			Warnings: []string{},
		},
		{
			ID: "eu-ls-amsterdam", Name: "LibreSpeed Amsterdam", Group: "eu", Protocol: "librespeed",
			City: "Amsterdam", Country: "NL", ResolvedIP: "194.127.172.176",
			Flags: []string{"best-effort"},
			Ping: &results.Ping{Method: "tcp", Sent: 10, Received: 10, AvgMs: 41.0, JitterMs: 3.0,
				Warning: "ICMP unavailable, so latency is TCP connect time to port 443."},
			Download: &results.Series{Metric: "download", Parallel: 1, MedianSteadyMbps: 1.80,
				Samples: []results.Sample{{Bytes: 1, ElapsedMs: 12000, SteadyMbps: 1.80, Undersized: true, Proto: "HTTP/1.1"}}},
			Warnings: []string{"timings: 1 of 4 probes failed"},
		},
	}

	warpServers := []results.Server{
		{
			ID: "id-cf-cgk", Name: "Cloudflare edge", Group: "id", Protocol: "cloudflare",
			Country: "ID", ResolvedIP: "198.51.100.7",
			Flags: []string{"footnote:cloudflare-edge"},
			Ping:  &results.Ping{Method: "icmp", Sent: 10, Received: 10, AvgMs: 21.4, JitterMs: 0.5, P95Ms: 22.0},
			Download: &results.Series{Metric: "download", Parallel: 1, MedianSteadyMbps: 118.4,
				Samples: []results.Sample{{Bytes: 1, ElapsedMs: 12000, SteadyMbps: 118.4, Proto: "HTTP/1.1"}}},
			Upload: &results.Series{Metric: "upload", Parallel: 1, MedianSteadyMbps: 96.1,
				Samples: []results.Sample{{Bytes: 1, ElapsedMs: 8000, SteadyMbps: 96.1, Proto: "HTTP/1.1"}}},
			Warnings: []string{},
		},
		{
			ID: "id-myrepublic-iperf3", Name: "MyRepublic Jakarta", Group: "id", Protocol: "iperf3",
			City: "Jakarta", Country: "ID", ResolvedIP: "103.10.60.5:5201",
			Flags: []string{"best-effort", "control:domestic"},
			// The same silent probe as the baseline: this is the target that
			// must not be turned into a 0.0 ms latency row.
			Ping: &results.Ping{Method: "tcp", Target: "speedtest.myrepublic.invalid:443",
				Sent: 10, Received: 0, LossPct: 100,
				Warning: "ICMP unavailable, so latency is TCP connect time to port 443."},
			Download: &results.Series{Metric: "download", Parallel: 1, MedianSteadyMbps: 210.1,
				Samples: []results.Sample{{Bytes: 1, ElapsedMs: 12000, SteadyMbps: 210.1, Proto: "HTTP/1.1"}}},
			Upload: &results.Series{Metric: "upload", Parallel: 1, MedianSteadyMbps: 4.9,
				Samples: []results.Sample{{Bytes: 1, ElapsedMs: 8000, SteadyMbps: 4.9, Proto: "HTTP/1.1"}}},
			Warnings: []string{},
		},
		{
			ID: "sg-linode", Name: "Linode Singapore", Group: "sg", Protocol: "http-file",
			City: "Singapore", Country: "SG", ResolvedIP: "139.162.23.4",
			Ping: &results.Ping{Method: "icmp", Sent: 10, Received: 10, AvgMs: 22.1, JitterMs: 0.4, P95Ms: 23.0},
			Download: &results.Series{Metric: "download", Parallel: 1, MedianSteadyMbps: 33.8,
				Samples: []results.Sample{{Bytes: 1, ElapsedMs: 12000, SteadyMbps: 33.8, Proto: "HTTP/1.1"}}},
			Warnings: []string{},
		},
		// eu-ls-amsterdam is deliberately absent: measured only on the ISP path.
	}

	baseline := &results.File{
		Schema: results.SchemaVersion, Tool: results.Tool{Version: "v0.1.0", Commit: "abc1234", Go: "go1.24.4"},
		Phase: "baseline", StartedAt: at, EndedAt: at.Add(6 * time.Minute), DurationSec: 360,
		Configuration: cfg,
		ServerList:    results.ServerListRef{Schema: 2, Revision: "2026-09-20", Source: "remote", Origin: "https://example.invalid/servers.json"},
		Environment:   env,
		Traces: []results.Trace{
			{Stage: "before-baseline", At: at.Format(time.RFC3339), Warp: "off", Colo: "SIN", IP: "203.0.113.0/24", Loc: "ID"},
			{Stage: "after-baseline", At: at.Add(6 * time.Minute).Format(time.RFC3339), Warp: "off", Colo: "SIN", IP: "203.0.113.0/24", Loc: "ID"},
		},
		Servers:  baselineServers,
		Warnings: []string{"server list fetched from the network"},
	}

	warp := &results.File{
		Schema: results.SchemaVersion, Tool: results.Tool{Version: "v0.1.0", Commit: "abc1234", Go: "go1.24.4"},
		Phase: "warp", StartedAt: warpAt, EndedAt: warpAt.Add(5 * time.Minute), DurationSec: 300,
		Configuration: cfg,
		ServerList:    results.ServerListRef{Schema: 2, Revision: "2026-09-20", Source: "remote", Origin: "https://example.invalid/servers.json"},
		Environment:   env,
		Traces: []results.Trace{
			{Stage: "before-warp", At: warpAt.Format(time.RFC3339), Warp: "off", Colo: "SIN", IP: "203.0.113.0/24", Loc: "ID"},
			{Stage: "after-warp", At: warpAt.Add(5 * time.Minute).Format(time.RFC3339), Warp: "on", Colo: "SIN", IP: "198.51.100.0/24", Loc: "ID"},
		},
		Servers:  warpServers,
		Warnings: []string{},
	}

	cmp, err := results.Compare(baseline, warp, results.CompareOptions{})
	if err != nil {
		panic(err)
	}
	return cmp
}
