package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rpratama123/warpbench/internal/results"
	"github.com/rpratama123/warpbench/internal/runner"
	"github.com/rpratama123/warpbench/internal/serverlist"
	"github.com/rpratama123/warpbench/internal/trace"
)

// screen is which part of the flow is showing.
type screen int

const (
	screenSetup screen = iota
	screenBaseline
	screenPause
	screenWarp
	screenResults
	screenSave
	screenDone
)

// RunPhase measures one phase. Injecting it keeps the app testable without a
// network and, more importantly, guarantees the TUI uses the same runner the
// non-interactive path does.
type RunPhase func(ctx context.Context, phase string, servers []serverlist.Server, budget runner.Budget, progress runner.Progress) (*results.File, error)

// TracePoll builds the message the pause screen expects from a WARP-state read.
//
// It is exported so the program can supply the check without depending on
// unexported message types, which would make the screen impossible to drive
// from outside this package.
func TracePoll(result trace.Result, err error) tea.Msg {
	return tracePollMsg{result: result, err: err}
}

// AppConfig is everything the app needs from its caller.
type AppConfig struct {
	List  *serverlist.List
	Mode  runner.Mode
	Theme Theme

	// Run measures a phase.
	Run RunPhase
	// CheckTrace returns a command that polls the WARP state for the pause
	// screen.
	CheckTrace func() tea.Cmd
	// Save persists both phases and returns the paths written.
	Save func(baseline, warp *results.File) ([]string, error)
	// Groups limits the flow to the named groups, mirroring --groups.
	Groups []string
	// CompareOptions configures the comparison, chiefly the revision override.
	CompareOptions results.CompareOptions
	// AutoConfirm skips the save prompt, for --yes.
	AutoConfirm bool
}

// AppModel drives the whole flow: choose, measure, pause, measure, compare,
// save.
type AppModel struct {
	cfg   AppConfig
	state screen

	setup    SetupModel
	progress ProgressModel
	pause    PauseModel
	results  ResultsModel

	baseline *results.File
	warp     *results.File
	saved    []string

	// err is a failure that ends the run, as opposed to a per-server warning.
	err error

	bridge *ProgressBridge
	ctx    context.Context
	cancel context.CancelFunc

	width, height int
}

// NewApp builds the interactive program.
func NewApp(cfg AppConfig) AppModel {
	ctx, cancel := context.WithCancel(context.Background())

	m := AppModel{
		cfg:    cfg,
		state:  screenSetup,
		setup:  NewSetup(cfg.List, cfg.Mode, cfg.Theme, cfg.Groups),
		ctx:    ctx,
		cancel: cancel,
	}
	return m
}

// State reports the active screen, for tests.
func (m AppModel) State() screen { return m.state }

// SavedPaths returns the files written, once the run has finished.
func (m AppModel) SavedPaths() []string { return m.saved }

// Err returns the failure that ended the run, if any.
func (m AppModel) Err() error { return m.err }

// Init satisfies tea.Model.
func (m AppModel) Init() tea.Cmd { return nil }

// Cleanup cancels any in-flight measurement. The caller must call it once the
// program exits, or a background run would outlive the UI.
func (m AppModel) Cleanup() { m.cancel() }

// Update drives the state machine.
func (m AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "ctrl+c" {
		m.cancel()
		return m, tea.Quit
	}

	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = size.Width, size.Height
	}

	switch m.state {
	case screenSetup:
		return m.updateSetup(msg)
	case screenBaseline, screenWarp:
		return m.updateProgress(msg)
	case screenPause:
		return m.updatePause(msg)
	case screenResults:
		return m.updateResults(msg)
	case screenSave:
		return m.updateSave(msg)
	}
	return m, nil
}

func (m AppModel) updateSetup(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.setup, _ = m.setup.Update(msg)

	switch {
	case m.setup.Quit:
		m.cancel()
		return m, tea.Quit
	case m.setup.Done:
		m.state = screenBaseline
		return m, m.startPhase("baseline")
	}
	return m, nil
}

// startPhase launches a measurement and begins draining its event channel.
func (m *AppModel) startPhase(phase string) tea.Cmd {
	servers := m.setup.Selection()
	budget := m.setup.Budget()

	progress := NewProgressModel(m.cfg.Theme)
	progress, _ = progress.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	m.progress = progress

	bridge := NewProgressBridge()
	m.bridge = bridge

	ctx := m.ctx
	run := m.cfg.Run

	// The measurement runs in the background and reports through the bridge, so
	// the UI keeps repainting while it works and every message arrives on one
	// ordered channel.
	go func() {
		file, err := run(ctx, phase, servers, budget, bridge)
		bridge.Send(phaseDoneMsg{file: file, err: err})
	}()

	return WaitForEvent(bridge.Messages())
}

