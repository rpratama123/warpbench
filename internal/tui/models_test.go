package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rpratama123/warpbench/internal/results"
	"github.com/rpratama123/warpbench/internal/serverlist"
	"github.com/rpratama123/warpbench/internal/trace"
)

// --- bridge ----------------------------------------------------------------

// The bridge is the only channel between the measurement goroutine and the UI,
// so every runner callback must arrive as a message.
func TestBridgeForwardsEveryEvent(t *testing.T) {
	b := NewProgressBridge()

	b.PhaseStarted("baseline", []serverlist.Server{srv("sg-1", "sg", "quick", "ping")}, time.Second)
	b.ServerStarted(1, 1, srv("sg-1", "sg", "quick", "ping"))
	b.Step(srv("sg-1", "sg", "quick", "ping"), "download")
	b.Trace(trace.Result{Stage: "before-baseline", Warp: trace.StateOff})
	b.ServerDone(srv("sg-1", "sg", "quick", "ping"), results.Server{ID: "sg-1"})

	want := []string{"phaseStartedMsg", "serverStartedMsg", "stepMsg", "traceMsg", "serverDoneMsg"}
	for i, expected := range want {
		select {
		case msg := <-b.Messages():
			if got := typeName(msg); got != expected {
				t.Errorf("message %d = %s, want %s", i, got, expected)
			}
		default:
			t.Fatalf("message %d (%s) never arrived", i, expected)
		}
	}
}

func typeName(v any) string {
	switch v.(type) {
	case phaseStartedMsg:
		return "phaseStartedMsg"
	case serverStartedMsg:
		return "serverStartedMsg"
	case stepMsg:
		return "stepMsg"
	case traceMsg:
		return "traceMsg"
	case serverDoneMsg:
		return "serverDoneMsg"
	case phaseDoneMsg:
		return "phaseDoneMsg"
	default:
		return "unknown"
	}
}

// --- progress --------------------------------------------------------------

func progressWith(servers ...serverlist.Server) ProgressModel {
	m := NewProgressModel(NewTheme(false))
	m, _ = m.Update(phaseStartedMsg{phase: "baseline", servers: servers, estimate: time.Minute})
	return m
}

func TestProgressBuildsARowPerServer(t *testing.T) {
	m := progressWith(
		srv("sg-1", "sg", "quick", "ping", "download"),
		srv("sg-2", "sg", "quick", "ping", "download"),
	)

	if len(m.servers) != 2 || m.total != 2 {
		t.Fatalf("got %d rows for %d total", len(m.servers), m.total)
	}
	if !strings.Contains(m.View(), "0/2") {
		t.Errorf("view does not show progress:\n%s", m.View())
	}
}

