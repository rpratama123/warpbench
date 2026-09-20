// Package runner orchestrates one phase of measurement.
//
// It owns the fairness rules from PLAN.md section 5.3: the server order is
// derived from the server list rather than from the user's selection order, so
// both phases measure the same targets in the same sequence; every server gets
// the same budget; and a metric that fails is recorded as a warning rather than
// silently becoming a zero or aborting the run.
package runner

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/rpratama123/warpbench/internal/netprobe"
	"github.com/rpratama123/warpbench/internal/results"
	"github.com/rpratama123/warpbench/internal/serverlist"
	"github.com/rpratama123/warpbench/internal/stats"
	"github.com/rpratama123/warpbench/internal/throughput"
	"github.com/rpratama123/warpbench/internal/trace"
)

// Deps carries what the runner needs from outside itself.
type Deps struct {
	// TraceDoer fetches the WARP-state endpoint.
	TraceDoer trace.Doer
	// TraceURL overrides the trace endpoint, for tests.
	TraceURL string
	// Client is the HTTP client shared by every throughput adapter.
	Client *throughput.HTTPClient
	// IPerf3Bin resolves the iperf3 binary, downloading it on first use.
	IPerf3Bin func(ctx context.Context) (string, error)
	// UserAgent identifies us to every endpoint we contact.
	UserAgent string
	// Now is injectable so tests can control timestamps.
	Now func() time.Time
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Progress receives run events.
//
// It is small and callback-shaped so the plain non-interactive renderer and the
// bubbletea model can both drive the same runner without it knowing which is
// attached.
type Progress interface {
	// PhaseStarted is called once the server order is fixed, before any
	// measurement, so a UI can show the estimate it is about to spend.
	PhaseStarted(phase string, servers []serverlist.Server, estimate time.Duration)
	// ServerStarted is called before each server's measurements. index is
	// one-based.
	ServerStarted(index, total int, s serverlist.Server)
	// Step reports progress within a server.
	Step(s serverlist.Server, step string)
	// Trace reports a WARP-state reading.
	Trace(r trace.Result)
	// ServerDone reports a finished server.
	ServerDone(s serverlist.Server, measured results.Server)
}

// NopProgress discards every event.
type NopProgress struct{}

func (NopProgress) PhaseStarted(string, []serverlist.Server, time.Duration) {}
func (NopProgress) ServerStarted(int, int, serverlist.Server)               {}
func (NopProgress) Step(serverlist.Server, string)                          {}
func (NopProgress) Trace(trace.Result)                                      {}
func (NopProgress) ServerDone(serverlist.Server, results.Server)            {}

// Config is a fully resolved run request.
type Config struct {
	// Phase is baseline or warp.
	Phase string
	Mode  Mode
	// List is the server list the selection came from, used to establish the
	// canonical order.
	List *serverlist.List
	// Servers is the selection; the runner reorders it canonically.
	Servers []serverlist.Server
	// SelectedIDs is the selection before ordering, recorded in the file.
	SelectedIDs []string
	Groups      []string
	Budget      Budget
	IPv6        bool
	Masked      bool
	Offline     bool
	// ServerListSource and Origin record where the list came from.
	ServerListSource serverlist.Source
	ServerListOrigin string

	// Progress receives run events. Nil means no events.
	Progress Progress

	Tool results.Tool
}

// Order returns servers in the canonical order of the list they came from:
// group order first, then the list's own server order within a group.
//
// Deriving the order from the list, rather than preserving whatever order the
// user toggled servers in, is what guarantees both phases measure the same
// targets in the same sequence.
func Order(list *serverlist.List, selected []serverlist.Server) []serverlist.Server {
	if list == nil {
		return append([]serverlist.Server(nil), selected...)
	}

	groupRank := make(map[string]int, len(list.Groups))
	for i, g := range list.Groups {
		groupRank[g.ID] = i
	}

	serverRank := make(map[string]int, len(list.Servers))
	for i, s := range list.Servers {
		serverRank[s.ID] = i
	}

	out := append([]serverlist.Server(nil), selected...)
	rank := func(s serverlist.Server) (int, int) {
		g, ok := groupRank[s.Group]
		if !ok {
			g = len(groupRank) // an unknown group sorts last rather than first
		}
		i, ok := serverRank[s.ID]
		if !ok {
			i = len(serverRank)
		}
		return g, i
	}

	// A stable insertion sort keeps this dependency-free and keeps equal
	// elements in their existing order.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			gi, ii := rank(out[j-1])
			gj, ij := rank(out[j])
			if gi < gj || (gi == gj && ii <= ij) {
				break
			}
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// Select returns the servers a run should measure.
//
// Quick runs the quick tier only. Extended runs the whole list: the tier field
// marks which targets are sufficient for a fast signal, not which are
// forbidden to a thorough run.
func Select(list *serverlist.List, mode Mode, groups []string, onlyIDs []string) []serverlist.Server {
	candidates := list.Selected(groups)

	if len(onlyIDs) > 0 {
		want := make(map[string]bool, len(onlyIDs))
		for _, id := range onlyIDs {
			want[id] = true
		}
		var out []serverlist.Server
		for _, s := range candidates {
			if want[s.ID] {
				out = append(out, s)
			}
		}
		return Order(list, out)
	}

	if mode == ModeQuick {
		var out []serverlist.Server
		for _, s := range candidates {
			if s.Tier == "quick" {
				out = append(out, s)
			}
		}
		return Order(list, out)
	}

	return Order(list, candidates)
}

// Run measures one phase and returns the result file.
//
// A server that fails entirely is recorded with a warning and the run
// continues: losing one target must not lose the other twenty-nine, and an
// asymmetry between phases is itself a finding.
func Run(ctx context.Context, cfg Config, deps Deps) (*results.File, error) {
	if cfg.List == nil {
		return nil, fmt.Errorf("no server list")
	}
	if len(cfg.Servers) == 0 {
		return nil, fmt.Errorf("no servers selected")
	}

	servers := Order(cfg.List, cfg.Servers)
	estimate := Estimate(servers, cfg.Budget)

	prog := cfg.Progress
	if prog == nil {
		prog = NopProgress{}
	}
	prog.PhaseStarted(cfg.Phase, servers, estimate)

	started := deps.now()
	file := &results.File{
		Schema:      results.SchemaVersion,
		Tool:        cfg.Tool,
		Phase:       cfg.Phase,
		StartedAt:   started,
		ServerList:  results.ServerListRef{Schema: cfg.List.Schema, Revision: cfg.List.Revision, Source: string(cfg.ServerListSource), Origin: cfg.ServerListOrigin},
		Environment: environment(deps.now()),
		Configuration: results.Configuration{
			Mode:         string(cfg.Mode),
			Groups:       orEmpty(cfg.Groups),
			ServerIDs:    serverIDs(servers),
			Parallel:     cfg.Budget.Parallel,
			IPv6:         cfg.IPv6,
			Masked:       cfg.Masked,
			DownloadSecs: cfg.Budget.DownloadDuration.Seconds(),
			UploadSecs:   cfg.Budget.UploadDuration.Seconds(),
			DownloadRuns: cfg.Budget.DownloadSamples,
			UploadRuns:   cfg.Budget.UploadSamples,
			PingCount:    cfg.Budget.PingCount,
			TimingRuns:   cfg.Budget.TimingSamples,
			WarmupSecs:   cfg.Budget.Warmup.Seconds(),
			MinBytes:     cfg.Budget.MinBytes,
			MaxBytes:     cfg.Budget.MaxBytes,
		},
		Servers: make([]results.Server, 0, len(servers)),
	}

	// Trace before measuring: the state at this moment is what the whole phase
	// is attributed to.
	file.Traces = append(file.Traces, deps.readTrace(ctx, prog, "before-"+cfg.Phase, cfg.Masked))

	for i, s := range servers {
		if err := ctx.Err(); err != nil {
			file.Warnings = append(file.Warnings, "run cancelled: "+err.Error())
			break
		}

		prog.ServerStarted(i+1, len(servers), s)
		measured := measureServer(ctx, cfg, deps, s, prog)
		file.Servers = append(file.Servers, measured)
		prog.ServerDone(s, measured)
	}

	file.Traces = append(file.Traces, deps.readTrace(ctx, prog, "after-"+cfg.Phase, cfg.Masked))

	file.EndedAt = deps.now()
	file.DurationSec = file.EndedAt.Sub(file.StartedAt).Seconds()
	if file.Warnings == nil {
		file.Warnings = []string{}
	}

	return file, nil
}

// measureServer measures one target, recording per-metric failures as warnings
// rather than losing the whole server.
func measureServer(ctx context.Context, cfg Config, deps Deps, s serverlist.Server, prog Progress) results.Server {
	out := results.Server{
		ID:       s.ID,
		Name:     s.Name,
		Group:    s.Group,
		Protocol: s.Protocol,
		Provider: s.Provider,
		City:     s.City,
		Country:  s.Country,
		Warnings: []string{},
	}

	adapter, err := throughput.New(s, throughput.Deps{
		Client:    deps.Client,
		UserAgent: deps.UserAgent,
		IPerf3Bin: deps.IPerf3Bin,
	})
	if err != nil {
		out.Warnings = append(out.Warnings, "no adapter: "+err.Error())
		return out
	}
	caps := adapter.Caps()

	if s.Has("ping") {
		prog.Step(s, "ping")
		ping, err := netprobe.Ping(ctx, s.PingHost, netprobe.PingOptions{
			Count:    cfg.Budget.PingCount,
			Interval: cfg.Budget.PingInterval,
			Timeout:  cfg.Budget.PingTimeout,
		})
		switch {
		case err != nil:
			out.Warnings = append(out.Warnings, "ping: "+err.Error())
		default:
			out.Ping = summarizePing(ping)
			if ping.Warning != "" {
				out.Warnings = append(out.Warnings, ping.Warning)
			}
			if out.ResolvedIP == "" {
				out.ResolvedIP = ping.Addr
			}
		}
	}

	if timingsURL := adapter.TimingsURL(); timingsURL != "" && s.Has("timings") && caps.Timings {
		prog.Step(s, "timings")
		timings, errs := netprobe.TimingsN(ctx, timingsURL, httpClient(deps.Client), netprobe.TimingOptions{
			UserAgent: deps.UserAgent,
		}, cfg.Budget.TimingSamples)

		switch {
		case len(timings) == 0:
			out.Warnings = append(out.Warnings, "timings: "+firstErr(errs))
		default:
			median := netprobe.MedianTiming(timings)
			out.Timings = summarizeTimings(median, len(timings))
			if netprobe.AddressChanged(timings) {
				out.Warnings = append(out.Warnings, "the resolved address changed during the timings run")
			}
			if out.ResolvedIP == "" {
				out.ResolvedIP = median.RemoteIP
			}
			if len(errs) > 0 {
				out.Warnings = append(out.Warnings, fmt.Sprintf("%d of %d timing probes failed", len(errs), len(errs)+len(timings)))
			}
		}
	}

	if s.Has("download") && caps.Download {
		prog.Step(s, "download")
		out.Download = seriesFrom(ctx, "download", adapter.Download, cfg.Budget.downloadOpts(deps.UserAgent), cfg.Budget.DownloadSamples, &out)
	}

	if s.Has("upload") && caps.Upload {
		prog.Step(s, "upload")
		out.Upload = seriesFrom(ctx, "upload", adapter.Upload, cfg.Budget.uploadOpts(deps.UserAgent), cfg.Budget.UploadSamples, &out)
	}

	return out
}

// seriesFrom runs n samples of one direction. Individual sample failures are
// recorded on the samples themselves so a partially successful series still
// carries everything that was measured.
func seriesFrom(ctx context.Context, metric string, measure func(context.Context, throughput.Opts) (throughput.Sample, error), opts throughput.Opts, n int, out *results.Server) *results.Series {
	if n < 1 {
		n = 1
	}

	series := &results.Series{
		Metric:   metric,
		Samples:  make([]results.Sample, 0, n),
		Parallel: opts.Parallel,
	}

	var steady []float64
	var overall []float64

	for i := range n {
		if err := ctx.Err(); err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("%s: cancelled after %d sample(s)", series.Metric, i))
			break
		}

		sample, err := measure(ctx, opts)
		if err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("%s sample %d: %v", series.Metric, i+1, err))
			series.Samples = append(series.Samples, results.Sample{Error: err.Error()})
			continue
		}

		converted := sampleToResult(sample)
		series.Samples = append(series.Samples, converted)
		if sample.SteadyMbps > 0 {
			steady = append(steady, sample.SteadyMbps)
			overall = append(overall, sample.OverallMbps)
		}
		if out.ResolvedIP == "" {
			out.ResolvedIP = sample.RemoteIP
		}
	}

	if len(steady) == 0 {
		// Nothing usable: report the absence rather than an empty series that
		// would render as zero throughput.
		if len(series.Samples) > 0 {
			out.Warnings = append(out.Warnings, series.Metric+": no usable samples")
		}
		return nil
	}

	series.MedianSteadyMbps = stats.MedianFloat(steady)
	series.MedianOverallMbps = stats.MedianFloat(overall)
	return series
}

