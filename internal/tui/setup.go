package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rpratama123/warpbench/internal/runner"
	"github.com/rpratama123/warpbench/internal/serverlist"
)

// setupStage is which half of the setup the user is on.
type setupStage int

const (
	// stageMode picks how much to measure.
	stageMode setupStage = iota
	// stageServers picks the individual targets.
	stageServers
)

// rowKind distinguishes a group header from a server line, so one cursor can
// walk a list that is conceptually two-level.
type rowKind int

const (
	rowGroup rowKind = iota
	rowServer
)

type setupRow struct {
	kind    rowKind
	groupID string
	server  serverlist.Server
}

// SetupModel is the mode and server-selection flow.
type SetupModel struct {
	list  *serverlist.List
	mode  runner.Mode
	stage setupStage
	theme Theme

	// groups limits the flow to the named groups, so --groups is honoured
	// interactively instead of being silently ignored.
	groups []string

	selected  map[string]bool
	collapsed map[string]bool
	rows      []setupRow
	cursor    int

	width  int
	height int

	// Done is set when the user confirms the selection.
	Done bool
	// Quit is set when the user aborts.
	Quit bool
}

// NewSetup builds the setup flow, starting on the mode choice.
//
// groups restricts the flow to those groups; an empty slice means every group.
func NewSetup(list *serverlist.List, mode runner.Mode, theme Theme, groups []string) SetupModel {
	m := SetupModel{
		list:      list,
		mode:      mode,
		stage:     stageMode,
		theme:     theme,
		groups:    groups,
		selected:  map[string]bool{},
		collapsed: map[string]bool{},
	}
	m.applyMode()
	m.rebuildRows()
	return m
}

// Stage reports which half of the setup is showing, for tests.
func (m SetupModel) Stage() setupStage { return m.stage }

// applyMode resets the selection to the mode's default set.
func (m *SetupModel) applyMode() {
	m.selected = map[string]bool{}
	for _, s := range runner.Select(m.list, m.mode, m.groups, nil) {
		m.selected[s.ID] = true
	}
}

// rebuildRows flattens the list into cursorable rows.
func (m *SetupModel) rebuildRows() {
	m.rows = nil
	inScope := make(map[string]bool, len(m.groups))
	for _, g := range m.groups {
		inScope[g] = true
	}

	byGroup := map[string][]serverlist.Server{}
	for _, s := range m.list.Servers {
		if len(inScope) > 0 && !inScope[s.Group] {
			continue
		}
		byGroup[s.Group] = append(byGroup[s.Group], s)
	}

	for _, g := range m.list.Groups {
		servers := byGroup[g.ID]
		if len(servers) == 0 {
			continue
		}
		m.rows = append(m.rows, setupRow{kind: rowGroup, groupID: g.ID})
		if m.collapsed[g.ID] {
			continue
		}
		for _, s := range servers {
			m.rows = append(m.rows, setupRow{kind: rowServer, groupID: g.ID, server: s})
		}
	}

	if m.cursor >= len(m.rows) {
		m.cursor = max(0, len(m.rows)-1)
	}
}

// Mode returns the current mode, which becomes custom once the user edits the
// selection by hand.
func (m SetupModel) Mode() runner.Mode { return m.mode }

// Selection returns the chosen servers in canonical order.
func (m SetupModel) Selection() []serverlist.Server {
	var out []serverlist.Server
	for _, s := range m.list.Servers {
		if m.selected[s.ID] {
			out = append(out, s)
		}
	}
	return runner.Order(m.list, out)
}

// Budget is the measurement budget implied by the current mode.
func (m SetupModel) Budget() runner.Budget { return runner.BudgetFor(m.mode) }

// Estimate is the expected duration of one phase over the current selection.
func (m SetupModel) Estimate() time.Duration {
	return runner.Estimate(m.Selection(), m.Budget())
}

// Update handles input and window resizes.
func (m SetupModel) Update(msg tea.Msg) (SetupModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		if m.stage == stageMode {
			return m.updateMode(msg)
		}
		return m.updateServers(msg)
	}
	return m, nil
}

func (m SetupModel) updateMode(msg tea.KeyMsg) (SetupModel, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.Quit = true

	case "up", "k", "left", "h":
		m.setMode(runner.ModeQuick)
	case "down", "j", "right", "l":
		m.setMode(runner.ModeExtended)

	case "enter":
		m.stage = stageServers
		m.cursor = 0
		m.rebuildRows()
	}
	return m, nil
}

func (m *SetupModel) setMode(mode runner.Mode) {
	if m.mode == mode {
		return
	}
	m.mode = mode
	m.applyMode()
	m.rebuildRows()
}

