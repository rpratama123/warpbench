package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rpratama123/warpbench/internal/results"
	"github.com/rpratama123/warpbench/internal/trace"
)

// traceResult builds a trace reading for the state-check tests. An unspecified
// state means unknown, which is what an unreadable endpoint produces.
type traceResult struct {
	warp string
	err  string
}

func (t traceResult) result() trace.Result {
	state := trace.State(t.warp)
	if state == "" {
		state = trace.StateUnknown
	}
	return trace.Result{Warp: state, WarpRaw: t.warp, Err: t.err}
}

// resultFile builds a schema-valid file so the compare path can be exercised
// without running a measurement.
func resultFile(phase string, started time.Time, servers ...results.Server) *results.File {
	return &results.File{
		Schema:      results.SchemaVersion,
		Tool:        results.Tool{Version: "test"},
		Phase:       phase,
		StartedAt:   started,
		EndedAt:     started.Add(6 * time.Minute),
		DurationSec: 360,
		Configuration: results.Configuration{
			Mode: "quick", Groups: []string{"sg"}, ServerIDs: []string{"sg-1"},
			Parallel: 1, Masked: true,
		},
		ServerList:  results.ServerListRef{Schema: 2, Revision: "2026-09-20", Source: "remote"},
		Environment: results.Environment{OS: "linux", Arch: "amd64"},
		Traces: []results.Trace{
			{Stage: "before-" + phase, Warp: warpStateFor(phase), Colo: "SIN", IP: "203.0.113.0/24"},
			{Stage: "after-" + phase, Warp: warpStateFor(phase), Colo: "SIN", IP: "203.0.113.0/24"},
		},
		Servers:  servers,
		Warnings: []string{},
	}
}

func warpStateFor(phase string) string {
	if phase == "warp" {
		return "on"
	}
	return "off"
}

func measured(id string, mbps, avgMs float64) results.Server {
	return results.Server{
		ID: id, Name: id, Group: "sg", Protocol: "http-file",
		Ping: &results.Ping{Method: "icmp", Sent: 10, Received: 10, AvgMs: avgMs, JitterMs: 2},
		Download: &results.Series{Metric: "download", Parallel: 1, MedianSteadyMbps: mbps,
			Samples: []results.Sample{{Bytes: 1, ElapsedMs: 1, SteadyMbps: mbps}}},
		Warnings: []string{},
	}
}

func writePair(t *testing.T) (baselinePath, warpPath string) {
	t.Helper()
	dir := t.TempDir()

	baselinePath = filepath.Join(dir, "baseline.json")
	warpPath = filepath.Join(dir, "warp.json")

	base := resultFile("baseline", time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC),
		measured("sg-1", 40, 60), measured("sg-2", 40, 60))
	warp := resultFile("warp", time.Date(2026, 9, 20, 8, 10, 0, 0, time.UTC),
		measured("sg-1", 120, 50), measured("sg-2", 20, 80))

	if err := results.Write(baselinePath, base); err != nil {
		t.Fatal(err)
	}
	if err := results.Write(warpPath, warp); err != nil {
		t.Fatal(err)
	}
	return baselinePath, warpPath
}

// --- flag validation -------------------------------------------------------

func TestPhaseFlagIsValidated(t *testing.T) {
	for _, phase := range []string{"", "before", "BASELINE!", "warping"} {
		t.Run(phase, func(t *testing.T) {
			code, _, stderr := runCapture(t, []string{"--phase", phase}, neverTTY)
			if phase == "baseline" || phase == "warp" {
				return // valid values are exercised elsewhere
			}
			if code != exitUsage {
				t.Errorf("exit code = %d, want %d", code, exitUsage)
			}
			if !strings.Contains(stderr, "--phase") {
				t.Errorf("stderr = %q, want it to name the flag", stderr)
			}
		})
	}
}

func TestConflictingModesAreRejected(t *testing.T) {
	code, _, stderr := runCapture(t, []string{"--quick", "--extended", "--phase", "baseline"}, neverTTY)

	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestParallelMustBePositive(t *testing.T) {
	code, _, stderr := runCapture(t, []string{"--parallel", "0", "--phase", "baseline"}, neverTTY)

	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "--parallel") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestPositionalArgsRejectedWithoutCompare(t *testing.T) {
	code, _, stderr := runCapture(t, []string{"extra.json"}, neverTTY)

	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "unexpected argument") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestCompareNeedsExactlyTwoFiles(t *testing.T) {
	for _, args := range [][]string{
		{"--compare"},
		{"--compare", "only-one.json"},
		{"--compare", "a.json", "b.json", "c.json"},
	} {
		code, _, stderr := runCapture(t, args, neverTTY)
		if code != exitUsage {
			t.Errorf("%v: exit code = %d, want %d", args, code, exitUsage)
		}
		if !strings.Contains(stderr, "two result files") {
			t.Errorf("%v: stderr = %q", args, stderr)
		}
	}
}

// The comparison is not a result file, so emitting "JSON" for it would be
// meaningless.
func TestCompareRejectsJSONFlag(t *testing.T) {
	code, _, stderr := runCapture(t, []string{"--compare", "--json", "a.json", "b.json"}, neverTTY)

	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "--json applies to a measurement run") {
		t.Errorf("stderr = %q", stderr)
	}
}

