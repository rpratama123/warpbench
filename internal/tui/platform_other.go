//go:build !windows

package tui

// unicodeEnabled reports whether the terminal can render the block and arrow
// characters.
//
// Outside Windows there is no code page in the way: the terminal receives UTF-8
// and the characters used here are ordinary Unicode that any modern terminal
// font carries. TERM=dumb already disables colour, and the chart additionally
// falls back to ASCII whenever colour is off, so a terminal that has declared
// itself incapable is handled before this is consulted.
func unicodeEnabled() bool { return true }
