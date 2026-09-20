package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rpratama123/warpbench/internal/results"
)

// caveat is one thing a reader must know to avoid over-interpreting a number.
type caveat struct {
	Title string
	Body  string
}

// writeCaveats renders the section that keeps the report honest.
func writeCaveats(b *strings.Builder, cmp *results.Comparison) {
	caveats := collectCaveats(cmp)

	b.WriteString("## Caveats\n\n")
	if len(caveats) == 0 {
		b.WriteString("None: every target was measured the same way in both phases, no substitutions were made, and no run-level warnings were recorded.\n\n")
		return
	}
	for _, c := range caveats {
		fmt.Fprintf(b, "- **%s.** %s\n", c.Title, c.Body)
	}
	b.WriteString("\n")
}

func collectCaveats(cmp *results.Comparison) []caveat {
	both := append(append([]results.Server{}, cmp.Baseline.Servers...), cmp.Warp.Servers...)

	var caveats []caveat

	// --- did the phases actually measure what they claim? -----------------
	//
	// This is checked first because it is the one thing that can make the whole
	// report meaningless, and a reader who misses a cell in the trace table
	// would otherwise take the comparison at face value.
	if !phaseWarpEnabled(cmp.Warp) {
		caveats = append(caveats, caveat{
			Title: "The WARP phase was not measured over WARP",
			Body: fmt.Sprintf(
				"The trace endpoint reported `warp=%s` at the end of the %s phase, so any difference below is not attributable to WARP. The comparison is shown for completeness; it must not be published as a WARP result.",
				lastTrace(cmp.Warp).Warp, cmp.Warp.Phase),
		})
	}

	if phaseWarpEnabled(cmp.Baseline) {
		caveats = append(caveats, caveat{
			Title: "The baseline was measured with WARP already on",
			Body: fmt.Sprintf(
				"The trace endpoint reported `warp=%s` at the end of the %s phase, so the baseline is not a raw ISP measurement and the two halves are not a comparison of ISP against WARP.",
				lastTrace(cmp.Baseline).Warp, cmp.Baseline.Phase),
		})
	}

	// --- what the targets actually are -----------------------------------

	if ids := serversWithFlag(both, "footnote:cloudflare-edge"); len(ids) > 0 {
		caveats = append(caveats, caveat{
			Title: "Cloudflare edge is not an end-to-end measurement",
			Body: fmt.Sprintf(
				"These targets terminate at the nearest Cloudflare edge: %s. With WARP enabled the path never leaves Cloudflare's network at all, so they measure the ISP-to-edge hop and must not be read as international results. They are kept because they are the only upload targets available in some groups, and because the contrast with the other targets is itself informative.",
				joinIDs(ids)),
		})
	}

	if ids := serversWithFlag(both, "control:domestic"); len(ids) > 0 {
		caveats = append(caveats, caveat{
			Title: "Domestic controls",
			Body: fmt.Sprintf(
				"These sit on the domestic path: %s. WARP is not expected to improve them, and they are included precisely so a reader can see that any improvement elsewhere is specific to international transit rather than a general uplift.",
				joinIDs(ids)),
		})
	}

	if ids := serversWithFlag(both, "best-effort"); len(ids) > 0 {
		caveats = append(caveats, caveat{
			Title: "Best-effort targets",
			Body: fmt.Sprintf(
				"Community-run or provider-run public test endpoints: %s. They can be busy, rate-limited or retired without notice, so a missing or anomalous row is more likely to be the target than the path.",
				joinIDs(ids)),
		})
	}

	// --- how latency was measured ----------------------------------------

	if ids := servicedBy(both, func(s results.Server) bool {
		return s.Ping != nil && s.Ping.Method == "tcp"
	}); len(ids) > 0 {
		caveats = append(caveats, caveat{
			Title: "Latency measured by TCP connect, not ICMP",
			Body: fmt.Sprintf(
				"ICMP was unavailable on these targets, so their latency is TCP connect time to port 443: %s. That is a different quantity measured at a different layer, and it is not comparable with the ICMP rows above it, nor with any other run's ICMP results.",
				joinIDs(ids)),
		})
	}

	// --- measurement quality ---------------------------------------------

	if ids := servicedBy(both, hasUndersizedSample); len(ids) > 0 {
		caveats = append(caveats, caveat{
			Title: "Undersized throughput samples",
			Body: fmt.Sprintf(
				"These transferred less than the minimum payload for the measurement window, so their rates rest on a shorter sample than the rest: %s. Treat those figures as indicative rather than comparable.",
				joinIDs(ids)),
		})
	}

	if ids := servicedBy(both, hasPartialSample); len(ids) > 0 {
		caveats = append(caveats, caveat{
			Title: "Interrupted samples",
			Body: fmt.Sprintf(
				"At least one sample ended early after data had already moved on: %s. The rate is usable, but the window was shorter than requested.",
				joinIDs(ids)),
		})
	}

	if ids := servicedBy(both, hasTruncatedSample); len(ids) > 0 {
		caveats = append(caveats, caveat{
			Title: "Byte-capped samples",
			Body: fmt.Sprintf(
				"These hit the payload guard before the clock expired, so their window was ended by the cap rather than by time: %s.",
				joinIDs(ids)),
		})
	}

	// --- did anything move underneath the measurement? --------------------

	if ids := changedAddress(both); len(ids) > 0 {
		caveats = append(caveats, caveat{
			Title: "Resolved address changed between phases",
			Body: fmt.Sprintf(
				"These resolved to a different address in each phase: %s. That is expected under WARP, where anycast and routing change, but it means the two rows are not necessarily the same server.",
				joinIDs(ids)),
		})
	}

	if changed := phasesWithChangingState(cmp); len(changed) > 0 {
		caveats = append(caveats, caveat{
			Title: "WARP state changed during a phase",
			Body: fmt.Sprintf(
				"The trace endpoint reported a different state at the end of the %s phase than at the start. Results from that phase may span two different paths.",
				strings.Join(changed, " and ")),
		})
	}

	// --- what the run itself reported ------------------------------------

	if len(cmp.Warnings) > 0 {
		caveats = append(caveats, caveat{
			Title: "Run warnings",
			Body:  bulletList(cmp.Warnings),
		})
	}

	if warnings := fileWarnings(cmp); len(warnings) > 0 {
		caveats = append(caveats, caveat{
			Title: "Recorded during measurement",
			Body:  bulletList(warnings),
		})
	}

	return caveats
}