// --- compare path ----------------------------------------------------------

func TestComparePrintsBothDirections(t *testing.T) {
	baseline, warp := writePair(t)

	code, stdout, stderr := runCapture(t, []string{"--compare", baseline, warp}, neverTTY)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}

	for _, want := range []string{
		"warpbench comparison",
		"baseline", "warp",
		"download (Mbps)",
		"latency avg (ms)",
		"sg-1", "sg-2",
		"+200.0%", // 40 -> 120
		"better",
		"worse", // 40 -> 20 download, and 60 -> 80 latency
		"Public IPs are masked",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q\n--- got ---\n%s", want, stdout)
		}
	}
}

// Passing the files the other way round means the same comparison, not an
// error.
func TestCompareAcceptsReversedOrder(t *testing.T) {
	baseline, warp := writePair(t)

	code, stdout, stderr := runCapture(t, []string{"--compare", warp, baseline}, neverTTY)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "+200.0%") {
		t.Errorf("reversed order changed the comparison:\n%s", stdout)
	}
}

func TestCompareReportsAMissingFile(t *testing.T) {
	_, warp := writePair(t)

	code, _, stderr := runCapture(t, []string{"--compare", filepath.Join(t.TempDir(), "nope.json"), warp}, neverTTY)
	if code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
	if !strings.Contains(stderr, "reading") {
		t.Errorf("stderr = %q, want it to explain the read failure", stderr)
	}
}

// Comparing two files from the same phase cannot mean anything.
func TestCompareRejectsTwoBaselines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.json")
	other := filepath.Join(dir, "b2.json")
	at := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)

	for _, p := range []string{path, other} {
		if err := results.Write(p, resultFile("baseline", at, measured("sg-1", 40, 60))); err != nil {
			t.Fatal(err)
		}
	}

	code, _, stderr := runCapture(t, []string{"--compare", path, other}, neverTTY)
	if code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
	if !strings.Contains(stderr, "both files are phase") {
		t.Errorf("stderr = %q", stderr)
	}
}

// A changed server list invalidates a like-for-like comparison unless forced.
func TestCompareRevisionGuardIsWiredToForce(t *testing.T) {
	dir := t.TempDir()
	baselinePath := filepath.Join(dir, "b.json")
	warpPath := filepath.Join(dir, "w.json")
	at := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)

	base := resultFile("baseline", at, measured("sg-1", 40, 60))
	base.ServerList.Revision = "2026-09-19"
	warp := resultFile("warp", at.Add(10*time.Minute), measured("sg-1", 80, 50))

	if err := results.Write(baselinePath, base); err != nil {
		t.Fatal(err)
	}
	if err := results.Write(warpPath, warp); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runCapture(t, []string{"--compare", baselinePath, warpPath}, neverTTY)
	if code != exitError {
		t.Errorf("exit code = %d, want %d for a revision mismatch", code, exitError)
	}
	if !strings.Contains(stderr, "--force") {
		t.Errorf("stderr = %q, want it to name the escape hatch", stderr)
	}

	code, stdout, stderr := runCapture(t, []string{"--compare", "--force", baselinePath, warpPath}, neverTTY)
	if code != exitOK {
		t.Fatalf("--force still failed: %s", stderr)
	}
	if !strings.Contains(stdout, "WARNING") {
		t.Errorf("stdout = %q, want the forced mismatch recorded", stdout)
	}
}

// --- helpers ---------------------------------------------------------------

func TestSplitList(t *testing.T) {
	tests := map[string][]string{
		"":               nil,
		"   ":            nil,
		"sg":             {"sg"},
		"id,sg":          {"id", "sg"},
		" id , sg , jp ": {"id", "sg", "jp"},
		"sg,,jp":         {"sg", "jp"},
	}

	for in, want := range tests {
		got := splitList(in)
		if len(got) != len(want) {
			t.Errorf("splitList(%q) = %v, want %v", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("splitList(%q) = %v, want %v", in, got, want)
				break
			}
		}
	}
}

func TestOnOff(t *testing.T) {
	if onOff(true) != "on" || onOff(false) != "off" {
		t.Error("onOff() is wrong")
	}
}

func TestDefaultOutPath(t *testing.T) {
	at := time.Date(2026, 9, 20, 14, 5, 0, 0, time.UTC)
	got := defaultOutPath("baseline", at)

	if got != "warpbench-20260920-1405-baseline.json" {
		t.Errorf("defaultOutPath() = %q", got)
	}
}

