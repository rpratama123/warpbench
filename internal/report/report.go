// Package report renders a comparison as a publishable Markdown document.
//
// The audience is a reader who was not present: they need to know what was
// measured, on which targets, under which conditions, and what the numbers do
// not prove. The caveats section is therefore not decoration. A report that
// shows a Cloudflare number without saying that the path never leaves
// Cloudflare's network is worse than no report, because it looks like evidence.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rpratama123/warpbench/internal/chart"
	"github.com/rpratama123/warpbench/internal/results"
)

const (
	// DefaultWidth is the chart width used in Markdown, deliberately narrower
	// than a terminal: a report is read in editors, on GitHub and in feeds, and
	// a chart that survives all three is worth more than a longer bar.
	DefaultWidth = 72

	// DefaultMethodologyURL points at the published method.
	DefaultMethodologyURL = "https://github.com/rpratama123/warpbench/blob/main/METHODOLOGY.md"
)

// Options configures rendering.
type Options struct {
	// CommandLine is the exact command that produced these files, reproduced
	// verbatim so a reader can repeat the run.
	CommandLine string
	// MethodologyURL links the published method.
	MethodologyURL string
	// Width is the chart width in columns.
	Width int
	// Title overrides the document heading.
	Title string
}

func (o Options) withDefaults() Options {
	if o.Width <= 0 {
		o.Width = DefaultWidth
	}
	if o.MethodologyURL == "" {
		o.MethodologyURL = DefaultMethodologyURL
	}
	if o.Title == "" {
		o.Title = "warpbench: raw ISP path vs Cloudflare WARP"
	}
	return o
}

// Markdown renders the comparison.
func Markdown(cmp *results.Comparison, opts Options) []byte {
	opts = opts.withDefaults()

	var b strings.Builder

	writeHeader(&b, cmp, opts)
	writeSummary(&b, cmp)
	writePhases(&b, cmp)
	writeMetrics(&b, cmp, opts)
	writeSkipped(&b, cmp)
	writeCaveats(&b, cmp)
	writeReproduce(&b, cmp, opts)

	return []byte(strings.TrimRight(b.String(), "\n") + "\n")
}

func writeHeader(b *strings.Builder, cmp *results.Comparison, opts Options) {
	fmt.Fprintf(b, "# %s\n\n", opts.Title)

	base, warp := cmp.Baseline, cmp.Warp

	b.WriteString("| | |\n|---|---|\n")
	fmt.Fprintf(b, "| Measured | %s to %s |\n",
		base.StartedAt.Format(time.RFC3339), warp.EndedAt.Format(time.RFC3339))
	fmt.Fprintf(b, "| Timezone | %s |\n", orUnknown(base.Environment.Timezone))
	fmt.Fprintf(b, "| Tool | warpbench %s (%s/%s, %s) |\n",
		orUnknown(base.Tool.Version), base.Environment.OS, base.Environment.Arch, orUnknown(base.Tool.Go))
	fmt.Fprintf(b, "| Server list | revision %s, source %s |\n",
		orUnknown(base.ServerList.Revision), orUnknown(base.ServerList.Source))
	fmt.Fprintf(b, "| Mode | %s, %d server(s), %d stream(s) |\n",
		orUnknown(base.Configuration.Mode), len(base.Servers), base.Configuration.Parallel)
	fmt.Fprintf(b, "| Address family | %s |\n", family(base.Configuration.IPv6))
	fmt.Fprintf(b, "| Transport | %s |\n", transportOf(base))
	b.WriteString("\n")
}

func writeSummary(b *strings.Builder, cmp *results.Comparison) {
	b.WriteString("## Summary\n\n")
	b.WriteString(cmp.Summary.Headline())
	b.WriteString("\n\n")
	b.WriteString("Charts are zero-based and share one scale within a section. A large")
	b.WriteString(" relative change and a small absolute one can therefore look similar, so")
	b.WriteString(" the Δ column, not the bar length, is the precise figure.\n\n")
}

func writePhases(b *strings.Builder, cmp *results.Comparison) {
	b.WriteString("## Phases\n\n")
	b.WriteString("| Phase | Started | Ended | Duration | WARP | Colo | IP |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")

	for _, f := range []*results.File{cmp.Baseline, cmp.Warp} {
		// The last reading describes the phase as it finished; an earlier one
		// is shown in the caveats if the state changed mid-run.
		tr := lastTrace(f)

		fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %s | %s |\n",
			f.Phase,
			f.StartedAt.Format("15:04:05"),
			f.EndedAt.Format("15:04:05"),
			formatDuration(f.EndedAt.Sub(f.StartedAt)),
			orUnknown(tr.Warp),
			orUnknown(tr.Colo),
			orUnknown(tr.IP),
		)
	}
	b.WriteString("\n")

	if cmp.Baseline.Configuration.Masked {
		b.WriteString("Public IPs are masked to their network prefix. Runnable with `--no-mask` to disclose them.\n\n")
	}
}