// phaseWarpEnabled reports whether a phase actually ran over WARP.
func phaseWarpEnabled(f *results.File) bool {
	switch lastTrace(f).Warp {
	case "on", "plus":
		return true
	default:
		return false
	}
}

// serversWithFlag returns the ids carrying a server-list flag.
func serversWithFlag(servers []results.Server, flag string) []string {
	return dedupeSorted(servicedBy(servers, func(s results.Server) bool {
		for _, f := range s.Flags {
			if f == flag {
				return true
			}
		}
		return false
	}))
}

func servicedBy(servers []results.Server, match func(results.Server) bool) []string {
	var out []string
	for _, s := range servers {
		if match(s) {
			out = append(out, s.ID)
		}
	}
	return out
}

func hasUndersizedSample(s results.Server) bool {
	return sampleMatch(s, func(x results.Sample) bool { return x.Undersized })
}
func hasPartialSample(s results.Server) bool {
	return sampleMatch(s, func(x results.Sample) bool { return x.Partial })
}
func hasTruncatedSample(s results.Server) bool {
	return sampleMatch(s, func(x results.Sample) bool { return x.Truncated })
}

func sampleMatch(s results.Server, match func(results.Sample) bool) bool {
	for _, series := range []*results.Series{s.Download, s.Upload} {
		if series == nil {
			continue
		}
		for _, sample := range series.Samples {
			if match(sample) {
				return true
			}
		}
	}
	return false
}