// The state check is what makes the comparison meaningful: a baseline measured
// with WARP already on looks fine and means nothing.
func TestCheckPhaseState(t *testing.T) {
	tests := map[string]struct {
		probe  traceResult
		phase  string
		force  bool
		wantOK bool
	}{
		"baseline with warp off": {traceResult{warp: "off"}, "baseline", false, true},
		"warp with warp on":      {traceResult{warp: "on"}, "warp", false, true},
		"warp plus is enough":    {traceResult{warp: "plus"}, "warp", false, true},
		"baseline while on":      {traceResult{warp: "on"}, "baseline", false, false},
		"warp while off":         {traceResult{warp: "off"}, "warp", false, false},
		"forced mismatch":        {traceResult{warp: "on"}, "baseline", true, true},
		"unknown is tolerated":   {traceResult{}, "baseline", false, true},
		"failed trace tolerated": {traceResult{err: "timeout"}, "baseline", false, true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := checkPhaseState(tc.probe.result(), tc.phase, tc.force, &stderr)

			if tc.wantOK && code != exitOK {
				t.Errorf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
			}
			if !tc.wantOK && code == exitOK {
				t.Error("exit code = 0, want a refusal")
			}
			if tc.force && code == exitOK && !strings.Contains(stderr.String(), "--force") {
				t.Errorf("stderr = %q, want the override recorded", stderr.String())
			}
		})
	}
}

// --- dispatch --------------------------------------------------------------

// Choosing what to measure is what the interactive screens are for, so a
// terminal and no stated phase means the TUI. Naming a phase, or asking for
// JSON, is a scriptable request and must stay on the plain path.
func TestWantsTUI(t *testing.T) {
	tests := map[string]struct {
		opts options
		tty  func() bool
		want bool
	}{
		"terminal, no phase":    {options{}, alwaysTTY, true},
		"terminal, phase given": {options{phase: "baseline"}, alwaysTTY, false},
		"terminal, json":        {options{jsonOut: true}, alwaysTTY, false},
		"terminal, --no-tty":    {options{noTTY: true}, alwaysTTY, false},
		"terminal, blank phase": {options{phase: "   "}, alwaysTTY, true},
		"no terminal":           {options{}, neverTTY, false},
		"no terminal, no phase": {options{phase: ""}, neverTTY, false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := wantsTUI(tc.opts, tc.tty); got != tc.want {
				t.Errorf("wantsTUI() = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- markdown report -------------------------------------------------------

func TestReportWritesAMarkdownFile(t *testing.T) {
	baseline, warp := writePair(t)
	out := filepath.Join(t.TempDir(), "report.md")

	code, stdout, stderr := runCapture(t, []string{"--compare", "--report", out, baseline, warp}, neverTTY)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	if !strings.Contains(stderr, "wrote") {
		t.Errorf("stderr = %q, want it to name the file written", stderr)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the report was not written: %v", err)
	}
	body := string(data)

	for _, want := range []string{
		"# warpbench",
		"## Summary",
		"## Phases",
		"## Download",
		"## Caveats",
		"## Reproduce",
		// The reproduce block must carry the actual invocation.
		"--compare",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("report is missing %q", want)
		}
	}

	// The plain table still prints, because the report went to a file.
	if !strings.Contains(stdout, "warpbench comparison") {
		t.Error("writing a report to a file should not suppress the comparison table")
	}
}

// Writing the report to stdout replaces the table: interleaving two documents
// would produce something neither readable nor pasteable.
func TestReportToStdoutReplacesTheTable(t *testing.T) {
	baseline, warp := writePair(t)

	code, stdout, stderr := runCapture(t, []string{"--compare", "--report", "-", baseline, warp}, neverTTY)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	if !strings.HasPrefix(stdout, "# warpbench") {
		t.Errorf("stdout should be the report:\n%s", stdout)
	}
	if strings.Contains(stdout, "warpbench comparison\n") {
		t.Error("the plain table was interleaved with the Markdown")
	}
}

func TestReportWriteFailureIsReported(t *testing.T) {
	baseline, warp := writePair(t)
	// A directory cannot be opened as a file.
	code, _, stderr := runCapture(t, []string{"--compare", "--report", t.TempDir(), baseline, warp}, neverTTY)

	if code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
	if !strings.Contains(stderr, "warpbench:") {
		t.Errorf("stderr = %q, want a diagnostic", stderr)
	}
}

func TestReportFlagAppearsInUsage(t *testing.T) {
	_, stdout, _ := runCapture(t, []string{"--help"}, neverTTY)

	if !strings.Contains(stdout, "--report") {
		t.Error("usage does not document --report")
	}
}
