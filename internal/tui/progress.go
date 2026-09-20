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

// --- messages --------------------------------------------------------------

// These carry the measurement goroutine's events into the update loop. They are
// the only channel between the runner and the UI, so the two never share state.
type (
	phaseStartedMsg struct {
		phase    string
		servers  []serverlist.Server
		estimate time.Duration
	}
	serverStartedMsg struct {
		index, total int
		server       serverlist.Server
	}
	stepMsg struct {
		server serverlist.Server
		step   string
	}
	traceMsg      struct{ result trace.Result }
	serverDoneMsg struct {
		server   serverlist.Server
		measured results.Server
	}
	phaseDoneMsg struct {
		file *results.File
		err  error
	}
	tracePollMsg struct {
		result trace.Result
		err    error
	}
)

// --- bridge ----------------------------------------------------------------

// bridgeDepth is how many events may be queued before the measurement goroutine
// blocks. It is generous because the events are tiny and the UI drains them
// continuously; a send that blocked forever would hang a run.
const bridgeDepth = 256

// ProgressBridge adapts runner.Progress to the update loop.
type ProgressBridge struct {
	ch chan tea.Msg
}

// NewProgressBridge returns a bridge and the channel its messages arrive on.
func NewProgressBridge() *ProgressBridge {
	return &ProgressBridge{ch: make(chan tea.Msg, bridgeDepth)}
}

// Messages is the channel the UI reads from.
func (b *ProgressBridge) Messages() <-chan tea.Msg { return b.ch }

// Send posts an arbitrary message, so completion travels the same ordered
// channel as the progress events. Two channels would let the completion arrive
// before the last few events and leave the table looking unfinished.
func (b *ProgressBridge) Send(msg tea.Msg) { b.ch <- msg }

func (b *ProgressBridge) PhaseStarted(phase string, servers []serverlist.Server, estimate time.Duration) {
	b.ch <- phaseStartedMsg{phase: phase, servers: servers, estimate: estimate}
}

func (b *ProgressBridge) ServerStarted(index, total int, s serverlist.Server) {
	b.ch <- serverStartedMsg{index: index, total: total, server: s}
}

func (b *ProgressBridge) Step(s serverlist.Server, step string) {
	b.ch <- stepMsg{server: s, step: step}
}

func (b *ProgressBridge) Trace(r trace.Result) {
	b.ch <- traceMsg{result: r}
}

func (b *ProgressBridge) ServerDone(s serverlist.Server, measured results.Server) {
	b.ch <- serverDoneMsg{server: s, measured: measured}
}

// WaitForEvent yields the next bridge message. The UI re-issues it after every
// message, which is what keeps the channel drained.
func WaitForEvent(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

// --- progress model --------------------------------------------------------

// serverProgress is the live state of one target.
type serverProgress struct {
	server   serverlist.Server
	measured results.Server
	step     string
	done     bool
}

// ProgressModel shows a phase as it runs.
type ProgressModel struct {
	phase     string
	servers   []serverProgress
	total     int
	current   int
	estimate  time.Duration
	action    string
	startedAt time.Time
	finished  bool
	err       error

	width, height int
	theme         Theme
	// now is injectable so the ETA can be tested without waiting.
	now func() time.Time
}

// NewProgressModel builds the progress screen.
func NewProgressModel(theme Theme) ProgressModel {
	return ProgressModel{theme: theme, now: time.Now}
}

func (m ProgressModel) Init() tea.Cmd { return nil }

func (m ProgressModel) Update(msg tea.Msg) (ProgressModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case phaseStartedMsg:
		m.phase = msg.phase
		m.total = len(msg.servers)
		m.estimate = msg.estimate
		m.startedAt = m.now()
		m.servers = make([]serverProgress, 0, len(msg.servers))
		for _, s := range msg.servers {
			m.servers = append(m.servers, serverProgress{server: s})
		}

	case serverStartedMsg:
		m.current = msg.index
		m.markServer(msg.server, func(sp *serverProgress) { sp.step = "starting" })

	case stepMsg:
		m.markServer(msg.server, func(sp *serverProgress) { sp.step = msg.step })
		m.action = fmt.Sprintf("%s: %s", msg.server.ID, msg.step)

	case traceMsg:
		m.action = "trace " + msg.result.Stage + ": " + msg.result.Describe()

	case serverDoneMsg:
		m.markServer(msg.server, func(sp *serverProgress) {
			sp.measured = msg.measured
			sp.done = true
			sp.step = ""
		})
		// The action line describes work in progress, so it must not keep
		// claiming a finished server is still busy.
		m.action = ""

	case phaseDoneMsg:
		m.finished = true
		m.err = msg.err
	}
	return m, nil
}

