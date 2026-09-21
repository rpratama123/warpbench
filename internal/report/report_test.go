package report

import (
	"strings"
	"testing"
	"time"

	"github.com/rpratama123/warpbench/internal/results"
)

// minimalFile builds a schema-valid file with the given servers.
func minimalFile(phase string, servers ...results.Server) *results.File {
	at := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)
	if phase == "warp" {
		at = at.Add(10 * time.Minute)
	}
	warp := "off"
	if phase == "warp" {
		warp = "on"
	}

	return &results.File{
		Schema:      results.SchemaVersion,
		Tool:        results.Tool{Version: "v0.1.0", Go: "go1.24.4"},
		Phase:       phase,
		StartedAt:   at,
		EndedAt:     at.Add(5 * time.Minute),
		DurationSec: 300,
		Configuration: results.Configuration{
			Mode: "quick", Groups: []string{"sg"}, ServerIDs: []string{"sg-1"},
			Parallel: 1, Masked: true,
		},
		ServerList:  results.ServerListRef{Schema: 2, Revision: "2026-09-20", Source: "remote"},
		Environment: results.Environment{OS: "linux", Arch: "amd64", Timezone: "UTC"},
		Traces: []results.Trace{
			{Stage: "before-" + phase, Warp: warp, Colo: "SIN", IP: "203.0.113.0/24"},
			{Stage: "after-" + phase, Warp: warp, Colo: "SIN", IP: "203.0.113.0/24"},
		},
		Servers:  servers,
		Warnings: []string{},
	}
}

func simpleServer(id string) results.Server {
	return results.Server{
		ID: id, Name: id, Group: "sg", Protocol: "http-file",
		Ping: &results.Ping{Method: "icmp", Sent: 10, Received: 10, AvgMs: 42},
		Download: &results.Series{Metric: "download", Parallel: 1, MedianSteadyMbps: 40,
			Samples: []results.Sample{{Bytes: 1, ElapsedMs: 12000, SteadyMbps: 40, Proto: "HTTP/1.1"}}},
		Warnings: []string{},
	}
}

func compareOf(t *testing.T, baseline, warp *results.File) *results.Comparison {
	t.Helper()
	cmp, err := results.Compare(baseline, warp, results.CompareOptions{Force: true})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	return cmp
}

func render(t *testing.T, cmp *results.Comparison) string {
	t.Helper()
	return string(Markdown(cmp, Options{CommandLine: "warpbench --compare a.json b.json"}))
}

// --- document structure ----------------------------------------------------

func TestReportHasEveryRequiredSection(t *testing.T) {
	got := render(t, fixture())

	for _, want := range []string{
		"# warpbench: raw ISP path vs Cloudflare WARP",
		"## Summary",
		"## Phases",
		"## Download",
		"## Latency (average)",
		"## Caveats",
		"## Reproduce",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q", want)
		}
	}
}

// The report must state the scale convention, or a reader will read a
// zero-based bar as evidence that two similar latencies are identical.
func TestReportExplainsTheChartScale(t *testing.T) {
	got := render(t, fixture())

	if !strings.Contains(got, "zero-based") {
		t.Error("the report should explain that bars are zero-based")
	}
}

func TestReportIncludesMetadata(t *testing.T) {
	got := render(t, fixture())

	for _, want := range []string{"revision 2026-09-20", "linux/amd64", "IPv4", "HTTP/1.1", "quick"} {
		if !strings.Contains(got, want) {
			t.Errorf("report metadata is missing %q", want)
		}
	}
}

func TestReportNotesMasking(t *testing.T) {
	got := render(t, fixture())
	if !strings.Contains(got, "masked to their network prefix") {
		t.Error("a masked report should say so")
	}

	base := minimalFile("baseline", simpleServer("sg-1"))
	warp := minimalFile("warp", simpleServer("sg-1"))
	base.Configuration.Masked = false
	warp.Configuration.Masked = false

	if got := render(t, compareOf(t, base, warp)); strings.Contains(got, "masked to their network prefix") {
		t.Error("an unmasked report should not claim masking")
	}
}

// A server measured only in one phase must be named rather than silently
// missing from the table.
func TestReportNamesServersMissingFromAPhase(t *testing.T) {
	base := minimalFile("baseline", simpleServer("sg-1"), simpleServer("sg-2"))
	warp := minimalFile("warp", simpleServer("sg-1"))

	got := render(t, compareOf(t, base, warp))

	if !strings.Contains(got, "## Not measured in both phases") {
		t.Error("missing the one-sided section")
	}
	if !strings.Contains(got, "sg-2") {
		t.Error("the one-sided server was not named")
	}
	if !strings.Contains(got, "not measured and measured zero are different findings") {
		t.Error("the section should explain why the row is absent")
	}
}