func (m SetupModel) updateServers(msg tea.KeyMsg) (SetupModel, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.Quit = true

	case "backspace":
		// Going back is useful because mode decides the default selection, and
		// a user who picked wrong should not have to restart.
		m.stage = stageMode

	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)

	case "left", "h":
		m.setCollapsed(true)
	case "right", "l":
		m.setCollapsed(false)

	case " ":
		m.toggleCursor()
	case "a":
		m.toggleGroupUnderCursor()
	case "A":
		m.setAll(true)
	case "n":
		m.setAll(false)

	case "enter":
		if len(m.Selection()) == 0 {
			// Refusing here is the point: a run with nothing selected would
			// otherwise start and measure nothing.
			return m, nil
		}
		m.Done = true
	}
	return m, nil
}

func (m *SetupModel) move(delta int) {
	if len(m.rows) == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
}

// setCollapsed collapses or expands the group under the cursor, keeping the
// cursor on that group so the key can be pressed repeatedly without hunting.
func (m *SetupModel) setCollapsed(collapse bool) {
	if len(m.rows) == 0 {
		return
	}
	group := m.rows[m.cursor].groupID

	if m.collapsed[group] == collapse {
		return
	}
	m.collapsed[group] = collapse
	m.rebuildRows()

	for i, row := range m.rows {
		if row.kind == rowGroup && row.groupID == group {
			m.cursor = i
			return
		}
	}
}

// toggleCursor toggles the server under the cursor, or the whole group when the
// cursor is on a group header.
func (m *SetupModel) toggleCursor() {
	if len(m.rows) == 0 {
		return
	}
	row := m.rows[m.cursor]
	if row.kind == rowGroup {
		m.toggleGroupUnderCursor()
		return
	}

	m.selected[row.server.ID] = !m.selected[row.server.ID]
	m.markCustom()
}

// toggleGroupUnderCursor inverts every server in the group the cursor is in.
func (m *SetupModel) toggleGroupUnderCursor() {
	if len(m.rows) == 0 {
		return
	}
	group := m.rows[m.cursor].groupID

	servers := m.serversIn(group)
	if len(servers) == 0 {
		return
	}

	// If everything is already on, the useful action is to clear the group.
	all := true
	for _, s := range servers {
		if !m.selected[s.ID] {
			all = false
			break
		}
	}
	for _, s := range servers {
		m.selected[s.ID] = !all
	}
	m.markCustom()
}

func (m *SetupModel) setAll(on bool) {
	for _, s := range m.inScopeServers() {
		m.selected[s.ID] = on
	}
	m.markCustom()
}

// inScopeServers is every server the --groups filter allows, so "select all"
// cannot quietly reach past the filter the user asked for.
func (m SetupModel) inScopeServers() []serverlist.Server {
	if len(m.groups) == 0 {
		return m.list.Servers
	}
	return runner.Select(m.list, runner.ModeExtended, m.groups, nil)
}

// markCustom records that the user overrode the mode's default set, so the
// result file does not claim a standard mode that was not used.
func (m *SetupModel) markCustom() {
	m.mode = runner.ModeCustom
}

func (m SetupModel) serversIn(group string) []serverlist.Server {
	inScope := make(map[string]bool, len(m.groups))
	for _, g := range m.groups {
		inScope[g] = true
	}

	var out []serverlist.Server
	for _, s := range m.list.Servers {
		if s.Group != group {
			continue
		}
		if len(inScope) > 0 && !inScope[s.Group] {
			continue
		}
		out = append(out, s)
	}
	return out
}

func (m SetupModel) groupName(id string) string {
	for _, g := range m.list.Groups {
		if g.ID == id {
			return g.Name
		}
	}
	return id
}

// help is the long and short form of the same hint.
type help struct {
	long, short string
}

// Help text is offered in two lengths: the long form is friendlier, and the
// short form keeps the help from becoming the thing that overflows a narrow
// terminal. The arrow glyphs come from the theme, so help on a console that
// cannot draw them says "^/v choose" rather than "?/? choose".
func (m SetupModel) modeHelp() help {
	g := m.theme.Glyphs()
	return help{
		long:  g.Up + "/" + g.Down + " choose  enter next  q quit",
		short: g.Up + g.Down + " choose  enter next  q quit",
	}
}

func (m SetupModel) listHelp() help {
	g := m.theme.Glyphs()
	return help{
		long: g.Up + "/" + g.Down + " move  space toggle  a group  A all  n none  " +
			g.Left + "/" + g.Right + " collapse  enter start  backspace mode  q quit",
		short: g.Up + g.Down + " move  space toggle  enter start  q quit",
	}
}

// fitFooter picks the longest help text that fits.
func fitFooter(width int, h help) string {
	if len([]rune(h.long)) <= width {
		return h.long
	}
	if len([]rune(h.short)) <= width {
		return h.short
	}
	return fitLine(h.short, width)
}

// View renders the current stage.
func (m SetupModel) View() string {
	if m.stage == stageMode {
		return m.viewMode()
	}
	return m.viewServers()
}

