package main

import (
	"fmt"
	"io"

	"github.com/rpratama123/warpbench/internal/results"
)

// runCompare loads two phase files and prints a side-by-side comparison.
//
// The Markdown report arrives in Phase 7; this is the plain, pasteable form.
func runCompare(opts options, stdout, stderr io.Writer) int {
	if len(opts.positional) != 2 {
		writef(stderr, "warpbench: --compare needs exactly two result files\n")
		return exitUsage
	}
	if opts.jsonOut {
		// The comparison is not a result file, so --json has nothing valid to
		// emit here; saying so beats printing something unexpected.
		writef(stderr, "warpbench: --json applies to a measurement run, not to --compare\n")
		return exitUsage
	}

	first, err := results.Load(opts.positional[0])
	if err != nil {
		writef(stderr, "warpbench: %v\n", err)
		return exitError
	}
	second, err := results.Load(opts.positional[1])
	if err != nil {
		writef(stderr, "warpbench: %v\n", err)
		return exitError
	}

	// Accept the files in either order: a user who passes the warp file first
	// means the same comparison, not an error.
	baseline, warp := first, second
	if first.Phase == "warp" && second.Phase == "baseline" {
		baseline, warp = second, first
	}

	cmp, err := results.Compare(baseline, warp, results.CompareOptions{Force: opts.force})
	if err != nil {
		writef(stderr, "warpbench: %v\n", err)
		return exitError
	}

	printComparison(stdout, cmp)
	return exitOK
}

func printComparison(w io.Writer, c *results.Comparison) {
	writef(w, "warpbench comparison\n")
	writef(w, "  baseline  %s  %s  (list %s)\n", c.Baseline.StartedAt.Format("2006-01-02 15:04"), shortVersion(c.Baseline), c.Baseline.ServerList.Revision)
	writef(w, "  warp      %s  %s  (list %s)\n", c.Warp.StartedAt.Format("2006-01-02 15:04"), shortVersion(c.Warp), c.Warp.ServerList.Revision)

	for _, t := range []struct {
		label string
		file  *results.File
	}{
		{"baseline", c.Baseline},
		{"warp", c.Warp},
	} {
		for _, tr := range t.file.Traces {
			writef(w, "  trace %-8s %-16s warp=%s colo=%s ip=%s\n", t.label, tr.Stage, tr.Warp, tr.Colo, tr.IP)
		}
	}

	for _, warning := range c.Warnings {
		writef(w, "\n  WARNING %s\n", warning)
	}

	writef(w, "\n%s\n", c.Summary.Headline())

	sections := []struct {
		title string
		pick  func(results.ServerDelta) *results.Delta
		unit  string
	}{
		{"download (Mbps)", func(s results.ServerDelta) *results.Delta { return s.Download }, "%.2f"},
		{"upload (Mbps)", func(s results.ServerDelta) *results.Delta { return s.Upload }, "%.2f"},
		{"latency avg (ms)", func(s results.ServerDelta) *results.Delta { return s.Latency }, "%.1f"},
		{"jitter (ms)", func(s results.ServerDelta) *results.Delta { return s.Jitter }, "%.1f"},
		{"loss (%)", func(s results.ServerDelta) *results.Delta { return s.Loss }, "%.2f"},
		{"ttfb (ms)", func(s results.ServerDelta) *results.Delta { return s.TTFB }, "%.1f"},
	}

	for _, section := range sections {
		rows := 0
		for _, s := range c.Servers {
			if d := section.pick(s); d != nil && d.Comparable() {
				rows++
			}
		}
		if rows == 0 {
			continue
		}

		writef(w, "\n%s\n", section.title)
		writef(w, "  %-24s %9s %9s %10s  %s\n", "server", "ISP", "WARP", "delta", "verdict")

		for _, s := range c.Servers {
			d := section.pick(s)
			if d == nil || !d.Comparable() {
				continue
			}

			// A percentage of a zero baseline is meaningless, so the absolute
			// change is shown instead.
			delta := fmt.Sprintf("%+.1f%%", d.PctChange)
			if d.Baseline == 0 {
				delta = fmt.Sprintf("%+.2f", d.AbsDiff)
			}

			writef(w, "  %-24s %9s %9s %10s  %s\n",
				truncate(s.ID, 24),
				fmt.Sprintf(section.unit, d.Baseline),
				fmt.Sprintf(section.unit, d.Warp),
				delta,
				d.Verdict())
		}
	}

	// Per-server notes explain asymmetries the tables above cannot express.
	var notes []string
	for _, s := range c.Servers {
		for _, n := range s.Notes {
			notes = append(notes, fmt.Sprintf("%s: %s", s.ID, n))
		}
	}
	if len(notes) > 0 {
		writef(w, "\nnotes\n")
		for _, n := range notes {
			writef(w, "  %s\n", n)
		}
	}

	if c.Baseline.Configuration.Masked {
		writef(w, "\nPublic IPs are masked (--no-mask disables this).\n")
	}
}

func shortVersion(f *results.File) string {
	if f.Tool.Version == "" {
		return "unknown"
	}
	return f.Tool.Version
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