// markServer edits the entry for a server, tolerating one we never saw start.
func (m *ProgressModel) markServer(s serverlist.Server, edit func(*serverProgress)) {
	for i := range m.servers {
		if m.servers[i].server.ID == s.ID {
			edit(&m.servers[i])
			return
		}
	}
	m.servers = append(m.servers, serverProgress{server: s})
	edit(&m.servers[len(m.servers)-1])
}

// completed counts finished servers.
func (m ProgressModel) completed() int {
	n := 0
	for _, s := range m.servers {
		if s.done {
			n++
		}
	}
	return n
}

// eta estimates the remaining time from what has actually completed, which is
// far more honest than the pre-run estimate once the run is under way.
func (m ProgressModel) eta() time.Duration {
	done := m.completed()
	if done == 0 || m.total == 0 || m.startedAt.IsZero() {
		return m.estimate
	}
	elapsed := m.now().Sub(m.startedAt)
	if elapsed <= 0 {
		return m.estimate
	}
	perServer := elapsed / time.Duration(done)
	remaining := time.Duration(m.total-done) * perServer
	// The first server carries connection setup and TLS handshakes that later
	// ones reuse, so the naive projection can overshoot badly. Clamping to the
	// pre-run estimate keeps the number believable without hiding progress.
	if remaining > m.estimate*2 {
		remaining = m.estimate
	}
	return remaining
}

// View renders the live table.
func (m ProgressModel) View() string {
	width := m.viewWidth()
	var b strings.Builder

	title := fmt.Sprintf("phase %s", m.phase)
	if m.finished {
		title += "  complete"
	}
	b.WriteString(fitLine(m.theme.Header("warpbench")+"  "+m.theme.Dim(title), width) + "\n")

	done := m.completed()
	b.WriteString(fitLine(fmt.Sprintf("%s  %d/%d servers  eta %s",
		progressBar(done, m.total, min(24, max(8, width/4))), done, m.total, formatDuration(m.eta())), width) + "\n\n")

	available := m.height - 8
	if available < 3 {
		available = len(m.servers)
	}
	windowStart, windowEnd := window(len(m.servers), max(0, m.current-1), available)

	for i := windowStart; i < windowEnd; i++ {
		b.WriteString(fitLine(m.renderServer(m.servers[i]), width) + "\n")
	}

	b.WriteString("\n")
	switch {
	case m.err != nil:
		b.WriteString(fitLine(m.theme.Worse("phase failed: "+m.err.Error()), width) + "\n")
	case m.action != "" && !m.finished:
		b.WriteString(fitLine(m.theme.Dim(m.action), width) + "\n")
	case m.finished:
		b.WriteString(fitLine(m.theme.Dim("press enter to continue"), width) + "\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// renderServer draws one live row.
func (m ProgressModel) renderServer(sp serverProgress) string {
	marker := "·"
	switch {
	case sp.done:
		marker = "✓"
	case sp.step != "":
		marker = "▶"
	}

	cells := []string{}
	if sp.measured.Ping != nil {
		cells = append(cells, fmt.Sprintf("ping %.0fms", sp.measured.Ping.AvgMs))
	}
	if sp.measured.Timings != nil {
		cells = append(cells, fmt.Sprintf("ttfb %.0fms", sp.measured.Timings.TTFBMs))
	}
	if sp.measured.Download != nil {
		cells = append(cells, fmt.Sprintf("dl %.1f", sp.measured.Download.MedianSteadyMbps))
	}
	if sp.measured.Upload != nil {
		cells = append(cells, fmt.Sprintf("ul %.1f", sp.measured.Upload.MedianSteadyMbps))
	}
	if len(cells) == 0 && sp.step != "" {
		cells = append(cells, sp.step)
	}
	if len(sp.measured.Warnings) > 0 {
		cells = append(cells, m.theme.Warn(fmt.Sprintf("%d warning(s)", len(sp.measured.Warnings))))
	}

	return fmt.Sprintf("  %s %-22s %s", marker, truncatePlain(sp.server.ID, 22), strings.Join(cells, "  "))
}

func (m ProgressModel) viewWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}

// progressBar renders a filled bar, or plain text when there is no room.
func progressBar(done, total, width int) string {
	if total <= 0 {
		return ""
	}
	if width <= 0 {
		width = 10
	}
	filled := done * width / total
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("#", filled) + strings.Repeat(".", width-filled) + "]"
}

// RunPhaseAsync runs one phase in the background, forwarding its events to the
// UI and reporting the outcome when it finishes.
//
// The runner is unchanged by this: the same Config and the same Progress
// interface drive the interactive and non-interactive paths.
func RunPhaseAsync(ctx context.Context, run func(context.Context, runner.Progress) (*results.File, error), bridge *ProgressBridge) tea.Cmd {
	return func() tea.Msg {
		file, err := run(ctx, bridge)
		return phaseDoneMsg{file: file, err: err}
	}
}
