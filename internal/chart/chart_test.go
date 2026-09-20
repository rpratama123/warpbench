package chart

import (
	"strings"
	"testing"
)

func rows() []Row {
	return []Row{
		{Label: "sg-linode", Series: []Series{
			{Name: "ISP", Value: 41.2, Suffix: " Mbps"},
			{Name: "WARP", Value: 118.4, Suffix: " Mbps"},
		}, Note: "+187%"},
		{Label: "jp-vultr", Series: []Series{
			{Name: "ISP", Value: 33.8, Suffix: " Mbps"},
			{Name: "WARP", Value: 96.1, Suffix: " Mbps"},
		}, Note: "+184%"},
		{Label: "id-myrepublic-iperf3", Series: []Series{
			{Name: "ISP", Value: 92.4, Suffix: " Mbps"},
			{Name: "WARP", Value: 88.7, Suffix: " Mbps"},
		}, Note: "-4%"},
	}
}

// The most likely way a chart breaks in a narrow terminal or a pasted comment
// is by wrapping, so this is the central invariant.
func TestNeverExceedsTheRequestedWidth(t *testing.T) {
	data := rows()

	for width := 12; width <= 200; width++ {
		for _, chars := range []Chars{ASCII, Unicode} {
			opts := Options{Width: width, Chars: chars}
			for _, line := range Render(data, opts) {
				if got := Width(line); got > width {
					t.Fatalf("width %d: line is %d columns: %q", width, got, line)
				}
			}
		}
	}
}

func TestRendersBothSeriesPerRow(t *testing.T) {
	lines := Render(rows(), Options{Width: 80})

	// Three rows of two series each.
	if len(lines) != 6 {
		t.Fatalf("got %d lines, want 6:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[0], "sg-linode") || !strings.Contains(lines[0], "ISP") {
		t.Errorf("first line = %q, want the label and the first series", lines[0])
	}
	if !strings.Contains(lines[1], "WARP") {
		t.Errorf("second line = %q, want the second series", lines[1])
	}
	// The row label appears once, on the first line.
	if strings.Contains(lines[1], "sg-linode") {
		t.Errorf("the row label was repeated on the second line: %q", lines[1])
	}
	// The note belongs to the last line of the row.
	if !strings.Contains(lines[1], "+187%") {
		t.Errorf("second line = %q, want the note", lines[1])
	}
}

// Bars share one scale, which is what makes them comparable across rows.
func TestBarsShareAScale(t *testing.T) {
	lines := Render([]Row{
		{Label: "a", Series: []Series{{Name: "x", Value: 100}}},
		{Label: "b", Series: []Series{{Name: "x", Value: 50}}},
	}, Options{Width: 40, Chars: ASCII})

	full := strings.Count(lines[0], "#")
	half := strings.Count(lines[1], "#")

	if full == 0 {
		t.Fatalf("the largest bar is empty: %q", lines[0])
	}
	if half*2 != full && half*2 != full-1 && half*2 != full+1 {
		t.Errorf("half the value should draw about half the bar: %d vs %d", half, full)
	}
}

func TestExplicitScaleIsHonoured(t *testing.T) {
	lines := Render([]Row{{Label: "a", Series: []Series{{Name: "x", Value: 50}}}}, Options{
		Width: 30, Scale: 100, Chars: ASCII,
	})

	if !strings.Contains(lines[0], ".") {
		t.Errorf("with scale 100 a value of 50 should leave empty space: %q", lines[0])
	}
}

func TestZeroAndNegativeValues(t *testing.T) {
	lines := Render([]Row{
		{Label: "zero", Series: []Series{{Name: "x", Value: 0}}},
		{Label: "negative", Series: []Series{{Name: "x", Value: -5}}},
		{Label: "tiny", Series: []Series{{Name: "x", Value: 0.0001}}},
	}, Options{Width: 40, Scale: 100, Chars: ASCII})

	if strings.Contains(lines[0], "#") {
		t.Errorf("a zero value should draw no filled blocks: %q", lines[0])
	}
	if strings.Contains(lines[1], "#") {
		t.Errorf("a negative value should draw no filled blocks: %q", lines[1])
	}
	// A nonzero value that rounds to nothing still gets one block, so it is not
	// drawn identically to zero.
	if !strings.Contains(lines[2], "#") {
		t.Errorf("a tiny nonzero value should still draw one block: %q", lines[2])
	}
}

func TestAllZeroRowsDoNotDivideByZero(t *testing.T) {
	lines := Render([]Row{
		{Label: "a", Series: []Series{{Name: "x", Value: 0}}},
		{Label: "b", Series: []Series{{Name: "x", Value: 0}}},
	}, Options{Width: 40})

	for _, line := range lines {
		if strings.Contains(line, "NaN") || strings.Contains(line, "Inf") {
			t.Errorf("line = %q", line)
		}
	}
}

// A shortened label must look shortened, or it reads as a different server.
func TestLongLabelsAreTruncatedVisibly(t *testing.T) {
	row := []Row{{
		Label:  "a-very-long-server-identifier-that-will-not-fit",
		Series: []Series{{Name: "ISP", Value: 1, Suffix: " Mbps"}},
	}}

	for _, tc := range []struct {
		name  string
		chars Chars
		want  string
	}{
		{"unicode", Unicode, "…"},
		{"ascii", ASCII, "..."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := Render(row, Options{Width: 40, Chars: tc.chars})
			line := lines[0]
			if !strings.Contains(line, tc.want) {
				t.Errorf("line = %q, want the %q truncation marker", line, tc.want)
			}
			if Width(line) > 40 {
				t.Errorf("line is %d columns, want at most 40", Width(line))
			}
		})
	}
}