func sampleToResult(s throughput.Sample) results.Sample {
	return results.Sample{
		Bytes:       s.Bytes,
		ElapsedMs:   results.Ms(s.Elapsed),
		SteadyBytes: s.SteadyBytes,
		SteadyForMs: results.Ms(s.SteadyFor),
		SteadyMbps:  s.SteadyMbps,
		OverallMbps: s.OverallMbps,
		Parallel:    s.Parallel,
		Proto:       s.Proto,
		RemoteIP:    s.RemoteIP,
		Undersized:  s.Undersized,
		Truncated:   s.Truncated,
		Partial:     s.Partial,
	}
}

func summarizePing(p *netprobe.PingResult) *results.Ping {
	// The first reply carries cold-path costs the others do not, so it is
	// dropped per the methodology.
	//
	// Loss, however, must count every probe. Deriving it from the trimmed set
	// would report 25% loss for a perfect four-probe run, because discarding
	// the cold reply looks identical to losing one.
	kept := stats.DropFirst(p.RTTs)
	summary := stats.Summarize(kept, len(kept))

	received := len(p.RTTs)
	lossPct := 0.0
	if p.Sent > 0 {
		lossPct = float64(p.Sent-received) / float64(p.Sent) * 100
		if lossPct < 0 {
			lossPct = 0
		}
	}

	return &results.Ping{
		Method:   string(p.Method),
		Target:   p.Target,
		Sent:     p.Sent,
		Received: received,
		LossPct:  lossPct,
		MinMs:    results.Ms(summary.Min),
		AvgMs:    results.Ms(summary.Avg),
		MedianMs: results.Ms(summary.Median),
		MaxMs:    results.Ms(summary.Max),
		P95Ms:    results.Ms(summary.P95),
		StdDevMs: results.Ms(summary.StdDev),
		JitterMs: results.Ms(summary.Jitter),
		Warning:  p.Warning,
	}
}

