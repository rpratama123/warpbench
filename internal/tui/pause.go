package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rpratama123/warpbench/internal/trace"
)

// pollInterval is how long the pause screen waits between WARP-state checks.
const pollInterval = 3 * time.Second

// maxTraceChecks bounds how long the pause screen keeps polling before it stops
// and makes the user act. The endpoint is cheap, but a screen that polls
// forever is a screen that never tells the user anything is wrong.
const maxTraceChecks = 5

// PauseModel waits for WARP to be switched on, and confirms it from the network
// rather than taking the user's word for it.
//
// This is the step that makes the whole comparison meaningful: a "WARP" phase
// measured with WARP still off looks perfectly fine and means nothing, so the
// state is read from Cloudflare and the user is shown what was actually seen.
type PauseModel struct {
	phase string

	result   trace.Result
	attempts int
	checking bool
	message  string

	// Force records an explicit override, which the result file reports.
	Force     bool
	Confirmed bool
	Aborted   bool

	width, height int
	theme         Theme

	// check returns a command that polls the trace endpoint once. It is
	// injected so the screen can be tested without a network.
	check func() tea.Cmd
}

// NewPauseModel builds the pause screen. check must return a command producing
// a tracePollMsg.
func NewPauseModel(theme Theme, phase string, check func() tea.Cmd) PauseModel {
	return PauseModel{theme: theme, phase: phase, check: check}
}

// beginCheckMsg asks the model to start a poll.
//
// Init has a value receiver, so it cannot record that a check is outstanding;
// routing through Update is what keeps the attempt count and the checking flag
// visible to the caller.
type beginCheckMsg struct{}

// Init starts the first check immediately, so the user sees the current state
// rather than having to ask for it.
func (m PauseModel) Init() tea.Cmd {
	return func() tea.Msg { return beginCheckMsg{} }
}

// startCheck records the attempt and returns the polling command.
func (m *PauseModel) startCheck() tea.Cmd {
	if m.check == nil {
		return nil
	}
	m.checking = true
	m.attempts++
	return m.check()
}

// Update handles polling results and keystrokes.
func (m PauseModel) Update(msg tea.Msg) (PauseModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case tracePollMsg:
		m.checking = false
		if msg.err != nil {
			m.result = trace.Result{Stage: "warp-check", Warp: trace.StateUnknown, Err: msg.err.Error()}
		} else {
			m.result = msg.result
		}

		// Keep polling while WARP is not on and the budget allows, whether the
		// last attempt succeeded or failed.
		//
		// Scheduling the retry only on success was a bug with the worst
		// possible timing: the moment WARP is switched on is when the resolver
		// is being reconfigured and a check is most likely to fail, and one
		// failure ended automatic retrying for the rest of the run.
		if !m.result.WarpEnabled() && m.attempts < maxTraceChecks {
			return m, tea.Tick(pollInterval, func(time.Time) tea.Msg { return pollTickMsg{} })
		}

	case beginCheckMsg:
		return m, m.startCheck()

	case pollTickMsg:
		return m, m.startCheck()

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.Aborted = true

		case "r":
			m.message = ""
			m.attempts = 0
			return m, m.startCheck()

		case "f":
			m.Force = true
			m.message = "overriding the state check; the result file will record it"

		case "enter":
			if m.result.WarpEnabled() || m.Force {
				m.Confirmed = true
				return m, nil
			}
			m.message = "WARP is not on yet"
		}
	}
	return m, nil
}

// pollTickMsg drives the retry timer.
type pollTickMsg struct{}

// View renders the pause screen.
func (m PauseModel) View() string {
	width := m.viewWidth()
	var b strings.Builder

	b.WriteString(fitLine(m.theme.Header("warpbench")+"  "+m.theme.Dim("switch to WARP"), width) + "\n\n")
	b.WriteString(fitLine("Turn on Cloudflare WARP now, then press enter.", width) + "\n\n")

	// The observed state is the headline, not a footnote: it is the thing the
	// user needs to trust.
	// The last known state stays on screen, with a progress line while another
	// check is in flight. Showing the stale result alone made a ten-second
	// timeout look like a frozen program.
	if m.checking {
		b.WriteString(fitLine(m.theme.Dim("checking the WARP state..."), width) + "\n")
	}
	if m.result.Warp != "" {
		appendWrapped(&b, "state: "+m.theme.Header(m.result.Describe()), width)
		if m.result.WarpEnabled() {
			b.WriteString(fitLine(m.theme.Better("WARP is on. Press enter to measure again."), width) + "\n")
		}
	}

	if explain := m.result.Explain(); explain != "" && !m.result.WarpEnabled() {
		// Guidance is wrapped, never truncated: cutting the explanation for a
		// stuck WARP pause removes exactly the part that helps.
		b.WriteString("\n")
		appendWrapped(&b, m.theme.Warn(explain), width)
	}

	if m.Force {
		b.WriteString("\n" + fitLine(m.theme.Warn("override active: this phase will be measured with the state above"), width) + "\n")
	}
	if m.message != "" {
		b.WriteString("\n" + fitLine(m.theme.Warn(m.message), width) + "\n")
	}

	attempts := m.attempts
	if attempts > maxTraceChecks {
		attempts = maxTraceChecks
	}
	fmt.Fprintf(&b, "\n%s\n", fitLine(m.theme.Dim(fmt.Sprintf("checked %d/%d times", attempts, maxTraceChecks)), width))

	if !m.result.WarpEnabled() && !m.Force && m.attempts >= maxTraceChecks {
		b.WriteString(fitLine(m.theme.Warn("stopped checking. press r to keep trying, or f to measure anyway."), width) + "\n")
	}
	b.WriteString(fitLine(m.theme.Dim("enter continue  r re-check  f override  q quit"), width))

	return b.String()
}

func (m PauseModel) viewWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}