func TestReportOmitsTheOneSidedSectionWhenThereIsNone(t *testing.T) {
	base := minimalFile("baseline", simpleServer("sg-1"))
	warp := minimalFile("warp", simpleServer("sg-1"))

	if got := render(t, compareOf(t, base, warp)); strings.Contains(got, "## Not measured in both phases") {
		t.Error("the section should be absent when nothing is one-sided")
	}
}

// A metric that neither phase measured is not part of this run, so naming it
// would invent a finding about a measurement that was never attempted.
//
// This is the rule the TUI used to get wrong, which made the two front ends
// disagree about the same run.
func TestReportIgnoresMetricsNeitherPhaseMeasured(t *testing.T) {
	// Present in both phases, but with no download recorded in either.
	noDownload := simpleServer("sg-2")
	noDownload.Download = nil

	base := minimalFile("baseline", simpleServer("sg-1"), noDownload)
	warp := minimalFile("warp", simpleServer("sg-1"), noDownload)

	if got := render(t, compareOf(t, base, warp)); strings.Contains(got, "## Not measured in both phases") {
		t.Errorf("a metric neither phase measured was reported as a gap:\n%s", got)
	}
}

func TestReportSaysWhenThereAreNoCaveats(t *testing.T) {
	base := minimalFile("baseline", simpleServer("sg-1"))
	warp := minimalFile("warp", simpleServer("sg-1"))

	got := render(t, compareOf(t, base, warp))
	if !strings.Contains(got, "None: every target was measured the same way") {
		t.Errorf("expected an explicit all-clear, got:\n%s", got)
	}
}

func TestReportReproduceBlock(t *testing.T) {
	got := string(Markdown(fixture(), Options{CommandLine: "warpbench --quick --phase baseline"}))

	for _, want := range []string{"## Reproduce", "warpbench --quick --phase baseline", "METHODOLOGY.md", "Server list revision"} {
		if !strings.Contains(got, want) {
			t.Errorf("reproduce block is missing %q", want)
		}
	}
}

func TestReportUsesACustomMethodologyURL(t *testing.T) {
	got := string(Markdown(fixture(), Options{MethodologyURL: "https://example.invalid/method"}))

	if !strings.Contains(got, "https://example.invalid/method") {
		t.Error("the custom methodology URL was ignored")
	}
}

// --- caveats ---------------------------------------------------------------

func caveatText(t *testing.T, cmp *results.Comparison) string {
	t.Helper()
	got := render(t, cmp)
	start := strings.Index(got, "## Caveats")
	if start < 0 {
		t.Fatal("no caveats section")
	}
	end := strings.Index(got[start:], "## Reproduce")
	if end < 0 {
		return got[start:]
	}
	return got[start : start+end]
}

func TestCaveatForTheCloudflareEdge(t *testing.T) {
	s := simpleServer("id-cf")
	s.Flags = []string{"footnote:cloudflare-edge"}

	got := caveatText(t, compareOf(t, minimalFile("baseline", s), minimalFile("warp", s)))

	if !strings.Contains(got, "never leaves Cloudflare's network") {
		t.Errorf("missing the Cloudflare caveat:\n%s", got)
	}
	if !strings.Contains(got, "id-cf") {
		t.Error("the caveat should name the target")
	}
}

func TestCaveatForDomesticControls(t *testing.T) {
	s := simpleServer("id-mirror")
	s.Flags = []string{"control:domestic"}

	got := caveatText(t, compareOf(t, minimalFile("baseline", s), minimalFile("warp", s)))

	if !strings.Contains(got, "domestic path") {
		t.Errorf("missing the domestic-control caveat:\n%s", got)
	}
}

func TestCaveatForBestEffortTargets(t *testing.T) {
	s := simpleServer("sg-iperf")
	s.Flags = []string{"best-effort"}

	got := caveatText(t, compareOf(t, minimalFile("baseline", s), minimalFile("warp", s)))

	if !strings.Contains(got, "rate-limited or retired") {
		t.Errorf("missing the best-effort caveat:\n%s", got)
	}
}

// An ICMP substitution changes what the number means, so it can never be left
// to a footnote in a log file.
func TestCaveatForTCPLatencySubstitution(t *testing.T) {
	s := simpleServer("sg-1")
	s.Ping = &results.Ping{Method: "tcp", Sent: 10, Received: 10, AvgMs: 42,
		Warning: "ICMP unavailable, so latency is TCP connect time"}

	got := caveatText(t, compareOf(t, minimalFile("baseline", s), minimalFile("warp", s)))

	if !strings.Contains(got, "TCP connect time") {
		t.Errorf("missing the ICMP-substitution caveat:\n%s", got)
	}
	if !strings.Contains(got, "not comparable") {
		t.Error("the caveat must say the figures are not comparable")
	}
}

