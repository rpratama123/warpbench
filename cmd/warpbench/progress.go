package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rpratama123/warpbench/internal/results"
	"github.com/rpratama123/warpbench/internal/runner"
	"github.com/rpratama123/warpbench/internal/serverlist"
	"github.com/rpratama123/warpbench/internal/trace"
)

// textProgress is one of the two front ends over runner.Progress; the TUI in
// Phase 6 is the other. Asserting it here means a change to the interface is a
// compile error rather than a silently missing event.
var _ runner.Progress = (*textProgress)(nil)

// textProgress renders run events as plain lines.
//
// It is the non-interactive front end over the same runner the TUI drives in
// Phase 6, so the two cannot drift in what they measure. It is deliberately
// line-oriented and ASCII: this output ends up in CI logs and pasted into
// issues.
type textProgress struct {
	w io.Writer
}

func (p *textProgress) PhaseStarted(phase string, servers []serverlist.Server, estimate time.Duration) {
	writef(p.w, "phase %s: %d server(s), estimated %s\n",
		phase, len(servers), estimate.Round(time.Second))
}

func (p *textProgress) ServerStarted(index, total int, s serverlist.Server) {
	writef(p.w, "[%d/%d] %s  %s  (%s)\n", index, total, s.ID, s.Name, s.Protocol)
}

func (p *textProgress) Step(_ serverlist.Server, step string) {
	writef(p.w, "      %-9s running\n", step)
}

func (p *textProgress) Trace(r trace.Result) {
	writef(p.w, "      trace %-16s %s\n", r.Stage, r.Describe())
}

func (p *textProgress) ServerDone(_ serverlist.Server, m results.Server) {
	if m.Ping != nil {
		writef(p.w, "      %-9s %s %d/%d loss %.1f%%  min %.1f avg %.1f p95 %.1f max %.1f ms  jitter %.1f\n",
			"ping", m.Ping.Method, m.Ping.Received, m.Ping.Sent, m.Ping.LossPct,
			m.Ping.MinMs, m.Ping.AvgMs, m.Ping.P95Ms, m.Ping.MaxMs, m.Ping.JitterMs)
	}

	if t := m.Timings; t != nil {
		writef(p.w, "      %-9s dns %s connect %s tls %s ttfb %s ms",
			"timings", ranOrMissing(t.DNSMs, t.DNSRan), ranOrMissing(t.ConnectMs, t.ConnectRan),
			ranOrMissing(t.TLSMs, t.TLSRan), ranOrMissing(t.TTFBMs, t.FirstByteRan))
		if t.Proto != "" {
			writef(p.w, "  %s", t.Proto)
		}
		if t.TLSVersion != "" {
			writef(p.w, " %s", t.TLSVersion)
		}
		writef(p.w, "  (%d samples)\n", t.Samples)
	}

	for _, series := range []*results.Series{m.Download, m.Upload} {
		if series == nil {
			continue
		}
		writef(p.w, "      %-9s %.2f Mbps steady / %.2f overall", series.Metric, series.MedianSteadyMbps, series.MedianOverallMbps)
		if series.Parallel > 1 {
			writef(p.w, "  (%d streams)", series.Parallel)
		}
		if flags := sampleFlags(series); flags != "" {
			writef(p.w, "  [%s]", flags)
		}
		writef(p.w, "\n")
	}

	if m.ResolvedIP != "" {
		writef(p.w, "      %-9s %s\n", "address", m.ResolvedIP)
	}

	for _, warn := range m.Warnings {
		writef(p.w, "      %-9s %s\n", "warning", warn)
	}
}

// ranOrMissing renders a setup phase that did not happen as "n/a" rather than
// as a zero-cost phase. A fast phase also measures as zero, which is why
// occurrence is tracked separately.
func ranOrMissing(ms float64, ran bool) string {
	if !ran {
		return "n/a"
	}
	return fmt.Sprintf("%.1f", ms)
}

func sampleFlags(series *results.Series) string {
	var flags []string
	for _, s := range series.Samples {
		if s.Undersized {
			flags = append(flags, "undersized")
		}
		if s.Truncated {
			flags = append(flags, "truncated")
		}
		if s.Partial {
			flags = append(flags, "partial")
		}
		if s.Error != "" {
			flags = append(flags, "failed")
		}
	}
	return strings.Join(dedupe(flags), " ")
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// Summary prints the end-of-phase recap.
func (p *textProgress) Summary(f *results.File) {
	writef(p.w, "\nphase %s finished in %s\n", f.Phase, time.Duration(f.DurationSec*float64(time.Second)).Round(time.Second))

	for _, t := range f.Traces {
		writef(p.w, "  trace %-16s warp=%s colo=%s ip=%s\n", t.Stage, t.Warp, t.Colo, t.IP)
	}

	var download, upload, latency int
	for _, s := range f.Servers {
		if s.Download != nil {
			download++
		}
		if s.Upload != nil {
			upload++
		}
		if s.Ping != nil {
			latency++
		}
	}
	writef(p.w, "  measured: %d latency, %d download, %d upload across %d servers\n",
		latency, download, upload, len(f.Servers))

	warnings := len(f.Warnings)
	for _, s := range f.Servers {
		warnings += len(s.Warnings)
	}
	if warnings > 0 {
		writef(p.w, "  %d warning(s); see the result file\n", warnings)
	}
}
