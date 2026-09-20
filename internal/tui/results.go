package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rpratama123/warpbench/internal/chart"
	"github.com/rpratama123/warpbench/internal/results"
)

// metricSpec describes one page of the results.
type metricSpec struct {
	name   string
	unit   string
	format func(float64) string
	get    func(results.ServerDelta) *results.Delta
}

// metricPages is the order the results are paged in: throughput first, because
// that is the question most people are asking.
func metricPages() []metricSpec {
	oneDecimal := func(v float64) string { return fmt.Sprintf("%.1f", v) }
	twoDecimals := func(v float64) string { return fmt.Sprintf("%.2f", v) }

	return []metricSpec{
		{name: "download", unit: " Mbps", format: oneDecimal,
			get: func(s results.ServerDelta) *results.Delta { return s.Download }},
		{name: "upload", unit: " Mbps", format: oneDecimal,
			get: func(s results.ServerDelta) *results.Delta { return s.Upload }},
		{name: "latency (avg)", unit: " ms", format: oneDecimal,
			get: func(s results.ServerDelta) *results.Delta { return s.Latency }},
		{name: "jitter", unit: " ms", format: oneDecimal,
			get: func(s results.ServerDelta) *results.Delta { return s.Jitter }},
		{name: "loss", unit: " %", format: twoDecimals,
			get: func(s results.ServerDelta) *results.Delta { return s.Loss }},
		{name: "ttfb", unit: " ms", format: oneDecimal,
			get: func(s results.ServerDelta) *results.Delta { return s.TTFB }},
	}
}

// ResultsModel shows the side-by-side comparison.
type ResultsModel struct {
	comparison *results.Comparison
	page       int
	width      int
	height     int
	theme      Theme
	plain      bool

	// Done is set when the user finishes looking.
	Done bool
	// Quit is set when the user aborts.
	Quit bool
}

// NewResultsModel builds the results screen.
//
// plain forces the ASCII character set, which is what a no-colour terminal or a
// --no-color run gets.
func NewResultsModel(cmp *results.Comparison, theme Theme) ResultsModel {
	return ResultsModel{comparison: cmp, theme: theme, plain: !theme.Colour()}
}

// Pages is how many metric pages exist.
func (m ResultsModel) Pages() int { return len(metricPages()) }

// Page is the current page index, for tests.
func (m ResultsModel) Page() int { return m.page }

// Update handles paging and quitting.
func (m ResultsModel) Update(msg tea.Msg) (ResultsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.Quit = true
		case "enter":
			m.Done = true
		case "right", "l", "tab", "down", "j":
			if m.page < m.Pages()-1 {
				m.page++
			}
		case "left", "h", "shift+tab", "up", "k":
			if m.page > 0 {
				m.page--
			}
		}
	}
	return m, nil
}

// chars returns the drawing set appropriate to this run.
func (m ResultsModel) chars() chart.Chars {
	if m.plain {
		return chart.ASCII
	}
	return chart.Unicode
}

// View renders the current page.
func (m ResultsModel) View() string {
	width := m.viewWidth()
	var b strings.Builder

	b.WriteString(fitLine(m.theme.Header("warpbench")+"  "+m.theme.Dim("results"), width) + "\n")

	if m.comparison != nil {
		// The headline sentence comes first: it is the answer to the question
		// the user actually asked.
		b.WriteString(fitLine(m.comparison.Summary.Headline(), width) + "\n")
	}
	b.WriteString("\n")

	pages := metricPages()
	if m.page >= len(pages) {
		m.page = 0
	}
	spec := pages[m.page]

	if m.comparison == nil {
		b.WriteString(fitLine("no comparison available", width) + "\n")
		return b.String()
	}

	rows, note := m.rowsFor(spec)
	if len(rows) == 0 {
		b.WriteString(fitLine(m.theme.Dim("no comparable "+spec.name+" measurements"), width) + "\n")
	} else {
		fmt.Fprintf(&b, "%s%s\n", m.theme.Header(spec.name), m.theme.Dim(spec.unit))
		if note != "" {
			b.WriteString(fitLine(m.theme.Dim(note), width) + "\n")
		}
		for _, line := range chart.Render(rows, chart.Options{
			// Four columns are reserved so a bar line can never wrap; the
			// chart itself also guarantees it, and this keeps the margin.
			Width: width,
			Chars: m.chars(),
			Scale: 0, // shared across rows, which is what makes them comparable
		}) {
			b.WriteString(fitLine(line, width) + "\n")
		}
	}

	b.WriteString("\n")
	fmt.Fprintf(&b, "%s\n", fitLine(m.theme.Dim(fmt.Sprintf("page %d/%d  (%s)", m.page+1, len(pages), strings.Join(pageNames(), " · "))), width))
	b.WriteString(fitLine(m.theme.Dim("←/→ page  enter finish  q quit"), width))

	return b.String()
}

// rowsFor builds the chart rows for one metric page.
func (m ResultsModel) rowsFor(spec metricSpec) ([]chart.Row, string) {
	var rows []chart.Row
	var skipped []string

	for _, s := range m.comparison.Servers {
		d := spec.get(s)
		if d == nil || !d.Comparable() {
			if d != nil {
				skipped = append(skipped, s.ID)
			}
			continue
		}

		note := fmt.Sprintf("%+.1f%%  %s", d.PctChange, d.Verdict())
		if d.Same() {
			// A change below the noise threshold is reported as the absolute
			// difference, because a percentage of nothing is misleading.
			note = fmt.Sprintf("%+.2f  %s", d.AbsDiff, d.Verdict())
		}
		note = m.colourVerdict(note, d.Verdict())

		rows = append(rows, chart.Row{
			Label: s.ID,
			Series: []chart.Series{
				{Name: "ISP", Value: d.Baseline, Suffix: spec.unit},
				{Name: "WARP", Value: d.Warp, Suffix: spec.unit},
			},
			Note: note,
		})
	}

	note := ""
	if len(skipped) > 0 {
		note = fmt.Sprintf("%d server(s) not measured in both phases: %s",
			len(skipped), strings.Join(skipped, ", "))
	}
	return rows, note
}

// colourVerdict tints a verdict according to the metric's direction, which is
// the only way the colour can be trusted: green means better for this metric.
func (m ResultsModel) colourVerdict(s, verdict string) string {
	switch verdict {
	case "better":
		return m.theme.Better(s)
	case "worse":
		return m.theme.Worse(s)
	default:
		return s
	}
}

func pageNames() []string {
	pages := metricPages()
	out := make([]string, 0, len(pages))
	for _, p := range pages {
		out = append(out, p.name)
	}
	return out
}

func (m ResultsModel) viewWidth() int {
	if m.width <= 0 {
		return 80
	}
	return m.width
}