func TestCaveatForUndersizedSamples(t *testing.T) {
	s := simpleServer("sg-1")
	s.Download.Samples[0].Undersized = true

	got := caveatText(t, compareOf(t, minimalFile("baseline", s), minimalFile("warp", s)))

	if !strings.Contains(got, "Undersized throughput samples") {
		t.Errorf("missing the undersized caveat:\n%s", got)
	}
}

func TestCaveatForInterruptedAndCappedSamples(t *testing.T) {
	s := simpleServer("sg-1")
	s.Download.Samples[0].Partial = true
	s.Download.Samples[0].Truncated = true

	got := caveatText(t, compareOf(t, minimalFile("baseline", s), minimalFile("warp", s)))

	for _, want := range []string{"Interrupted samples", "Byte-capped samples"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

func TestCaveatForAChangedAddress(t *testing.T) {
	base := simpleServer("sg-1")
	base.ResolvedIP = "203.0.113.9"
	warp := simpleServer("sg-1")
	warp.ResolvedIP = "198.51.100.4"

	got := caveatText(t, compareOf(t, minimalFile("baseline", base), minimalFile("warp", warp)))

	if !strings.Contains(got, "different address in each phase") {
		t.Errorf("missing the address-change caveat:\n%s", got)
	}
}

// A phase that spanned two WARP states cannot support a clean conclusion.
func TestCaveatForAStateChangeMidPhase(t *testing.T) {
	base := minimalFile("baseline", simpleServer("sg-1"))
	warp := minimalFile("warp", simpleServer("sg-1"))
	warp.Traces[0].Warp = "off"
	warp.Traces[1].Warp = "on"

	got := caveatText(t, compareOf(t, base, warp))

	if !strings.Contains(got, "different state at the end") {
		t.Errorf("missing the state-change caveat:\n%s", got)
	}
}

func TestCaveatCarriesRecordedWarnings(t *testing.T) {
	base := minimalFile("baseline", simpleServer("sg-1"))
	base.Warnings = []string{"the server list came from the cache"}
	base.Servers[0].Warnings = []string{"timings: 1 of 4 probes failed"}
	warp := minimalFile("warp", simpleServer("sg-1"))

	got := caveatText(t, compareOf(t, base, warp))

	for _, want := range []string{"came from the cache", "1 of 4 probes failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing recorded warning %q:\n%s", want, got)
		}
	}
}

// The gap between phases is the obvious alternative explanation for any
// difference, so it must reach the report.
func TestCaveatForTheInterPhaseGap(t *testing.T) {
	base := minimalFile("baseline", simpleServer("sg-1"))
	warp := minimalFile("warp", simpleServer("sg-1"))
	warp.StartedAt = base.StartedAt.Add(2 * time.Hour)
	warp.EndedAt = warp.StartedAt.Add(5 * time.Minute)

	got := caveatText(t, compareOf(t, base, warp))

	if !strings.Contains(got, "apart") {
		t.Errorf("missing the gap caveat:\n%s", got)
	}
}

func TestCaveatsAreDeduplicatedAndSorted(t *testing.T) {
	s := simpleServer("sg-1")
	s.Flags = []string{"best-effort"}
	// The same server in both phases must not produce the caveat twice.
	got := caveatText(t, compareOf(t, minimalFile("baseline", s), minimalFile("warp", s)))

	if n := strings.Count(got, "Best-effort targets"); n != 1 {
		t.Errorf("the best-effort caveat appeared %d times", n)
	}
}

// --- layout ----------------------------------------------------------------

// Markdown is read in editors and on GitHub, so a chart line that overflows a
// code block wraps and becomes unreadable.
func TestReportChartsFitTheConfiguredWidth(t *testing.T) {
	for _, width := range []int{40, 56, 72, 100} {
		got := string(Markdown(fixture(), Options{Width: width, CommandLine: "x"}))

		inBlock := false
		for _, line := range strings.Split(got, "\n") {
			if strings.HasPrefix(line, "```text") {
				inBlock = true
				continue
			}
			if strings.HasPrefix(line, "```") {
				inBlock = false
				continue
			}
			if inBlock && len([]rune(line)) > width {
				t.Errorf("width %d: chart line is %d columns: %q", width, len([]rune(line)), line)
			}
		}
	}
}

func TestReportIsDeterministic(t *testing.T) {
	first := render(t, fixture())
	second := render(t, fixture())

	if first != second {
		t.Error("rendering the same comparison twice produced different reports")
	}
}

