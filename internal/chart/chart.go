// Package chart renders horizontal bar charts as text.
//
// It is deliberately pure: no colour, no terminal, no clock. Both the TUI and
// the Markdown report draw from it, so a publisher and an interactive user see
// the same picture of the same numbers.
//
// Every renderer guarantees that no line exceeds the requested width. A wrapped
// bar line is worse than a short bar: it turns a chart into an unreadable
// paragraph, and it is the single most likely thing to break in a narrow
// terminal or a pasted GitHub comment.
package chart

import (
	"math"
	"strconv"
	"strings"
)

// Chars is a drawing character set.
type Chars struct {
	Full  string
	Empty string
	// Ellipsis marks a label or line cut short. It travels with the rest of
	// the set because a caller that needs output which survives a legacy
	// code page needs the whole thing in ASCII, and an ellipsis is not one.
	Ellipsis string
}

// ASCII is the fallback: no block or box-drawing characters, so the output
// survives any terminal, any font, and a paste into a GitHub issue.
var ASCII = Chars{Full: "#", Empty: ".", Ellipsis: "..."}

// Unicode uses block characters, for terminals that can be trusted with them.
var Unicode = Chars{Full: "█", Empty: "░", Ellipsis: "…"}

// defaultEllipsis is used when a caller supplies a character set without one,
// so a hand-built Chars keeps the historical rendering.
const defaultEllipsis = "…"

// Options controls rendering.
type Options struct {
	// Width is the total number of columns available. Every returned line is at
	// most this wide.
	Width int
	// Scale is the value that fills a bar. Zero means "use the largest value
	// present", which is what makes a set of bars comparable to each other.
	Scale float64
	// Chars selects the drawing characters.
	Chars Chars
	// ValueFormat renders a value; defaults to one decimal place.
	ValueFormat func(float64) string
}

func (o Options) withDefaults() Options {
	if o.Width <= 0 {
		o.Width = 80
	}
	if o.Chars.Full == "" {
		o.Chars = ASCII
	}
	if o.Chars.Ellipsis == "" {
		o.Chars.Ellipsis = defaultEllipsis
	}
	if o.ValueFormat == nil {
		o.ValueFormat = func(v float64) string { return trimFloat(v) }
	}
	return o
}

// Series is one bar inside a row.
type Series struct {
	// Name labels the bar, e.g. "ISP" or "WARP".
	Name string
	// Value is the measurement.
	Value float64
	// Suffix is appended to the value, e.g. " Mbps".
	Suffix string
}

// Row is a labelled group of bars, drawn one line per series.
//
// Grouping matters for this tool: the whole point is to see the same server
// before and after, and two bars sharing a scale is what makes that readable.
type Row struct {
	Label  string
	Series []Series
	// Note is appended to the last line, e.g. "+187%".
	Note string
}

// minBarWidth is the narrowest bar worth drawing. Below this the bar carries no
// information, so the layout drops it rather than showing a token block.
const minBarWidth = 3

// Render draws rows as text, one line per series.
func Render(rows []Row, opts Options) []string {
	opts = opts.withDefaults()

	if len(rows) == 0 {
		return nil
	}

	scale := opts.Scale
	if scale <= 0 {
		scale = maxValue(rows)
	}

	labelWidth, seriesWidth, valueWidth, noteWidth := measure(rows, opts)

	// Fixed cost per line: one space before the bar, one after it, and one
	// before the value, plus the note column and its gap when notes are used.
	const fixedGaps = 3
	noteCost := 0
	if noteWidth > 0 {
		noteCost = noteWidth + 2
	}

	barWidth := opts.Width - labelWidth - seriesWidth - valueWidth - fixedGaps - noteCost

	// A narrow terminal must not produce a wrapped line. The columns give way in
	// order of how little they convey: the bar shrinks first, then the series
	// name, then the label. The bar is dropped entirely rather than drawn as a
	// token block that carries no information.
	dropSeriesColumn := false
	dropNote := false

	if barWidth < minBarWidth {
		dropNote = true
		barWidth += noteCost
	}
	if barWidth < minBarWidth {
		seriesWidth = 0
		dropSeriesColumn = true
		barWidth = opts.Width - labelWidth - valueWidth - fixedGaps
	}
	if barWidth < minBarWidth {
		labelWidth = max(0, opts.Width-valueWidth-fixedGaps-minBarWidth)
		barWidth = opts.Width - labelWidth - valueWidth - fixedGaps - 1
	}
	if barWidth < 1 {
		barWidth = 0
	}

	out := make([]string, 0, len(rows)*2)
	for _, row := range rows {
		for i, s := range row.Series {
			label := ""
			if i == 0 {
				label = truncate(row.Label, labelWidth, opts.Chars)
			}

			var seriesName string
			if !dropSeriesColumn && seriesWidth > 0 {
				seriesName = s.Name
			}

			value := opts.ValueFormat(s.Value) + s.Suffix

			note := ""
			if i == len(row.Series)-1 && !dropNote {
				note = row.Note
			}

			line := buildLine(label, labelWidth, seriesName, seriesWidth, s.Value, scale, barWidth, value, valueWidth, opts.Chars)
			withNote := ""
			if note != "" {
				withNote = line + "  " + note
			}

			out = append(out, fit(line, withNote, opts.Width, opts.Chars))
		}
	}

	return out
}

