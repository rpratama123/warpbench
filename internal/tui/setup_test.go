package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rpratama123/warpbench/internal/runner"
	"github.com/rpratama123/warpbench/internal/serverlist"
)

func srv(id, group, tier string, caps ...string) serverlist.Server {
	return serverlist.Server{
		ID: id, Group: group, Name: id, Protocol: "http-file",
		PingHost: "example.com", Capabilities: caps, Tier: tier,
	}
}

func testList() *serverlist.List {
	return &serverlist.List{
		Schema: 2, Revision: "2026-09-20",
		Groups: []serverlist.Group{
			{ID: "sg", Name: "Singapore"},
			{ID: "jp", Name: "Tokyo"},
		},
		Servers: []serverlist.Server{
			srv("sg-quick", "sg", "quick", "ping", "download", "timings"),
			srv("sg-ext", "sg", "extended", "ping", "download"),
			srv("jp-quick", "jp", "quick", "ping", "download", "upload"),
			srv("jp-ext", "jp", "extended", "ping", "download", "upload"),
		},
	}
}

// key builds a KeyMsg from a readable name, so tests read as keystrokes.
func key(s string) tea.KeyMsg {
	switch s {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func press(t *testing.T, m SetupModel, keys ...string) SetupModel {
	t.Helper()
	for _, k := range keys {
		m, _ = m.Update(key(k))
	}
	return m
}

func sized(t *testing.T, m SetupModel, width, height int) SetupModel {
	t.Helper()
	m, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

func ids(servers []serverlist.Server) []string {
	out := make([]string, 0, len(servers))
	for _, s := range servers {
		out = append(out, s.ID)
	}
	return out
}

// --- mode stage ------------------------------------------------------------

func TestSetupStartsOnTheModeChoice(t *testing.T) {
	m := NewSetup(testList(), runner.ModeQuick, NewTheme(false), nil)

	if m.Stage() != stageMode {
		t.Error("setup should start by choosing how much to measure")
	}
	if m.Mode() != runner.ModeQuick {
		t.Errorf("Mode() = %q, want quick", m.Mode())
	}
	if got := ids(m.Selection()); strings.Join(got, ",") != "sg-quick,jp-quick" {
		t.Errorf("Selection() = %v, want the quick tier", got)
	}
}

func TestModeChoiceChangesTheSelection(t *testing.T) {
	m := NewSetup(testList(), runner.ModeQuick, NewTheme(false), nil)

	m = press(t, m, "down")
	if m.Mode() != runner.ModeExtended {
		t.Fatalf("Mode() = %q, want extended", m.Mode())
	}
	if got := len(m.Selection()); got != 4 {
		t.Errorf("extended should select every server, got %d", got)
	}

	m = press(t, m, "up")
	if m.Mode() != runner.ModeQuick || len(m.Selection()) != 2 {
		t.Errorf("moving back up should restore the quick tier, got %q/%d", m.Mode(), len(m.Selection()))
	}
}

func TestModeScreenStatesTheCostOfEachOption(t *testing.T) {
	m := sized(t, NewSetup(testList(), runner.ModeQuick, NewTheme(false), nil), 80, 24)
	view := m.View()

	for _, want := range []string{"quick", "extended", "servers", "about", "twice"} {
		if !strings.Contains(view, want) {
			t.Errorf("mode screen missing %q:\n%s", want, view)
		}
	}
}

func TestEnterAdvancesToSelection(t *testing.T) {
	m := NewSetup(testList(), runner.ModeQuick, NewTheme(false), nil)
	m = press(t, m, "enter")

	if m.Stage() != stageServers {
		t.Error("enter should advance to the server list")
	}
}

func TestQuitFromTheModeScreen(t *testing.T) {
	m := press(t, NewSetup(testList(), runner.ModeQuick, NewTheme(false), nil), "q")

	if !m.Quit {
		t.Error("q did not request a quit")
	}
}

// --- selection stage -------------------------------------------------------

func selectStage(t *testing.T) SetupModel {
	t.Helper()
	// The glyph set is pinned rather than left to NewTheme: colour and glyph
	// capability are independent, and a test that asserts a specific marker
	// must not depend on the code page of whatever machine runs it.
	return press(t, NewSetup(testList(), runner.ModeQuick, NewThemeWithGlyphs(false, true), nil), "enter")
}

func TestBackspaceReturnsToTheModeChoice(t *testing.T) {
	m := press(t, selectStage(t), "backspace")

	if m.Stage() != stageMode {
		t.Error("backspace should return to the mode choice")
	}
}

func TestCursorMovesAndClamps(t *testing.T) {
	m := selectStage(t)

	if m.cursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", m.cursor)
	}
	m = press(t, m, "up")
	if m.cursor != 0 {
		t.Errorf("cursor moved above the first row: %d", m.cursor)
	}

	for range len(m.rows) + 5 {
		m = press(t, m, "down")
	}
	if m.cursor != len(m.rows)-1 {
		t.Errorf("cursor = %d, want it clamped to the last row (%d)", m.cursor, len(m.rows)-1)
	}
}

func TestSpaceTogglesAServerAndMarksTheModeCustom(t *testing.T) {
	m := selectStage(t)

	// Row 0 is the Singapore header, row 1 the first server.
	m = press(t, m, "down", " ")
	if m.selected["sg-quick"] {
		t.Error("space did not deselect the server under the cursor")
	}
	if m.Mode() != runner.ModeCustom {
		t.Errorf("Mode() = %q, want custom after a manual edit", m.Mode())
	}

	m = press(t, m, " ")
	if !m.selected["sg-quick"] {
		t.Error("space did not reselect the server")
	}
}

// Toggling on a header is the natural way to say "this whole country".
//
// In quick mode Singapore is mixed -- sg-quick is on and sg-ext is off -- so the
// useful action is to fill the group rather than clear it, and only the group
// under the cursor may change.
func TestSpaceOnAGroupHeaderTogglesTheGroup(t *testing.T) {
	m := selectStage(t)

	m = press(t, m, " ")
	if !m.selected["sg-quick"] || !m.selected["sg-ext"] {
		t.Error("space on a mixed group should select it entirely")
	}
	if !m.selected["jp-quick"] {
		t.Error("only the group under the cursor should change")
	}

	// Now the group is full, so the same key clears it.
	m = press(t, m, " ")
	if m.selected["sg-quick"] || m.selected["sg-ext"] {
		t.Error("space on a full group should clear it")
	}
}

func TestGroupToggleInvertsRatherThanAlwaysEnabling(t *testing.T) {
	m := selectStage(t)

	// Put the cursor on the Singapore header.
	if m.rows[m.cursor].kind != rowGroup {
		t.Fatalf("cursor is not on a header: %+v", m.rows[m.cursor])
	}

	// The group starts mixed, so `a` fills it.
	m = press(t, m, "a")
	if !m.selected["sg-quick"] || !m.selected["sg-ext"] {
		t.Error("`a` on a mixed group should select it entirely")
	}

	// Pressing it again clears, because everything is already on.
	m = press(t, m, "a")
	if m.selected["sg-quick"] || m.selected["sg-ext"] {
		t.Error("`a` on a fully selected group should clear it")
	}
}

func TestSelectAllAndNone(t *testing.T) {
	m := selectStage(t)

	m = press(t, m, "A")
	if len(m.Selection()) != 4 {
		t.Errorf("A selected %d servers, want 4", len(m.Selection()))
	}

	m = press(t, m, "n")
	if len(m.Selection()) != 0 {
		t.Errorf("n left %d servers selected", len(m.Selection()))
	}
}

func TestCollapseHidesServersAndKeepsTheCursorOnTheGroup(t *testing.T) {
	m := selectStage(t)
	before := len(m.rows)

	m = press(t, m, "left")
	if !m.collapsed["sg"] {
		t.Error("left should collapse the group under the cursor")
	}
	if len(m.rows) != before-2 {
		t.Errorf("rows = %d, want %d after collapsing two servers", len(m.rows), before-2)
	}
	if m.rows[m.cursor].kind != rowGroup {
		t.Error("the cursor should stay on the collapsed group so the key can be pressed again")
	}
	if got := m.View(); !strings.Contains(got, "▸") {
		t.Error("a collapsed group should be marked")
	}

	m = press(t, m, "right")
	if m.collapsed["sg"] {
		t.Error("right should expand the group")
	}
	if len(m.rows) != before {
		t.Errorf("rows = %d, want %d after expanding", len(m.rows), before)
	}
}

// A run with nothing selected would measure nothing, so it must not start.
func TestEnterRefusesAnEmptySelection(t *testing.T) {
	m := press(t, selectStage(t), "n")

	m = press(t, m, "enter")
	if m.Done {
		t.Error("enter accepted an empty selection")
	}
	if !strings.Contains(m.View(), "select at least one server") {
		t.Error("the screen should say why it will not continue")
	}

	m = press(t, m, "A", "enter")
	if !m.Done {
		t.Error("enter did not confirm a non-empty selection")
	}
}

func TestSelectionAndEstimateFollowTheToggles(t *testing.T) {
	m := selectStage(t)
	initial := m.Estimate()

	m = press(t, m, "A")
	if m.Estimate() <= initial {
		t.Errorf("selecting more servers should raise the estimate: %v -> %v", initial, m.Estimate())
	}

	m = press(t, m, "n")
	if m.Estimate() != 0 {
		t.Errorf("Estimate() with nothing selected = %v, want 0", m.Estimate())
	}
}

// --- layout ----------------------------------------------------------------

// No screen may wrap. A wrapped line breaks the highlight bars and makes the
// layout unreadable in exactly the narrow terminal this test covers.
func TestSetupNeverExceedsTheTerminalWidth(t *testing.T) {
	for _, width := range []int{30, 40, 60, 80, 120, 200} {
		for _, stage := range []SetupModel{
			NewSetup(testList(), runner.ModeQuick, NewTheme(false), nil),
			selectStage(t),
		} {
			m := sized(t, stage, width, 24)
			for i, line := range strings.Split(m.View(), "\n") {
				if got := len([]rune(line)); got > width {
					t.Errorf("width %d, line %d is %d columns: %q", width, i, got, line)
				}
			}
		}
	}
}

func TestSetupFitsANarrowTerminalAtSixtyColumns(t *testing.T) {
	m := sized(t, selectStage(t), 60, 20)

	view := m.View()
	if !strings.Contains(view, "sg-quick") {
		t.Errorf("the server list is not visible at 60 columns:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if len([]rune(line)) > 60 {
			t.Errorf("line exceeds 60 columns: %q", line)
		}
	}
}

func TestViewWorksBeforeAnyWindowSize(t *testing.T) {
	// bubbletea delivers the size asynchronously, so a View before the first
	// WindowSizeMsg must still render.
	m := NewSetup(testList(), runner.ModeQuick, NewTheme(false), nil)

	if got := m.View(); !strings.Contains(got, "warpbench") {
		t.Errorf("View() before a size = %q", got)
	}
}

func TestLongServerListScrollsInsteadOfOverflowing(t *testing.T) {
	list := testList()
	for i := range 60 {
		list.Servers = append(list.Servers, srv(
			"sg-extra-"+string(rune('a'+i%26))+string(rune('a'+i/26)),
			"sg", "extended", "ping", "download"))
	}

	m := sized(t, NewSetup(list, runner.ModeExtended, NewTheme(false), nil), 60, 20)
	m = press(t, m, "enter")

	// Walk to the end; the visible window must follow without exceeding height.
	for range len(m.rows) + 2 {
		m = press(t, m, "down")
	}

	lines := strings.Split(m.View(), "\n")
	if len(lines) > 20 {
		t.Errorf("the view is %d lines tall in a 20-line terminal", len(lines))
	}
}

func TestWindowBounds(t *testing.T) {
	tests := map[string]struct {
		total, cursor, available int
		wantStart, wantEnd       int
	}{
		"everything fits": {5, 2, 10, 0, 5},
		"no space given":  {5, 2, 0, 0, 5},
		"cursor at top":   {20, 0, 5, 0, 5},
		"cursor at end":   {20, 19, 5, 15, 20},
		"cursor middle":   {20, 10, 5, 8, 13},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			start, end := window(tc.total, tc.cursor, tc.available)
			if start != tc.wantStart || end != tc.wantEnd {
				t.Errorf("window() = %d,%d want %d,%d", start, end, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

// --- small helpers ---------------------------------------------------------

func TestCapabilityBadges(t *testing.T) {
	if got := capabilityBadges(srv("a", "sg", "quick", "ping", "download", "upload", "timings")); got != "dl ping tm ul" {
		t.Errorf("capabilityBadges() = %q", got)
	}
	if got := capabilityBadges(srv("a", "sg", "quick")); got != "-" {
		t.Errorf("capabilityBadges() with none = %q, want -", got)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := map[time.Duration]string{
		0:                               "0s",
		-1 * time.Second:                "0s",
		45 * time.Second:                "45s",
		90 * time.Second:                "1m30s",
		7*time.Minute + 36*time.Second:  "7m36s",
		2 * time.Hour:                   "2h00m",
		75*time.Minute + 30*time.Second: "1h15m",
	}

	for in, want := range tests {
		if got := formatDuration(in); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestColourEnabled(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	// LookupEnv sees NO_COLOR as present even when empty, which is the
	// documented convention: any value means "do not colour".
	if ColourEnabled(false) {
		t.Error("NO_COLOR should disable colour even when set to an empty value")
	}

	t.Setenv("TERM", "dumb")
	if ColourEnabled(false) {
		t.Error("TERM=dumb should disable colour")
	}
	if ColourEnabled(true) {
		t.Error("--no-color should disable colour")
	}
}

func TestThemeWithoutColourReturnsPlainText(t *testing.T) {
	theme := NewTheme(false)

	for _, got := range []string{
		theme.Header("x"), theme.Selected("x"), theme.Dim("x"),
		theme.Better("x"), theme.Worse("x"), theme.Warn("x"),
	} {
		if got != "x" {
			t.Errorf("a colourless theme altered the text: %q", got)
		}
	}
}

func TestThemeWithColourAddsEscapes(t *testing.T) {
	theme := NewTheme(true)

	if got := theme.Header("x"); got == "x" {
		t.Error("a coloured theme should style the header")
	}
	if !theme.Colour() {
		t.Error("Colour() = false")
	}
}

func TestFitLineAndTruncatePlain(t *testing.T) {
	if got := fitLine("abcdef", 4); got != "abc…" {
		t.Errorf("fitLine = %q", got)
	}
	if got := fitLine("abc", 10); got != "abc" {
		t.Errorf("fitLine should not pad: %q", got)
	}
	if got := fitLine("abc", 0); got != "abc" {
		t.Errorf("fitLine with width 0 should pass through: %q", got)
	}
	if got := truncatePlain("abcdef", 1); got != "a" {
		t.Errorf("truncatePlain to 1 = %q", got)
	}
	if got := truncatePlain("abcdef", 0); got != "" {
		t.Errorf("truncatePlain to 0 = %q", got)
	}
}

// --groups must be honoured interactively, not silently ignored: a user who
// narrows the run to one group must not be shown, or measure, the rest.
func TestSetupHonoursAGroupFilter(t *testing.T) {
	list := testList()
	m := NewSetup(list, runner.ModeExtended, NewTheme(false), []string{"sg"})

	got := ids(m.Selection())
	if strings.Join(got, ",") != "sg-quick,sg-ext" {
		t.Errorf("Selection() = %v, want only the sg group", got)
	}

	// The list must not offer out-of-scope servers either.
	m = press(t, m, "enter")
	view := m.View()
	if strings.Contains(view, "jp-quick") || strings.Contains(view, "Tokyo") {
		t.Errorf("out-of-scope group is still listed:\n%s", view)
	}
	if !strings.Contains(view, "Singapore") {
		t.Errorf("the filtered group is missing:\n%s", view)
	}

	// Selecting all must stay inside the filter.
	m = press(t, m, "n", "A")
	if got := ids(m.Selection()); strings.Join(got, ",") != "sg-quick,sg-ext" {
		t.Errorf("A selected beyond the filter: %v", got)
	}

	// The mode screen's counts must reflect the filter too.
	m = press(t, m, "backspace")
	if strings.Contains(m.View(), "10 servers") {
		t.Errorf("mode screen ignores the group filter:\n%s", m.View())
	}
}

func TestSetupWithoutAGroupFilterSeesEverything(t *testing.T) {
	m := NewSetup(testList(), runner.ModeExtended, NewTheme(false), nil)

	if got := len(m.Selection()); got != 4 {
		t.Errorf("Selection() = %d, want all four", got)
	}
}