func writeMetrics(b *strings.Builder, cmp *results.Comparison, opts Options) {
	for _, metric := range results.Metrics() {
		comparable, _ := partition(cmp, metric)
		if len(comparable) == 0 {
			continue
		}

		fmt.Fprintf(b, "## %s\n\n", metric.Title)
		fmt.Fprintf(b, "| Server | ISP%s | WARP%s | Δ | Δ%% | Verdict |\n", metric.Unit, metric.Unit)
		b.WriteString("|---|---:|---:|---:|---:|---|\n")

		for _, d := range comparable {
			pct := fmt.Sprintf("%+.1f%%", d.PctChange)
			if d.Baseline == 0 {
				// A percentage of a zero baseline is meaningless; the absolute
				// change still carries information.
				pct = "n/a"
			}
			fmt.Fprintf(b, "| %s | %s | %s | %+.2f | %s | %s |\n",
				d.id,
				formatValue(metric.Unit, d.Baseline),
				formatValue(metric.Unit, d.Warp),
				d.AbsDiff,
				pct,
				d.Verdict(),
			)
		}
		b.WriteString("\n")

		// The chart shows the same numbers as the table, because a shape is
		// easier to read than a column of figures.
		rows := chartRows(comparable, metric)
		valueFormat := func(v float64) string { return fmt.Sprintf("%.1f", v) }
		if strings.Contains(metric.Unit, "%") {
			valueFormat = func(v float64) string { return fmt.Sprintf("%.2f", v) }
		}

		b.WriteString("```text\n")
		for _, line := range chart.Render(rows, chart.Options{
			Width:       opts.Width,
			Chars:       chart.ASCII,
			ValueFormat: valueFormat,
		}) {
			b.WriteString(strings.TrimRight(line, " ") + "\n")
		}
		b.WriteString("```\n\n")
	}
}

// metricRow pairs a delta with its server id for rendering.
type metricRow struct {
	id string
	*results.Delta
}

func partition(cmp *results.Comparison, metric results.Metric) (comparable []metricRow, skipped []string) {
	for _, s := range cmp.Servers {
		d := metric.Get(s)
		if d == nil || !d.Comparable() {
			// Only a genuinely one-sided measurement belongs in the skipped
			// list. A metric neither phase measured is simply not part of this
			// run, and listing every server under it would invent a finding.
			if d != nil && (d.HasBaseline || d.HasWarp) {
				skipped = append(skipped, s.ID)
			}
			continue
		}
		comparable = append(comparable, metricRow{id: s.ID, Delta: d})
	}
	return comparable, skipped
}

func chartRows(rows []metricRow, metric results.Metric) []chart.Row {
	out := make([]chart.Row, 0, len(rows))
	for _, r := range rows {
		note := fmt.Sprintf("%+.1f%%  %s", r.PctChange, r.Verdict())
		if r.Baseline == 0 {
			note = fmt.Sprintf("%+.2f  %s", r.AbsDiff, r.Verdict())
		}
		out = append(out, chart.Row{
			Label: r.id,
			Series: []chart.Series{
				{Name: "ISP", Value: r.Baseline, Suffix: metric.Unit},
				{Name: "WARP", Value: r.Warp, Suffix: metric.Unit},
			},
			Note: note,
		})
	}
	return out
}

// writeSkipped names the servers a metric could not compare, so a missing row
// is explained rather than silently absent.
func writeSkipped(b *strings.Builder, cmp *results.Comparison) {
	var lines []string
	for _, metric := range results.Metrics() {
		_, skipped := partition(cmp, metric)
		if len(skipped) == 0 {
			continue
		}
		sort.Strings(skipped)
		lines = append(lines, fmt.Sprintf("- **%s**: %s", metric.Title, strings.Join(skipped, ", ")))
	}
	if len(lines) == 0 {
		return
	}

	b.WriteString("## Not measured in both phases\n\n")
	b.WriteString("These rows are absent above rather than shown as zero: not measured and measured zero are different findings.\n\n")
	for _, line := range lines {
		b.WriteString(line + "\n")
	}
	b.WriteString("\n")
}

func lastTrace(f *results.File) results.Trace {
	if len(f.Traces) == 0 {
		return results.Trace{}
	}
	return f.Traces[len(f.Traces)-1]
}

func family(ipv6 bool) string {
	if ipv6 {
		return "IPv6"
	}
	return "IPv4"
}

// transportOf reports the protocols actually used, so a reader knows whether a
// sample was multiplexed.
func transportOf(f *results.File) string {
	seen := map[string]bool{}
	for _, s := range f.Servers {
		for _, series := range []*results.Series{s.Download, s.Upload} {
			if series == nil {
				continue
			}
			for _, sample := range series.Samples {
				if sample.Proto != "" {
					seen[sample.Proto] = true
				}
			}
		}
	}

	if len(seen) == 0 {
		return "unknown"
	}
	out := make([]string, 0, len(seen))
	for proto := range seen {
		out = append(out, proto)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func formatValue(unit string, v float64) string {
	switch {
	case strings.Contains(unit, "ms"):
		return fmt.Sprintf("%.1f", v)
	default:
		return fmt.Sprintf("%.2f", v)
	}
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}