// changedAddress returns servers whose resolved address differs between the two
// halves of the concatenated slice.
func changedAddress(servers []results.Server) []string {
	seen := map[string]string{}
	changed := map[string]bool{}

	for _, s := range servers {
		if s.ResolvedIP == "" {
			continue
		}
		if prev, ok := seen[s.ID]; ok && prev != s.ResolvedIP {
			changed[s.ID] = true
		}
		seen[s.ID] = s.ResolvedIP
	}

	var out []string
	for id := range changed {
		out = append(out, id)
	}
	return dedupeSorted(out)
}

// phasesWithChangingState returns the phases whose WARP state was not stable.
func phasesWithChangingState(cmp *results.Comparison) []string {
	var out []string
	for _, f := range []*results.File{cmp.Baseline, cmp.Warp} {
		if len(f.Traces) < 2 {
			continue
		}
		first, last := f.Traces[0].Warp, f.Traces[len(f.Traces)-1].Warp
		if first != last {
			out = append(out, fmt.Sprintf("%s (%s to %s)", f.Phase, first, last))
		}
	}
	return out
}

// fileWarnings gathers the run-level and per-server warnings both files
// recorded, so nothing captured during a run is lost on the way into the
// report.
func fileWarnings(cmp *results.Comparison) []string {
	var out []string
	for _, f := range []*results.File{cmp.Baseline, cmp.Warp} {
		for _, w := range f.Warnings {
			out = append(out, fmt.Sprintf("%s: %s", f.Phase, w))
		}
		for _, s := range f.Servers {
			for _, w := range s.Warnings {
				out = append(out, fmt.Sprintf("%s/%s: %s", f.Phase, s.ID, w))
			}
		}
	}
	return dedupeSorted(out)
}

func dedupeSorted(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// joinIDs renders a list of server ids, backticked so they read as identifiers.
func joinIDs(ids []string) string {
	out := make([]string, 0, len(ids))
	for _, id := range dedupeSorted(ids) {
		out = append(out, "`"+id+"`")
	}
	if len(out) == 1 {
		return out[0]
	}
	return strings.Join(out[:len(out)-1], ", ") + " and " + out[len(out)-1]
}

// bulletList renders warnings as a run-in list, kept to one bullet to avoid a
// wall of text in the caveats.
func bulletList(items []string) string {
	if len(items) == 1 {
		return items[0]
	}
	const maxShown = 6
	shown := items
	extra := 0
	if len(shown) > maxShown {
		extra = len(shown) - maxShown
		shown = shown[:maxShown]
	}

	parts := make([]string, 0, len(shown)+1)
	for _, item := range shown {
		parts = append(parts, "`"+item+"`")
	}
	out := strings.Join(parts, "; ")
	if extra > 0 {
		out += fmt.Sprintf("; and %d more", extra)
	}
	return out
}

// writeReproduce gives a reader everything needed to repeat the run.
func writeReproduce(b *strings.Builder, cmp *results.Comparison, opts Options) {
	b.WriteString("## Reproduce\n\n")

	if opts.CommandLine != "" {
		b.WriteString("```sh\n")
		b.WriteString(opts.CommandLine)
		b.WriteString("\n```\n\n")
	}

	b.WriteString("| | |\n|---|---|\n")
	fmt.Fprintf(b, "| Server list revision | %s |\n", orUnknown(cmp.Baseline.ServerList.Revision))
	fmt.Fprintf(b, "| Tool version | %s |\n", orUnknown(cmp.Baseline.Tool.Version))
	fmt.Fprintf(b, "| Phase order | baseline, then WARP |\n")
	b.WriteString("\n")

	b.WriteString("The full method, including the measurement windows, the slow-start\n")
	b.WriteString("exclusion and the fairness rules, is documented at\n")
	fmt.Fprintf(b, "<%s>.\n", opts.MethodologyURL)
}
