// Package tui is the interactive front end.
//
// It is a second front end over the same runner the plain path drives, not a
// second implementation: every measurement, every fairness rule and every
// result file comes from internal/runner, so the interactive and
// non-interactive paths cannot disagree about what was measured.
//
// The models are ordinary bubbletea models with no terminal dependency.
// Update and View are pure functions of their input, which is what makes the
// layout testable at a fixed width instead of only by eye.
package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Theme renders styled text, or leaves it alone when colour is unavailable.
//
// Colour is a nicety; the charts have to be readable without it, because a
// substantial share of terminals, CI logs and pasted issues have none.
type Theme struct {
	colour   bool
	renderer *lipgloss.Renderer
}

// ColourEnabled reports whether colour should be used, given the environment
// and the user's flags.
//
// NO_COLOR is honoured because it is the conventional way a user says "never";
// TERM=dumb means the terminal has told us it cannot do this.
func ColourEnabled(noColourFlag bool) bool {
	if noColourFlag {
		return false
	}
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	if strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return false
	}
	return true
}

// NewTheme returns a theme with colour on or off.
//
// The colour profile is set explicitly rather than left to lipgloss's
// detection. Detection answers "what can this output do", and a caller that has
// already decided must not have that decision silently overridden -- which is
// exactly what happens when output is a pipe or a test buffer.
func NewTheme(colour bool) Theme {
	renderer := lipgloss.NewRenderer(os.Stdout)
	if colour {
		renderer.SetColorProfile(termenv.ANSI256)
	} else {
		renderer.SetColorProfile(termenv.Ascii)
	}
	return Theme{colour: colour, renderer: renderer}
}

// Header is used for titles and section headings.
func (t Theme) Header(s string) string {
	if !t.colour {
		return s
	}
	return t.renderer.NewStyle().Bold(true).Render(s)
}

// Selected marks the row under the cursor.
func (t Theme) Selected(s string) string {
	if !t.colour {
		return s
	}
	return t.renderer.NewStyle().Bold(true).Reverse(true).Render(s)
}

// Dim is used for secondary information.
func (t Theme) Dim(s string) string {
	if !t.colour {
		return s
	}
	return t.renderer.NewStyle().Faint(true).Render(s)
}

// Better marks an improvement. Green, but only where the metric's direction
// says the change is actually an improvement.
func (t Theme) Better(s string) string {
	if !t.colour {
		return s
	}
	return t.renderer.NewStyle().Foreground(lipgloss.Color("2")).Render(s)
}

// Worse marks a regression.
func (t Theme) Worse(s string) string {
	if !t.colour {
		return s
	}
	return t.renderer.NewStyle().Foreground(lipgloss.Color("1")).Render(s)
}

// Warn marks something the user needs to notice, such as a substitution that
// changes what a number means.
func (t Theme) Warn(s string) string {
	if !t.colour {
		return s
	}
	return t.renderer.NewStyle().Foreground(lipgloss.Color("3")).Render(s)
}

// Colour reports whether this theme applies colour.
func (t Theme) Colour() bool { return t.colour }