func TestProgressCellsFillIn(t *testing.T) {
	m := progressWith(srv("sg-1", "sg", "quick", "ping", "download"))

	m, _ = m.Update(stepMsg{server: srv("sg-1", "sg", "quick", "ping"), step: "download"})
	if !strings.Contains(m.View(), "download") {
		t.Errorf("the current step should be visible:\n%s", m.View())
	}

	m, _ = m.Update(serverDoneMsg{
		server: srv("sg-1", "sg", "quick", "ping"),
		measured: results.Server{
			ID:       "sg-1",
			Ping:     &results.Ping{Method: "icmp", Sent: 10, Received: 10, AvgMs: 42},
			Download: &results.Series{Metric: "download", Samples: []results.Sample{{Bytes: 1, ElapsedMs: 1}}, MedianSteadyMbps: 41.2},
		},
	})

	view := m.View()
	for _, want := range []string{"✓", "ping 42ms", "dl 41.2", "1/1"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
	if m.completed() != 1 {
		t.Errorf("completed() = %d, want 1", m.completed())
	}
}

// A server must never be lost just because its start message was missed.
func TestProgressToleratesAnUnknownServer(t *testing.T) {
	m := progressWith(srv("sg-1", "sg", "quick", "ping"))

	m, _ = m.Update(serverDoneMsg{server: srv("sg-2", "sg", "quick", "ping"), measured: results.Server{ID: "sg-2"}})

	if len(m.servers) != 2 {
		t.Fatalf("got %d rows, want the unknown server appended", len(m.servers))
	}
	if !strings.Contains(m.View(), "sg-2") {
		t.Error("the appended server is not visible")
	}
}

// The pre-run estimate is a promise; the ETA during a run should reflect what
// has actually happened.
func TestProgressETAProjection(t *testing.T) {
	m := progressWith(
		srv("a", "sg", "quick", "ping"), srv("b", "sg", "quick", "ping"),
		srv("c", "sg", "quick", "ping"), srv("d", "sg", "quick", "ping"),
	)
	start := time.Now()
	m.startedAt = start

	// Nothing done yet: fall back to the pre-run estimate.
	m.estimate = 4 * time.Minute
	if got := m.eta(); got != 4*time.Minute {
		t.Errorf("eta with nothing done = %v, want the estimate", got)
	}

	// One of four done in one minute: about three minutes remain.
	m.servers[0].done = true
	m.now = func() time.Time { return start.Add(time.Minute) }
	if got := m.eta(); got != 3*time.Minute {
		t.Errorf("eta = %v, want 3m", got)
	}

	// A wild projection is clamped to the pre-run estimate rather than
	// promising a finish time nobody believes.
	m.servers[1].done = true
	m.servers[2].done = true
	m.now = func() time.Time { return start.Add(3 * time.Second) }
	if got := m.eta(); got > 2*4*time.Minute {
		t.Errorf("eta = %v, want it clamped", got)
	}
}

func TestProgressBar(t *testing.T) {
	if got := progressBar(0, 4, 8); got != "[........]" {
		t.Errorf("progressBar(0/4) = %q", got)
	}
	if got := progressBar(4, 4, 8); got != "[########]" {
		t.Errorf("progressBar(4/4) = %q", got)
	}
	if got := progressBar(2, 4, 8); got != "[####....]" {
		t.Errorf("progressBar(2/4) = %q", got)
	}
	if got := progressBar(1, 0, 8); got != "" {
		t.Errorf("progressBar with no total = %q, want empty", got)
	}
	// An over-count must not produce a bar wider than asked for.
	if got := progressBar(9, 4, 8); len(got) != 10 {
		t.Errorf("progressBar over-count = %q", got)
	}
}

func TestProgressShowsFailures(t *testing.T) {
	m := progressWith(srv("sg-1", "sg", "quick", "ping"))
	m, _ = m.Update(phaseDoneMsg{err: errors.New("connection reset")})

	if !strings.Contains(m.View(), "connection reset") {
		t.Errorf("a failed phase should say so:\n%s", m.View())
	}
}

func TestProgressViewFitsTheWidth(t *testing.T) {
	m := progressWith(
		srv("id-myrepublic-iperf3", "id", "quick", "ping", "download", "upload"),
		srv("eu-ls-prg-cesnet", "eu", "quick", "ping", "download", "upload", "timings"),
	)
	m, _ = m.Update(serverDoneMsg{
		server: srv("id-myrepublic-iperf3", "id", "quick"),
		measured: results.Server{
			ID:       "id-myrepublic-iperf3",
			Ping:     &results.Ping{AvgMs: 21.7},
			Timings:  &results.Timings{TTFBMs: 22.4},
			Download: &results.Series{MedianSteadyMbps: 1.57},
			Upload:   &results.Series{MedianSteadyMbps: 319.92},
			Warnings: []string{"download: undersized"},
		},
	})

	for _, width := range []int{30, 40, 60, 80, 120} {
		sized, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		sm := sized
		for _, line := range strings.Split(sm.View(), "\n") {
			if got := len([]rune(line)); got > width {
				t.Errorf("width %d: line is %d columns: %q", width, got, line)
			}
		}
	}
}

// --- pause -----------------------------------------------------------------

// cannedCheck returns a command producing a fixed trace reading.
func cannedCheck(r trace.Result, err error) func() tea.Cmd {
	return func() tea.Cmd {
		return func() tea.Msg { return tracePollMsg{result: r, err: err} }
	}
}

func warpOn() trace.Result {
	return trace.Result{Stage: "warp-check", Warp: trace.StateOn, WarpRaw: "on", Colo: "SIN", IP: "203.0.113.9"}
}

func warpOff() trace.Result {
	return trace.Result{Stage: "warp-check", Warp: trace.StateOff, WarpRaw: "off", Colo: "SIN"}
}

func TestPauseChecksImmediatelyOnEntry(t *testing.T) {
	m := NewPauseModel(NewTheme(false), "warp", cannedCheck(warpOn(), nil))

	if cmd := m.Init(); cmd == nil {
		t.Fatal("the pause screen should ask to start checking as soon as it appears")
	}

	// Init cannot mutate a value receiver, so the state is recorded when Update
	// handles the request.
	m, cmd := m.Update(beginCheckMsg{})
	if cmd == nil {
		t.Fatal("no poll was scheduled")
	}
	if !m.checking {
		t.Error("checking should be true while a check is outstanding")
	}
	if m.attempts != 1 {
		t.Errorf("attempts = %d, want 1", m.attempts)
	}
}

func TestPauseConfirmsWhenWARPIsOn(t *testing.T) {
	m := NewPauseModel(NewTheme(false), "warp", cannedCheck(warpOn(), nil))
	m.Init()

	m, _ = m.Update(tracePollMsg{result: warpOn()})
	if !strings.Contains(m.View(), "WARP is on") {
		t.Errorf("view = %q", m.View())
	}

	m, _ = m.Update(key("enter"))
	if !m.Confirmed {
		t.Error("enter should confirm once WARP is on")
	}
}

// Accepting a phase the endpoint contradicts is exactly the failure this screen
// exists to prevent.
func TestPauseRefusesWhenWARPIsOff(t *testing.T) {
	m := NewPauseModel(NewTheme(false), "warp", cannedCheck(warpOff(), nil))
	m, _ = m.Update(beginCheckMsg{})
	m, _ = m.Update(tracePollMsg{result: warpOff()})

	m, _ = m.Update(key("enter"))

	if m.Confirmed {
		t.Fatal("enter confirmed while WARP was off")
	}
	if !strings.Contains(m.View(), "WARP is not on yet") {
		t.Errorf("the screen should explain the refusal:\n%s", m.View())
	}
}

// DoH-only mode reports warp=off even when the user believes WARP is on, and it
// is the most likely reason for being stuck here.
func TestPauseExplainsTheDoHCase(t *testing.T) {
	m := NewPauseModel(NewTheme(false), "warp", cannedCheck(warpOff(), nil))
	m, _ = m.Update(beginCheckMsg{})
	m, _ = m.Update(tracePollMsg{result: warpOff()})

	if !strings.Contains(m.View(), "DNS-only") {
		t.Errorf("view should explain DoH-only mode:\n%s", m.View())
	}
}

func TestPauseForceAllowsContinuing(t *testing.T) {
	m := NewPauseModel(NewTheme(false), "warp", cannedCheck(warpOff(), nil))
	m, _ = m.Update(beginCheckMsg{})
	m, _ = m.Update(tracePollMsg{result: warpOff()})

	m, _ = m.Update(key("enter"))
	if m.Confirmed {
		t.Fatal("setup failed: it confirmed without force")
	}

	m, _ = m.Update(key("f"))
	if !m.Force {
		t.Fatal("f did not set the override")
	}
	if !strings.Contains(m.View(), "override active") {
		t.Error("the override should be visible")
	}

	m, _ = m.Update(key("enter"))
	if !m.Confirmed {
		t.Error("enter should confirm once the override is set")
	}
}

// Turning WARP on should be noticed without the user pressing anything.
func TestPauseKeepsPollingWhileWARPIsOff(t *testing.T) {
	m := NewPauseModel(NewTheme(false), "warp", cannedCheck(warpOff(), nil))
	m, _ = m.Update(beginCheckMsg{})

	_, cmd := m.Update(tracePollMsg{result: warpOff()})
	if cmd == nil {
		t.Fatal("polling stopped while WARP was still off")
	}

	// Once on, polling stops: there is nothing left to wait for.
	_, cmd = m.Update(tracePollMsg{result: warpOn()})
	if cmd != nil {
		t.Error("polling continued after WARP came on")
	}
}

func TestPauseStopsPollingAfterTheAttemptLimit(t *testing.T) {
	m := NewPauseModel(NewTheme(false), "warp", cannedCheck(warpOff(), nil))

	// Drive a full set of poll cycles, the way the program would.
	var cmd tea.Cmd
	for range maxTraceChecks {
		m, cmd = m.Update(beginCheckMsg{})
		if cmd == nil {
			break
		}
		m, cmd = m.Update(tracePollMsg{result: warpOff()})
	}

	if cmd != nil {
		t.Error("polling continued past the attempt limit")
	}
	if !strings.Contains(m.View(), "checked") {
		t.Errorf("the attempt count should be visible:\n%s", m.View())
	}
}

func TestPauseRecordsAFailedCheckAsUnknown(t *testing.T) {
	m := NewPauseModel(NewTheme(false), "warp", cannedCheck(trace.Result{}, errors.New("timeout")))
	m, _ = m.Update(beginCheckMsg{})
	m, _ = m.Update(tracePollMsg{err: errors.New("dial timeout")})

	if !strings.Contains(m.View(), "could not be reached") {
		t.Errorf("a failed check should be explained, not shown as off:\n%s", m.View())
	}
	if !strings.Contains(m.View(), "timeout") {
		t.Errorf("the failure reason should be visible:\n%s", m.View())
	}
}

func TestPauseManualRecheckAndQuit(t *testing.T) {
	m := NewPauseModel(NewTheme(false), "warp", cannedCheck(warpOn(), nil))

	if _, cmd := m.Update(key("r")); cmd == nil {
		t.Error("r should trigger a check")
	}
	if _, cmd := m.Update(key("q")); cmd != nil {
		t.Error("quitting should not schedule work")
	}
	m, _ = m.Update(key("q"))
	if !m.Aborted {
		t.Error("q did not abort")
	}
}

func TestPauseViewFitsTheWidth(t *testing.T) {
	m := NewPauseModel(NewTheme(false), "warp", cannedCheck(warpOff(), nil))
	m, _ = m.Update(beginCheckMsg{})
	m, _ = m.Update(tracePollMsg{result: warpOff()})
	m, _ = m.Update(key("f"))

	for _, width := range []int{30, 40, 60, 80} {
		sized, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		sm := sized
		for _, line := range strings.Split(sm.View(), "\n") {
			if got := len([]rune(line)); got > width {
				t.Errorf("width %d: line is %d columns: %q", width, got, line)
			}
		}
	}
}

// --- results ---------------------------------------------------------------

func comparison() *results.Comparison {
	return &results.Comparison{
		Baseline: &results.File{Phase: "baseline", Configuration: results.Configuration{Masked: true}},
		Warp:     &results.File{Phase: "warp"},
		Servers: []results.ServerDelta{
			{
				ID: "sg-linode", Group: "sg", Protocol: "http-file",
				Download: &results.Delta{Metric: "download", Direction: results.HigherIsBetter,
					Baseline: 40, Warp: 120, HasBaseline: true, HasWarp: true, AbsDiff: 80, PctChange: 200},
				Upload: &results.Delta{Metric: "upload", Direction: results.HigherIsBetter,
					Baseline: 10, Warp: 9, HasBaseline: true, HasWarp: true, AbsDiff: -1, PctChange: -10},
				Latency: &results.Delta{Metric: "latency_avg", Direction: results.LowerIsBetter,
					Baseline: 60, Warp: 50, HasBaseline: true, HasWarp: true, AbsDiff: -10, PctChange: -16.7},
			},
			{
				ID: "jp-vultr", Group: "jp", Protocol: "http-file",
				Download: &results.Delta{Metric: "download", Direction: results.HigherIsBetter,
					Baseline: 30, Warp: 30.05, HasBaseline: true, HasWarp: true, AbsDiff: 0.05, PctChange: 0.17},
			},
			{
				ID: "eu-only-baseline", Group: "eu", Protocol: "http-file",
				Download: &results.Delta{Metric: "download", Direction: results.HigherIsBetter,
					Baseline: 20, HasBaseline: true, Note: "not measured over WARP"},
			},
		},
		Summary: results.Summary{
			Download: results.ServerTally{Improved: 1, Total: 2, MedianPct: 100},
		},
	}
}

func TestResultsPagesThroughMetrics(t *testing.T) {
	m := NewResultsModel(comparison(), NewTheme(false))

	if m.Pages() < 4 {
		t.Fatalf("Pages() = %d, want throughput, latency and more", m.Pages())
	}
	if m.Page() != 0 {
		t.Errorf("should open on the first page, got %d", m.Page())
	}

	m, _ = m.Update(key("right"))
	if m.Page() != 1 {
		t.Errorf("right did not advance: %d", m.Page())
	}

	// Paging past the end must clamp rather than wrap, so the user cannot get
	// lost.
	for range m.Pages() + 3 {
		m, _ = m.Update(key("right"))
	}
	if m.Page() != m.Pages()-1 {
		t.Errorf("Page() = %d, want it clamped to %d", m.Page(), m.Pages()-1)
	}

	for range m.Pages() + 3 {
		m, _ = m.Update(key("left"))
	}
	if m.Page() != 0 {
		t.Errorf("Page() = %d, want it clamped to 0", m.Page())
	}
}

func TestResultsShowsBothSeriesAndVerdicts(t *testing.T) {
	m := NewResultsModel(comparison(), NewTheme(false))
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = sized

	view := m.View()
	for _, want := range []string{"download", "ISP", "WARP", "sg-linode", "better"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
	// A server measured in only one phase is reported, not drawn as a bar.
	if !strings.Contains(view, "not measured in both phases") {
		t.Errorf("view should name the one-sided server:\n%s", view)
	}
}

// The colour must follow the metric's direction, or green would mean "the
// number went up" rather than "this got better".
func TestResultsVerdictFollowsDirection(t *testing.T) {
	m := NewResultsModel(comparison(), NewTheme(false))

	spec := metricPages()[0] // download, higher-is-better
	rows, _ := m.rowsFor(spec)

	if len(rows) == 0 {
		t.Fatal("no rows for download")
	}
	if !strings.Contains(rows[0].Note, "better") {
		t.Errorf("40 -> 120 Mbps should read as better: %q", rows[0].Note)
	}
	// The 0.17% change is noise, and must not be sold as an improvement.
	if !strings.Contains(rows[1].Note, "same") {
		t.Errorf("a 0.17%% change should read as same: %q", rows[1].Note)
	}

	latencySpec := metricPages()[2]
	latencyRows, _ := m.rowsFor(latencySpec)
	if len(latencyRows) == 0 {
		t.Fatal("no rows for latency")
	}
	if !strings.Contains(latencyRows[0].Note, "better") {
		t.Errorf("60 -> 50 ms latency should read as better: %q", latencyRows[0].Note)
	}
}

func TestResultsUsesASCIIWithoutColour(t *testing.T) {
	plain := NewResultsModel(comparison(), NewTheme(false))
	plain, _ = plain.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	if !strings.Contains(plain.View(), "#") {
		t.Error("a colourless run should draw ASCII bars")
	}
	if strings.Contains(plain.View(), "█") {
		t.Error("a colourless run should not use block characters")
	}
}

func TestResultsViewFitsTheWidth(t *testing.T) {
	for _, width := range []int{30, 40, 60, 80, 100} {
		m := NewResultsModel(comparison(), NewTheme(false))
		sized, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		m = sized

		for _, line := range strings.Split(m.View(), "\n") {
			if got := len([]rune(line)); got > width {
				t.Errorf("width %d: line is %d columns: %q", width, got, line)
			}
		}
	}
}

func TestResultsHandlesNoComparison(t *testing.T) {
	m := NewResultsModel(nil, NewTheme(false))
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	if !strings.Contains(m.View(), "no comparison") {
		t.Errorf("view = %q", m.View())
	}
}

func TestResultsFinishAndQuit(t *testing.T) {
	m := NewResultsModel(comparison(), NewTheme(false))

	m, _ = m.Update(key("enter"))
	if !m.Done {
		t.Error("enter should finish")
	}
	m, _ = m.Update(key("q"))
	if !m.Quit {
		t.Error("q should quit")
	}
}

// A metric nobody measured must read as absent, not as a chart of zeroes.
func TestResultsReportsMetricsWithNothingToCompare(t *testing.T) {
	cmp := comparison()
	for i := range cmp.Servers {
		cmp.Servers[i].Upload = nil
	}

	m := NewResultsModel(cmp, NewTheme(false))
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m = sized
	m, _ = m.Update(key("right")) // the upload page

	if !strings.Contains(m.View(), "no comparable") {
		t.Errorf("view = %q", m.View())
	}
}

func TestResultsWorksBeforeAnyWindowSize(t *testing.T) {
	m := NewResultsModel(comparison(), NewTheme(false))

	if !strings.Contains(m.View(), "warpbench") {
		t.Errorf("View() before a size = %q", m.View())
	}
}
