package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rpratama123/warpbench/internal/results"
	"github.com/rpratama123/warpbench/internal/runner"
	"github.com/rpratama123/warpbench/internal/serverlist"
)

// fakePhase builds a result file the comparison can accept.
func fakePhase(phase string, mbps float64) *results.File {
	at := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)
	if phase == "warp" {
		at = at.Add(10 * time.Minute)
	}

	return &results.File{
		Schema:      results.SchemaVersion,
		Tool:        results.Tool{Version: "test"},
		Phase:       phase,
		StartedAt:   at,
		EndedAt:     at.Add(time.Minute),
		DurationSec: 60,
		Configuration: results.Configuration{
			Mode: "quick", ServerIDs: []string{"sg-1"}, Parallel: 1,
		},
		ServerList:  results.ServerListRef{Schema: 2, Revision: "2026-09-20", Source: "remote"},
		Environment: results.Environment{OS: "linux", Arch: "amd64"},
		Traces:      []results.Trace{{Stage: "before-" + phase, Warp: warpState(phase)}},
		Servers: []results.Server{{
			ID: "sg-1", Name: "sg-1", Group: "sg", Protocol: "http-file",
			Ping: &results.Ping{Method: "icmp", Sent: 10, Received: 10, AvgMs: 42},
			Download: &results.Series{Metric: "download", Parallel: 1, MedianSteadyMbps: mbps,
				Samples: []results.Sample{{Bytes: 1, ElapsedMs: 1000, SteadyMbps: mbps}}},
			Warnings: []string{},
		}},
		Warnings: []string{},
	}
}

func warpState(phase string) string {
	if phase == "warp" {
		return "on"
	}
	return "off"
}

// appFixture wires an app whose measurement is entirely synthetic.
type appFixture struct {
	app      AppModel
	ran      []string
	saved    []*results.File
	saveErr  error
	runErr   error
	checkCmd func() tea.Cmd
}

func newAppFixture(t *testing.T, opts ...func(*AppConfig)) *appFixture {
	t.Helper()
	f := &appFixture{checkCmd: cannedCheck(warpOn(), nil)}

	cfg := AppConfig{
		List:  testList(),
		Mode:  runner.ModeQuick,
		Theme: NewTheme(false),
		Run: func(ctx context.Context, phase string, servers []serverlist.Server, budget runner.Budget, progress runner.Progress) (*results.File, error) {
			f.ran = append(f.ran, phase)
			if f.runErr != nil {
				return nil, f.runErr
			}
			// Exercise the bridge the way the real runner does.
			progress.PhaseStarted(phase, servers, time.Minute)
			progress.ServerStarted(1, len(servers), servers[0])
			progress.Step(servers[0], "download")
			progress.ServerDone(servers[0], results.Server{ID: servers[0].ID})
			return fakePhase(phase, 100), nil
		},
		CheckTrace: f.checkCmd,
		Save: func(baseline, warp *results.File) ([]string, error) {
			if f.saveErr != nil {
				return nil, f.saveErr
			}
			f.saved = append(f.saved, baseline, warp)
			return []string{"baseline.json", "warp.json"}, nil
		},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	f.app = NewApp(cfg)
	return f
}

// send drives one message and returns the updated model.
func (f *appFixture) send(t *testing.T, msg tea.Msg) (AppModel, tea.Cmd) {
	t.Helper()
	updated, cmd := f.app.Update(msg)
	next, ok := updated.(AppModel)
	if !ok {
		t.Fatalf("Update returned %T, want AppModel", updated)
	}
	f.app = next
	return next, cmd
}

// runCmd executes a command and returns its message, failing if it produces
// none.
func runCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command")
	}
	return cmd()
}