func TestASCIISetUsesNoBlockCharacters(t *testing.T) {
	lines := Render(rows(), Options{Width: 80, Chars: ASCII})

	joined := strings.Join(lines, "\n")
	for _, block := range []string{"█", "░", "▏", "▕"} {
		if strings.Contains(joined, block) {
			t.Errorf("ASCII output contains %q:\n%s", block, joined)
		}
	}
	if !strings.Contains(joined, "#") {
		t.Error("ASCII output has no bars")
	}
}

func TestUnicodeSetUsesBlockCharacters(t *testing.T) {
	lines := Render(rows(), Options{Width: 80, Chars: Unicode})

	if !strings.Contains(strings.Join(lines, "\n"), "█") {
		t.Error("Unicode output has no bars")
	}
}

// Values are rendered compactly, and whole numbers must not lose digits.
func TestValueFormatting(t *testing.T) {
	tests := map[float64]string{
		0:       "0",
		1:       "1",
		41.2:    "41.2",
		100:     "100",
		1000:    "1000",
		1234.56: "1234.6",
		-5:      "-5",
		-0.5:    "-0.5",
	}

	for in, want := range tests {
		if got := trimFloat(in); got != want {
			t.Errorf("trimFloat(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestCustomValueFormat(t *testing.T) {
	lines := Render([]Row{{Label: "a", Series: []Series{{Name: "x", Value: 3, Suffix: "s"}}}}, Options{
		Width:       30,
		ValueFormat: func(v float64) string { return "V" },
	})

	if !strings.Contains(lines[0], "Vs") {
		t.Errorf("line = %q, want the custom format", lines[0])
	}
}

func TestEmptyInput(t *testing.T) {
	if got := Render(nil, Options{Width: 80}); got != nil {
		t.Errorf("Render(nil) = %v, want nil", got)
	}
	if got := Render([]Row{}, Options{Width: 80}); len(got) != 0 {
		t.Errorf("Render(empty) = %v", got)
	}
}

// A single-series row is the common case for a plain metric list.
func TestSingleSeriesRows(t *testing.T) {
	lines := Render([]Row{
		{Label: "a", Series: []Series{{Name: "dl", Value: 10, Suffix: " Mbps"}}},
		{Label: "b", Series: []Series{{Name: "dl", Value: 20, Suffix: " Mbps"}}},
	}, Options{Width: 60})

	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	for _, line := range lines {
		if !strings.Contains(line, "Mbps") {
			t.Errorf("line = %q, want the suffix", line)
		}
	}
}

// Options are taken by value, so a caller cannot be surprised by defaults
// leaking back out.
func TestDefaultsDoNotMutateOptions(t *testing.T) {
	opts := Options{Width: 0}
	_ = Render(rows(), opts)

	if opts.Width != 0 {
		t.Error("Render mutated the caller's Options")
	}
}

func TestDefaultsAreUsable(t *testing.T) {
	lines := Render(rows(), Options{})
	if len(lines) == 0 {
		t.Fatal("no output with zero-value options")
	}
	for _, line := range lines {
		if Width(line) > 80 {
			t.Errorf("default width should be 80, got %d", Width(line))
		}
	}
	// The zero-value Options have no Chars, so the ASCII fallback applies.
	if strings.Contains(strings.Join(lines, ""), "█") {
		t.Error("zero-value options should fall back to ASCII")
	}
}

func TestRuneLenAndTruncate(t *testing.T) {
	if got := runeLen("abc"); got != 3 {
		t.Errorf("runeLen = %d", got)
	}
	if got := truncate("abcdef", 4, Unicode); got != "abc…" {
		t.Errorf("truncate = %q, want abc…", got)
	}
	if got := truncate("abcdef", 4, ASCII); got != "a..." {
		t.Errorf("ASCII truncate = %q, want a... (marker reserved at its own width)", got)
	}
	if got := truncate("abc", 10, Unicode); got != "abc" {
		t.Errorf("truncate should not pad: %q", got)
	}
	if got := truncate("abc", 0, Unicode); got != "" {
		t.Errorf("truncate to 0 = %q", got)
	}
	if got := truncate("abc", 1, Unicode); got != "a" {
		t.Errorf("truncate to 1 = %q", got)
	}
	// A hand-built character set without an ellipsis keeps the old rendering.
	if got := truncate("abcdef", 4, Chars{Full: "#", Empty: "."}); got != "abc…" {
		t.Errorf("zero-value ellipsis = %q, want abc…", got)
	}
}

// The ASCII set is what the Markdown report is written with, and that report is
// saved, mailed and pasted by readers on every platform. If a non-ASCII
// character reaches it, a reader on a legacy code page loses it silently.
func TestASCIICharsAreActuallyASCII(t *testing.T) {
	rows := []Row{{
		Label: "a-very-long-server-identifier-indeed",
		Series: []Series{
			{Name: "ISP", Value: 41.2, Suffix: " Mbps"},
			{Name: "WARP", Value: 118.4, Suffix: " Mbps"},
		},
		Note: "+187%  better",
	}}

	for _, width := range []int{12, 20, 40, 72, 200} {
		lines := Render(rows, Options{Width: width, Chars: ASCII})
		for _, line := range lines {
			for _, r := range line {
				if r > 127 {
					t.Fatalf("ASCII chart at width %d emitted %q (U+%04X): %q", width, r, r, line)
				}
			}
		}
	}
}

func TestPad(t *testing.T) {
	if got := pad("ab", 4); got != "ab  " {
		t.Errorf("pad = %q", got)
	}
	if got := pad("abcdef", 3); got != "abcdef" {
		t.Errorf("pad must not truncate: %q", got)
	}
}
