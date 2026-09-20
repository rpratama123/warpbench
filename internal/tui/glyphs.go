package tui

import "github.com/rpratama123/warpbench/internal/chart"

// Glyphs are the non-alphabetic characters the interface draws with.
//
// They are collected into one set because they fail together. On a Windows
// console running a legacy code page, Go writes output by converting UTF-8 to
// UTF-16 and calling WriteConsoleW, and Windows then re-encodes that into the
// console's code page using best-fit mapping. Neither U+2588 nor U+2591 has a
// mapping in a code page such as 437 or 1252, so Windows substitutes the same
// character -- '¦' -- for both, and every bar in a chart becomes an identical
// run of one glyph. The chart does not look wrong, it looks like data. The
// arrows fare no better and usually become '?'.
//
// Choosing per character would leave a chart that renders as unreadable noise
// next to help text that renders fine, so the whole set is chosen at once.
type Glyphs struct {
	// Chart draws the bars.
	Chart chart.Chars
	// Collapsed and Expanded mark a group in the setup browser.
	Collapsed, Expanded string
	// Done and Running mark a server's state while a run is in progress.
	Done, Running string
	// Up, Down, Left and Right name the arrow keys in the help text. They are
	// only ever used as key names, never as decorations.
	Up, Down, Left, Right string
}

// UnicodeGlyphs is the preferred set.
var UnicodeGlyphs = Glyphs{
	Chart:     chart.Unicode,
	Collapsed: "▸",
	Expanded:  "▾",
	Done:      "✓",
	Running:   "▶",
	Up:        "↑",
	Down:      "↓",
	Left:      "←",
	Right:     "→",
}

// ASCIIGlyphs is the fallback for a terminal that cannot be trusted with the
// characters above. Every glyph is representable in every code page this tool
// is likely to meet, so nothing can be substituted or dropped silently.
var ASCIIGlyphs = Glyphs{
	Chart:     chart.ASCII,
	Collapsed: ">",
	Expanded:  "v",
	Done:      "+",
	Running:   ">",
	Up:        "^",
	Down:      "v",
	Left:      "<",
	Right:     ">",
}