// drain feeds every message the running phase produces into the update loop,
// the way the bubbletea runtime would, until the phase reports completion.
func (f *appFixture) drain(t *testing.T) {
	t.Helper()

	if f.app.bridge == nil {
		t.Fatal("no phase is running, so there is nothing to drain")
	}

	timeout := time.After(5 * time.Second)
	for {
		select {
		case msg := <-f.app.bridge.Messages():
			f.send(t, msg)
			if _, done := msg.(phaseDoneMsg); done {
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for the phase to finish")
		}
	}
}

// advanceToPause walks the app from setup to the pause screen.
func (f *appFixture) advanceToPause(t *testing.T) {
	t.Helper()
	f.send(t, key("enter")) // mode -> selection
	f.send(t, key("enter")) // selection -> baseline

	if f.app.State() != screenBaseline {
		t.Fatalf("state = %v, want the baseline phase", f.app.State())
	}

	f.drain(t)
	if f.app.State() != screenPause {
		t.Fatalf("state = %v, want the pause screen", f.app.State())
	}
}

func TestAppStartsOnSetup(t *testing.T) {
	f := newAppFixture(t)

	if f.app.State() != screenSetup {
		t.Errorf("State() = %v, want setup", f.app.State())
	}
	if f.app.Init() != nil {
		t.Error("Init should not schedule work before the user has chosen")
	}
	if !strings.Contains(f.app.View(), "warpbench") {
		t.Errorf("View() = %q", f.app.View())
	}
}

func TestAppMeasuresBaselineThenPauses(t *testing.T) {
	f := newAppFixture(t)
	f.advanceToPause(t)

	if len(f.ran) != 1 || f.ran[0] != "baseline" {
		t.Errorf("ran = %v, want a single baseline phase", f.ran)
	}
	if f.app.baseline == nil {
		t.Error("the baseline file was not kept")
	}
}

// The pause screen exists to check the state; the app must not launch the WARP
// phase until it has been confirmed.
func TestAppWaitsForConfirmationBeforeTheWarpPhase(t *testing.T) {
	f := newAppFixture(t)
	f.advanceToPause(t)

	// Polling reports WARP on, but the user has not confirmed yet.
	f.send(t, beginCheckMsg{})
	f.send(t, tracePollMsg{result: warpOn()})

	if len(f.ran) != 1 {
		t.Fatalf("ran = %v, want the WARP phase withheld until confirmation", f.ran)
	}

	f.send(t, key("enter"))
	if f.app.State() != screenWarp {
		t.Fatalf("state = %v, want the warp phase", f.app.State())
	}
	f.drain(t)

	if len(f.ran) != 2 || f.ran[1] != "warp" {
		t.Errorf("ran = %v, want the warp phase after confirmation", f.ran)
	}
}

func TestAppReachesResultsAndSaves(t *testing.T) {
	f := newAppFixture(t)
	f.advanceToPause(t)
	f.send(t, beginCheckMsg{})
	f.send(t, tracePollMsg{result: warpOn()})
	f.send(t, key("enter"))

	// Finish the WARP phase.
	f.drain(t)
	if f.app.State() != screenResults {
		t.Fatalf("state = %v, want results", f.app.State())
	}
	// The headline must compare the two phases, not just print one.
	if view := f.app.View(); !strings.Contains(view, "download") {
		t.Errorf("results view = %q", view)
	}

	f.send(t, key("enter")) // results -> save prompt
	if f.app.State() != screenSave {
		t.Fatalf("state = %v, want the save prompt", f.app.State())
	}
	if !strings.Contains(f.app.View(), "Save both phases") {
		t.Errorf("save view = %q", f.app.View())
	}

	f.send(t, key("y"))
	if f.app.State() != screenDone {
		t.Errorf("state = %v, want done", f.app.State())
	}
	if len(f.saved) != 2 {
		t.Errorf("saved %d files, want both phases", len(f.saved))
	}
	if got := f.app.SavedPaths(); len(got) != 2 {
		t.Errorf("SavedPaths() = %v", got)
	}
	if !strings.Contains(f.app.View(), "baseline.json") {
		t.Errorf("the saved paths should be shown: %q", f.app.View())
	}
}

func TestAppDiscardsOnRequest(t *testing.T) {
	f := newAppFixture(t)
	f.advanceToPause(t)
	f.send(t, beginCheckMsg{})
	f.send(t, tracePollMsg{result: warpOn()})
	f.send(t, key("enter"))
	f.drain(t)
	f.send(t, key("enter")) // to the save prompt
	f.send(t, key("n"))

	if len(f.saved) != 0 {
		t.Errorf("saved %d files after declining", len(f.saved))
	}
	if !strings.Contains(f.app.View(), "nothing was saved") {
		t.Errorf("view = %q", f.app.View())
	}
}

// --yes means the user already agreed; asking again would be a prompt a script
// cannot answer.
func TestAppAutoConfirmSkipsThePrompt(t *testing.T) {
	f := newAppFixture(t, func(cfg *AppConfig) { cfg.AutoConfirm = true })
	f.advanceToPause(t)
	f.send(t, beginCheckMsg{})
	f.send(t, tracePollMsg{result: warpOn()})
	f.send(t, key("enter"))
	f.drain(t)

	if f.app.State() == screenSave {
		t.Fatal("the save prompt appeared despite AutoConfirm")
	}
	if len(f.saved) != 2 {
		t.Errorf("saved %d files, want both phases", len(f.saved))
	}
}

func TestAppReportsAPhaseFailure(t *testing.T) {
	f := newAppFixture(t, func(cfg *AppConfig) {
		cfg.Run = func(context.Context, string, []serverlist.Server, runner.Budget, runner.Progress) (*results.File, error) {
			return nil, errors.New("host unreachable")
		}
	})

	f.send(t, key("enter"))
	f.send(t, key("enter"))
	f.drain(t)

	if f.app.State() != screenDone {
		t.Fatalf("state = %v, want done", f.app.State())
	}
	if f.app.Err() == nil {
		t.Error("the failure was not recorded")
	}
	if !strings.Contains(f.app.View(), "host unreachable") {
		t.Errorf("view = %q", f.app.View())
	}
}

// Comparing two files from the same phase cannot mean anything, and the app
// must say so rather than showing an empty table.
func TestAppReportsAnUncomparablePair(t *testing.T) {
	f := newAppFixture(t, func(cfg *AppConfig) {
		// A runner that mislabels the phase, as a wiring bug would.
		cfg.Run = func(ctx context.Context, phase string, servers []serverlist.Server, budget runner.Budget, progress runner.Progress) (*results.File, error) {
			return fakePhase("baseline", 120), nil
		}
	})
	f.advanceToPause(t)
	f.send(t, beginCheckMsg{})
	f.send(t, tracePollMsg{result: warpOn()})
	f.send(t, key("enter"))
	f.drain(t)

	if f.app.State() != screenDone {
		t.Fatalf("state = %v, want done", f.app.State())
	}
	if f.app.Err() == nil || !strings.Contains(f.app.Err().Error(), "phase") {
		t.Errorf("Err() = %v, want an explanation about the phases", f.app.Err())
	}
}

func TestAppReportsASaveFailure(t *testing.T) {
	f := newAppFixture(t, func(cfg *AppConfig) {
		cfg.Save = func(*results.File, *results.File) ([]string, error) {
			return nil, errors.New("disk full")
		}
	})
	f.advanceToPause(t)
	f.send(t, beginCheckMsg{})
	f.send(t, tracePollMsg{result: warpOn()})
	f.send(t, key("enter"))
	f.drain(t)
	f.send(t, key("enter"))
	f.send(t, key("y"))

	if f.app.Err() == nil || !strings.Contains(f.app.Err().Error(), "disk full") {
		t.Errorf("Err() = %v", f.app.Err())
	}
}

func TestAppQuitsOnCtrlC(t *testing.T) {
	f := newAppFixture(t)

	_, cmd := f.send(t, tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c should return a quit command")
	}
	if msg := runCmd(t, cmd); msg == nil {
		t.Error("the quit command produced no message")
	}
}

func TestAppQuitsFromSetup(t *testing.T) {
	f := newAppFixture(t)
	_, cmd := f.send(t, key("q"))

	if cmd == nil {
		t.Fatal("q should return a quit command")
	}
}

// The bridge must keep being drained while a phase runs, or the measurement
// goroutine blocks once the buffer fills.
func TestAppKeepsDrainingTheBridge(t *testing.T) {
	f := newAppFixture(t)
	f.send(t, key("enter"))
	f.send(t, key("enter"))

	_, cmd := f.send(t, serverStartedMsg{index: 1, total: 1, server: srv("sg-1", "sg", "quick", "ping")})
	if cmd == nil {
		t.Fatal("a bridge event must re-arm the read")
	}
}

func TestAppTracksWindowSize(t *testing.T) {
	f := newAppFixture(t)
	f.send(t, tea.WindowSizeMsg{Width: 60, Height: 20})

	if f.app.width != 60 || f.app.height != 20 {
		t.Errorf("size = %dx%d, want 60x20", f.app.width, f.app.height)
	}

	// The whole flow must stay within a 60-column terminal.
	f.send(t, key("enter"))
	for _, line := range strings.Split(f.app.View(), "\n") {
		if len([]rune(line)) > 60 {
			t.Errorf("line exceeds 60 columns: %q", line)
		}
	}
}

func TestAppViewAtSixtyColumnsAcrossEveryScreen(t *testing.T) {
	f := newAppFixture(t)
	f.send(t, tea.WindowSizeMsg{Width: 60, Height: 20})

	check := func(name string) {
		t.Helper()
		for _, line := range strings.Split(f.app.View(), "\n") {
			if len([]rune(line)) > 60 {
				t.Errorf("%s: line is %d columns: %q", name, len([]rune(line)), line)
			}
		}
	}

	check("setup-mode")
	f.send(t, key("enter"))
	check("setup-servers")

	f.send(t, key("enter")) // start the baseline phase
	f.drain(t)
	check("pause")

	f.send(t, beginCheckMsg{})
	f.send(t, tracePollMsg{result: warpOn()})
	check("pause-confirmed")

	f.send(t, key("enter"))
	f.drain(t)
	check("results")

	f.send(t, key("enter"))
	check("save")
}

func TestDefaultPaths(t *testing.T) {
	got := defaultPaths(time.Date(2026, 9, 20, 14, 5, 0, 0, time.UTC))

	for _, want := range []string{"20260920-1405", "baseline.json", "warp.json"} {
		if !strings.Contains(got, want) {
			t.Errorf("defaultPaths() = %q, want %q", got, want)
		}
	}
}

func TestAppCleanupIsSafe(t *testing.T) {
	f := newAppFixture(t)
	f.app.Cleanup()
	f.app.Cleanup() // must be idempotent
}

func TestRunPhaseAsyncReportsTheOutcome(t *testing.T) {
	bridge := NewProgressBridge()
	cmd := RunPhaseAsync(context.Background(), func(context.Context, runner.Progress) (*results.File, error) {
		return fakePhase("warp", 1), nil
	}, bridge)

	msg, ok := runCmd(t, cmd).(phaseDoneMsg)
	if !ok {
		t.Fatal("RunPhaseAsync did not produce a phaseDoneMsg")
	}
	if msg.file == nil || msg.file.Phase != "warp" {
		t.Errorf("msg.file = %+v", msg.file)
	}
}