func summarizeTimings(t netprobe.Timing, samples int) *results.Timings {
	return &results.Timings{
		DNSMs:        results.Ms(t.DNS),
		ConnectMs:    results.Ms(t.Connect),
		TLSMs:        results.Ms(t.TLS),
		TTFBMs:       results.Ms(t.TTFB),
		TotalMs:      results.Ms(t.Total),
		RemoteIP:     t.RemoteIP,
		Proto:        t.Proto,
		TLSVersion:   t.TLSVersion,
		Samples:      samples,
		DNSRan:       t.DNSRan,
		ConnectRan:   t.ConnectRan,
		TLSRan:       t.TLSRan,
		FirstByteRan: t.FirstByteRan,
	}
}

func environment(now time.Time) results.Environment {
	zone, _ := now.Zone()
	return results.Environment{
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Go:        runtime.Version(),
		LocalTime: now.Format(time.RFC3339),
		Timezone:  zone,
	}
}

// orEmpty keeps a nil slice from marshalling as JSON null, which the results
// schema correctly rejects: an absent list and an empty one are different
// claims.
func orEmpty(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func serverIDs(servers []serverlist.Server) []string {
	ids := make([]string, 0, len(servers))
	for _, s := range servers {
		ids = append(ids, s.ID)
	}
	return ids
}

// httpClient returns the client the timings probe should use, sharing the
// throughput connection policy so the two cannot differ for unrelated reasons.
func httpClient(c *throughput.HTTPClient) *http.Client {
	if c == nil {
		return &http.Client{Timeout: 15 * time.Second}
	}
	return c.Client()
}

func firstErr(errs []error) string {
	if len(errs) == 0 {
		return "no samples"
	}
	return errs[0].Error()
}

// readTrace takes one reading and reports it, keeping a failure as a recorded
// fact rather than an error that stops the run.
func (d Deps) readTrace(ctx context.Context, prog Progress, stage string, masked bool) results.Trace {
	r, _ := trace.Fetch(ctx, d.TraceDoer, d.TraceURL, stage)
	prog.Trace(r)
	return results.Trace{
		Stage:       r.Stage,
		At:          r.At.Format(time.RFC3339),
		Warp:        string(r.Warp),
		WarpRaw:     r.WarpRaw,
		Colo:        r.Colo,
		IP:          maskIf(r.IP, masked),
		Gateway:     r.Gateway,
		Loc:         r.Loc,
		HTTP:        r.HTTP,
		TLS:         r.TLS,
		Kex:         r.Kex,
		RBI:         r.RBI,
		VisitScheme: r.VisitScheme,
		Timestamp:   r.Timestamp,
		Err:         r.Err,
	}
}

// maskIf masks an address unless masking was disabled. Reports are published,
// so masking is the default and opting out is explicit.
func maskIf(ip string, mask bool) string {
	if !mask {
		return ip
	}
	return trace.MaskIP(ip)
}