func (m AppModel) updateProgress(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.progress, _ = m.progress.Update(msg)

	switch typed := msg.(type) {
	case phaseStartedMsg, serverStartedMsg, stepMsg, traceMsg, serverDoneMsg:
		// Re-arm the read, or the bridge fills up and the run blocks.
		return m, WaitForEvent(m.bridge.Messages())

	case phaseDoneMsg:
		if typed.err != nil {
			m.err = typed.err
			m.state = screenDone
			return m, tea.Quit
		}

		switch m.state {
		case screenBaseline:
			m.baseline = typed.file
			m.state = screenPause
			m.pause = NewPauseModel(m.cfg.Theme, "warp", m.cfg.CheckTrace)
			return m, m.pause.Init()

		case screenWarp:
			m.warp = typed.file
			return m, m.finishComparison()
		}
	}
	return m, nil
}

// finishComparison builds the results screen, or reports why it cannot.
func (m *AppModel) finishComparison() tea.Cmd {
	cmp, err := results.Compare(m.baseline, m.warp, m.cfg.CompareOptions)
	if err != nil {
		m.err = err
		m.state = screenDone
		return tea.Quit
	}

	m.results = NewResultsModel(cmp, m.cfg.Theme)
	m.results, _ = m.results.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	m.state = screenResults

	if m.cfg.AutoConfirm {
		// --yes means the user has already agreed to save; asking again would
		// be a prompt they cannot answer in a script.
		return m.saveNow()
	}
	return nil
}

func (m AppModel) updatePause(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.pause, cmd = m.pause.Update(msg)

	switch {
	case m.pause.Aborted:
		m.cancel()
		return m, tea.Quit
	case m.pause.Confirmed:
		m.state = screenWarp
		return m, tea.Batch(cmd, m.startPhase("warp"))
	}
	return m, cmd
}

func (m AppModel) updateResults(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.results, _ = m.results.Update(msg)

	switch {
	case m.results.Quit:
		m.cancel()
		return m, tea.Quit
	case m.results.Done:
		m.state = screenSave
	}
	return m, nil
}

func (m AppModel) updateSave(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch km.String() {
	case "y", "enter":
		return m, m.saveNow()
	case "n", "q", "esc":
		m.state = screenDone
		return m, tea.Quit
	}
	return m, nil
}

// saveNow writes both phases and ends the run.
func (m *AppModel) saveNow() tea.Cmd {
	if m.cfg.Save == nil {
		m.state = screenDone
		return tea.Quit
	}

	paths, err := m.cfg.Save(m.baseline, m.warp)
	if err != nil {
		m.err = err
	}
	m.saved = paths
	m.state = screenDone
	return tea.Quit
}

// View renders the active screen.
func (m AppModel) View() string {
	width := m.width
	if width <= 0 {
		width = 80
	}

	switch m.state {
	case screenSetup:
		return m.setup.View()
	case screenBaseline, screenWarp:
		return m.progress.View()
	case screenPause:
		return m.pause.View()
	case screenResults:
		return m.results.View()
	case screenSave:
		return m.viewSave(width)
	case screenDone:
		return m.viewDone(width)
	}
	return ""
}

func (m AppModel) viewSave(width int) string {
	var b strings.Builder
	b.WriteString(fitLine(m.theme().Header("warpbench")+"  "+m.theme().Dim("save"), width) + "\n\n")
	b.WriteString(fitLine("Save both phases as JSON? [Y/n]", width) + "\n\n")
	b.WriteString(fitLine(m.theme().Dim(defaultPaths(time.Now())), width) + "\n\n")
	b.WriteString(fitLine(m.theme().Dim("y save  n discard  q quit"), width))
	return b.String()
}

func (m AppModel) viewDone(width int) string {
	var b strings.Builder
	b.WriteString(fitLine(m.theme().Header("warpbench")+"  "+m.theme().Dim("finished"), width) + "\n\n")

	switch {
	case m.err != nil:
		appendWrapped(&b, m.theme().Worse("failed: "+m.err.Error()), width)
	case len(m.saved) > 0:
		b.WriteString(fitLine("wrote:", width) + "\n")
		for _, p := range m.saved {
			b.WriteString(fitLine("  "+p, width) + "\n")
		}
	default:
		b.WriteString(fitLine("nothing was saved", width) + "\n")
	}
	return b.String()
}

func (m AppModel) theme() Theme { return m.cfg.Theme }

// defaultPaths names the files a save would write, so the prompt can show them
// rather than asking blind.
func defaultPaths(at time.Time) string {
	stamp := at.Format("20060102-1504")
	return fmt.Sprintf("./warpbench-%s-baseline.json and ./warpbench-%s-warp.json", stamp, stamp)
}