func (m SetupModel) viewMode() string {
	width := m.viewWidth()
	var b strings.Builder

	b.WriteString(fitLine(m.theme.Header("warpbench")+"  "+m.theme.Dim("how much should be measured?"), width) + "\n\n")

	for _, mode := range []runner.Mode{runner.ModeQuick, runner.ModeExtended} {
		servers := runner.Select(m.list, mode, m.groups, nil)
		estimate := runner.Estimate(servers, runner.BudgetFor(mode))

		// Each option states its real cost, computed from the shipped list, so
		// choosing is informed rather than a guess.
		line := fmt.Sprintf("  %-9s %2d servers   about %s per phase",
			string(mode), len(servers), formatDuration(estimate))
		if mode == m.mode {
			line = "> " + strings.TrimPrefix(line, "  ")
			line = m.theme.Selected(fitLine(line, width))
		} else {
			line = "  " + strings.TrimPrefix(line, "  ")
			line = fitLine(line, width)
		}
		b.WriteString(line + "\n")
	}

	b.WriteString("\n" + fitLine(m.theme.Dim("A full comparison runs this twice, once per phase."), width) + "\n\n")
	b.WriteString(fitLine(m.theme.Dim(fitFooter(width, m.modeHelp())), width))
	return b.String()
}

func (m SetupModel) viewServers() string {
	width := m.viewWidth()
	selected := m.Selection()

	var b strings.Builder
	b.WriteString(fitLine(m.theme.Header("warpbench")+"  "+m.theme.Dim("select targets"), width) + "\n")
	b.WriteString(fitLine(fmt.Sprintf("mode: %s   servers: %d/%d   estimated: %s for this phase",
		m.theme.Header(string(m.mode)), len(selected), len(m.list.Servers), formatDuration(m.Estimate())), width) + "\n\n")

	available := m.height - 6
	if available < 3 {
		available = len(m.rows)
	}
	start, end := window(len(m.rows), m.cursor, available)

	for i := start; i < end; i++ {
		line := m.renderRow(m.rows[i])
		if i == m.cursor {
			line = m.theme.Selected(fitLine(line, width))
		} else {
			line = fitLine(line, width)
		}
		b.WriteString(line + "\n")
	}

	if len(m.rows) == 0 {
		b.WriteString(fitLine(m.theme.Warn("no servers in this list"), width) + "\n")
	}

	b.WriteString("\n")
	if len(selected) == 0 {
		b.WriteString(fitLine(m.theme.Warn("select at least one server"), width) + "\n")
	}
	b.WriteString(fitLine(m.theme.Dim(fitFooter(width, m.listHelp())), width))
	return b.String()
}

func (m SetupModel) viewWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}

func (m SetupModel) renderRow(row setupRow) string {
	if row.kind == rowGroup {
		servers := m.serversIn(row.groupID)
		on := 0
		for _, s := range servers {
			if m.selected[s.ID] {
				on++
			}
		}
		marker := m.theme.Glyphs().Expanded
		if m.collapsed[row.groupID] {
			marker = m.theme.Glyphs().Collapsed
		}
		state := " "
		if on == len(servers) {
			state = "x"
		}
		return fmt.Sprintf("  [%s] %s %s (%d/%d)", state, marker, m.groupName(row.groupID), on, len(servers))
	}

	s := row.server
	check := " "
	if m.selected[s.ID] {
		check = "x"
	}
	return fmt.Sprintf("      [%s] %-22s %-14s %s", check, truncatePlain(s.ID, 22), capabilityBadges(s), m.theme.Dim(s.Tier))
}

// capabilityBadges renders the declared capabilities compactly.
func capabilityBadges(s serverlist.Server) string {
	var out []string
	for _, c := range []struct{ name, label string }{
		{"ping", "ping"},
		{"download", "dl"},
		{"upload", "ul"},
		{"timings", "tm"},
	} {
		if s.Has(c.name) {
			out = append(out, c.label)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return "-"
	}
	return strings.Join(out, " ")
}

// window returns the slice bounds that keep the cursor visible.
func window(total, cursor, available int) (int, int) {
	if available <= 0 || available >= total {
		return 0, total
	}
	start := cursor - available/2
	if start < 0 {
		start = 0
	}
	if start+available > total {
		start = total - available
	}
	return start, start + available
}

// fitLine truncates a rendered line to width, so no screen can wrap. A wrapped
// line breaks the cursor highlighting and makes the layout unreadable.
func fitLine(s string, width int) string {
	r := []rune(s)
	if width <= 0 || len(r) <= width {
		return s
	}
	if width == 1 {
		return string(r[:1])
	}
	return string(r[:width-1]) + "…"
}

func truncatePlain(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width <= 1 {
		return string(r[:max(0, width)])
	}
	return string(r[:width-1]) + "…"
}

// formatDuration renders a duration compactly, rounding to the second because
// a duration estimate is not precise to the millisecond and pretending
// otherwise is noise.
func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}