// measure returns the width of each column.
func measure(rows []Row, opts Options) (labelWidth, seriesWidth, valueWidth, noteWidth int) {
	for _, row := range rows {
		labelWidth = max(labelWidth, runeLen(row.Label))
		for _, s := range row.Series {
			seriesWidth = max(seriesWidth, runeLen(s.Name))
			valueWidth = max(valueWidth, runeLen(opts.ValueFormat(s.Value)+s.Suffix))
		}
		noteWidth = max(noteWidth, runeLen(row.Note))
	}
	return labelWidth, seriesWidth, valueWidth, noteWidth
}

// fit enforces the width contract.
//
// The note is dropped before anything else, because it is the least load-bearing
// part of a line, and the whole line is truncated only when even the bare value
// cannot fit. Wrapping is never an option: a wrapped bar line turns a chart into
// an unreadable paragraph.
func fit(bare, withNote string, width int, chars Chars) string {
	if withNote != "" && runeLen(withNote) <= width {
		return withNote
	}
	if runeLen(bare) <= width {
		return bare
	}
	return truncateLine(bare, width, chars)
}

// truncateLine cuts a line to width, marking the cut when there is room for a
// marker.
func truncateLine(s string, width int, chars Chars) string {
	return truncate(s, width, chars)
}

// ellipsisFor returns the cut marker, tolerating a zero-value character set.
func ellipsisFor(chars Chars) string {
	if chars.Ellipsis == "" {
		return defaultEllipsis
	}
	return chars.Ellipsis
}

// buildLine assembles one line without its note.
func buildLine(label string, labelWidth int, seriesName string, seriesWidth int, value, scale float64, barWidth int, rendered string, valueWidth int, chars Chars) string {
	var b strings.Builder

	b.WriteString(pad(label, labelWidth))
	b.WriteString(" ")

	if seriesWidth > 0 {
		b.WriteString(pad(seriesName, seriesWidth))
		b.WriteString(" ")
	}

	if barWidth > 0 {
		b.WriteString(bar(value, scale, barWidth, chars))
	}

	b.WriteString(" ")
	b.WriteString(pad(rendered, valueWidth))

	return b.String()
}

// bar renders one bar of exactly width visible characters.
func bar(value, scale float64, width int, chars Chars) string {
	if width <= 0 {
		return ""
	}
	if scale <= 0 || value <= 0 {
		return strings.Repeat(chars.Empty, width)
	}

	filled := int(math.Round(value / scale * float64(width)))
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}

	// A value that rounds to nothing still gets one block, so "nonzero" and
	// "zero" are never drawn identically.
	if filled == 0 && value > 0 {
		filled = 1
	}

	return strings.Repeat(chars.Full, filled) + strings.Repeat(chars.Empty, width-filled)
}

func maxValue(rows []Row) float64 {
	var out float64
	for _, row := range rows {
		for _, s := range row.Series {
			if s.Value > out {
				out = s.Value
			}
		}
	}
	return out
}

// pad right-pads to width, never truncating.
func pad(s string, width int) string {
	if n := width - runeLen(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// truncate shortens to width, marking the cut with the set's ellipsis so a
// shortened label is visibly shortened rather than looking like a different
// server.
//
// The marker is reserved at its own rune width rather than assumed to occupy
// one column, because the ASCII set writes "..." and the chart's width contract
// -- no line wider than the caller asked for -- has to hold for both sets. When
// there is no room for the marker the string is cut without one, which is
// better than overflowing the line the cut was meant to fit.
func truncate(s string, width int, chars Chars) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	marker := []rune(ellipsisFor(chars))
	if len(marker) == 0 || width <= len(marker) {
		return string(r[:width])
	}
	return string(r[:width-len(marker)]) + string(marker)
}

func runeLen(s string) int { return len([]rune(s)) }

// trimFloat renders a number with the fewest digits that stay informative:
// whole numbers as integers, everything else to one decimal place.
func trimFloat(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "-"
	}
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// Width returns the visible width of a rendered line.
//
// The chart only ever emits characters of width one, so counting runes is
// exact. Widening the character sets beyond ASCII and block elements would
// require a width table, and the test suite asserts this invariant.
func Width(line string) int { return runeLen(line) }