func TestFormatValuePicksPrecisionByUnit(t *testing.T) {
	if got := formatValue(" Mbps", 41.234); got != "41.23" {
		t.Errorf("formatValue(Mbps) = %q", got)
	}
	if got := formatValue(" ms", 41.234); got != "41.2" {
		t.Errorf("formatValue(ms) = %q", got)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := map[time.Duration]string{
		45 * time.Second:                "45s",
		6 * time.Minute:                 "6m00s",
		75*time.Minute + 30*time.Second: "75m30s",
	}
	for in, want := range tests {
		if got := formatDuration(in); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestOrUnknown(t *testing.T) {
	if got := orUnknown(""); got != "unknown" {
		t.Errorf("orUnknown(\"\") = %q", got)
	}
	if got := orUnknown("  "); got != "unknown" {
		t.Errorf("orUnknown(blank) = %q", got)
	}
	if got := orUnknown("SIN"); got != "SIN" {
		t.Errorf("orUnknown(SIN) = %q", got)
	}
}

// --- did the phases measure what they claim? -------------------------------

// The single most consequential thing a report can get wrong: presenting a
// comparison as a WARP result when the WARP phase was not over WARP.
func TestCaveatWhenTheWarpPhaseWasNotOverWARP(t *testing.T) {
	base := minimalFile("baseline", simpleServer("sg-1"))
	warp := minimalFile("warp", simpleServer("sg-1"))
	// The WARP phase ran with WARP still off, as --force permits.
	warp.Traces[0].Warp = "off"
	warp.Traces[1].Warp = "off"

	got := caveatText(t, compareOf(t, base, warp))

	if !strings.Contains(got, "not measured over WARP") {
		t.Errorf("missing the headline honesty caveat:\n%s", got)
	}
	if !strings.Contains(got, "must not be published as a WARP result") {
		t.Error("the caveat should say the result is not publishable as a WARP result")
	}
}

// It must lead, not be buried: a reader who stops after the first caveat should
// still learn that the comparison does not hold.
func TestTheNotOverWARPCaveatComesFirst(t *testing.T) {
	base := minimalFile("baseline", simpleServer("sg-1"))
	warp := minimalFile("warp", simpleServer("sg-1"))
	warp.Traces[1].Warp = "off"
	base.Servers[0].Flags = []string{"best-effort"}

	got := caveatText(t, compareOf(t, base, warp))

	notOver := strings.Index(got, "not measured over WARP")
	bestEffort := strings.Index(got, "Best-effort targets")
	if notOver < 0 || bestEffort < 0 {
		t.Fatalf("expected both caveats:\n%s", got)
	}
	if notOver > bestEffort {
		t.Error("the WARP-not-enabled caveat should come before the target caveats")
	}
}

func TestCaveatWhenTheBaselineWasAlreadyOverWARP(t *testing.T) {
	base := minimalFile("baseline", simpleServer("sg-1"))
	base.Traces[1].Warp = "on"
	warp := minimalFile("warp", simpleServer("sg-1"))

	got := caveatText(t, compareOf(t, base, warp))

	if !strings.Contains(got, "baseline was measured with WARP already on") {
		t.Errorf("missing the contaminated-baseline caveat:\n%s", got)
	}
}

func TestNoStateCaveatsWhenThePhasesAreCorrect(t *testing.T) {
	got := caveatText(t, compareOf(t, minimalFile("baseline", simpleServer("sg-1")), minimalFile("warp", simpleServer("sg-1"))))

	for _, unwanted := range []string{"not measured over WARP", "already on"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("correct phases produced %q:\n%s", unwanted, got)
		}
	}
}

// A "plus" reading is still WARP.
func TestWarpPlusCountsAsEnabled(t *testing.T) {
	base := minimalFile("baseline", simpleServer("sg-1"))
	warp := minimalFile("warp", simpleServer("sg-1"))
	warp.Traces[1].Warp = "plus"

	got := caveatText(t, compareOf(t, base, warp))
	if strings.Contains(got, "not measured over WARP") {
		t.Errorf("warp=plus should count as enabled:\n%s", got)
	}
}

// An unknown state cannot be assumed to be fine either.
func TestUnknownWarpStateIsFlagged(t *testing.T) {
	base := minimalFile("baseline", simpleServer("sg-1"))
	warp := minimalFile("warp", simpleServer("sg-1"))
	warp.Traces[1].Warp = "unknown"

	if got := caveatText(t, compareOf(t, base, warp)); !strings.Contains(got, "not measured over WARP") {
		t.Errorf("an unknown state should not be treated as enabled:\n%s", got)
	}
}
