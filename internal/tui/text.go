package tui

import "strings"

// wrapText wraps prose to width.
//
// Bars must never wrap, but guidance must never be truncated either: the
// explanation for a stuck WARP pause is the most useful thing on that screen,
// and cutting it to an ellipsis removes exactly the part that helps.
func wrapText(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}

	var lines []string
	var current string

	for _, word := range strings.Fields(s) {
		// A single word longer than the line is truncated rather than allowed to
		// overflow; a URL or a long token is the usual cause.
		if len([]rune(word)) > width {
			word = fitLine(word, width)
		}

		switch {
		case current == "":
			current = word
		case len([]rune(current))+1+len([]rune(word)) <= width:
			current += " " + word
		default:
			lines = append(lines, current)
			current = word
		}
	}

	if current != "" {
		lines = append(lines, current)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

// appendWrapped adds prose to a builder, one wrapped line at a time.
func appendWrapped(b *strings.Builder, s string, width int) {
	for _, line := range wrapText(s, width) {
		b.WriteString(line)
		b.WriteString("\n")
	}
}
