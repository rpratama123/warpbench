package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/text/encoding/charmap"

	"github.com/rpratama123/warpbench/internal/results"
	"github.com/rpratama123/warpbench/internal/runner"
	"github.com/rpratama123/warpbench/internal/serverlist"
)

// assertLegacyConsoleSafe fails if the view contains a character a Windows
// console running a legacy code page cannot render.
//
// Windows re-encodes console output into the active code page, substituting a
// best-fit character for anything the page does not contain. U+2588 and U+2591
// both best-fit to '¦', so a chart drawn with them becomes a uniform run and
// stops carrying information at all. Windows-1252 is the page this tool is most
// likely to meet, and it is exactly the set of characters that survive: "·" and
// "…" are in it, the block and arrow characters are not.
func assertLegacyConsoleSafe(t *testing.T, what, view string) {
	t.Helper()
	if _, err := charmap.Windows1252.NewEncoder().String(view); err != nil {
		t.Errorf("%s is not renderable on a legacy Windows console: %v\n%s", what, err, view)
	}
}

func TestASCIIGlyphsSurviveALegacyConsole(t *testing.T) {
	g := ASCIIGlyphs
	for name, s := range map[string]string{
		"Chart.Full": g.Chart.Full, "Chart.Empty": g.Chart.Empty,
		"Chart.Ellipsis": g.Chart.Ellipsis,
		"Collapsed":      g.Collapsed, "Expanded": g.Expanded,
		"Done": g.Done, "Running": g.Running,
		"Up": g.Up, "Down": g.Down, "Left": g.Left, "Right": g.Right,
	} {
		if _, err := charmap.Windows1252.NewEncoder().String(s); err != nil {
			t.Errorf("ASCIIGlyphs.%s = %q is not renderable on a legacy console", name, s)
		}
	}
}

// This is the reported bug: a colour-capable Windows console is not the same
// thing as a Unicode-capable one, and the chart must follow the console rather
// than the colour decision.
func TestLegacyConsoleGetsASCIIChartsEvenWithColour(t *testing.T) {
	theme := NewThemeWithGlyphs(true, false)
	if !theme.Colour() {
		t.Fatal("this theme is meant to keep colour on")
	}

	m := NewResultsModel(comparison(), theme)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	view := m.View()

	if strings.Contains(view, "█") {
		t.Errorf("a console that cannot draw block characters was given some:\n%s", view)
	}
	if !strings.Contains(view, "#") {
		t.Errorf("expected ASCII bars:\n%s", view)
	}
	assertLegacyConsoleSafe(t, "the results view", view)
}

func TestSetupAndProgressSurviveALegacyConsole(t *testing.T) {
	theme := NewThemeWithGlyphs(true, false)

	setup := NewSetup(testList(), runner.ModeQuick, theme, nil)
	setup = sized(t, setup, 100, 30)
	// Entering the list stage is what draws the collapse markers and the
	// arrow-key help text.
	setup = press(t, setup, "enter")
	assertLegacyConsoleSafe(t, "the setup view", setup.View())

	pm := NewProgressModel(theme)
	pm, _ = pm.Update(phaseStartedMsg{
		phase:    "baseline",
		servers:  []serverlist.Server{srv("sg-1", "sg", "quick", "ping", "download")},
		estimate: time.Minute,
	})
	pm, _ = pm.Update(stepMsg{server: srv("sg-1", "sg", "quick", "ping"), step: "download"})
	assertLegacyConsoleSafe(t, "the progress view", pm.View())

	pm, _ = pm.Update(serverDoneMsg{
		server: srv("sg-1", "sg", "quick", "ping"),
		measured: results.Server{
			ID:   "sg-1",
			Ping: &results.Ping{Method: "icmp", Sent: 10, Received: 10, AvgMs: 42},
		},
	})
	assertLegacyConsoleSafe(t, "the completed progress view", pm.View())
}

// The fallback must not cost a capable terminal its charts.
func TestUnicodeThemeStillDrawsBlocks(t *testing.T) {
	theme := NewThemeWithGlyphs(true, true)
	if !theme.Unicode() {
		t.Fatal("Unicode() = false for a theme built with unicode on")
	}

	m := NewResultsModel(comparison(), theme)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if !strings.Contains(m.View(), "█") {
		t.Error("a Unicode terminal should still get block characters")
	}
}

// Colour off is the stronger signal: it forces ASCII whatever the console is.
func TestColourlessThemeIsASCIIWhateverTheConsole(t *testing.T) {
	theme := NewThemeWithGlyphs(false, true)
	m := NewResultsModel(comparison(), theme)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	if strings.Contains(m.View(), "█") {
		t.Errorf("a colourless run should draw ASCII bars:\n%s", m.View())
	}
}
