package results

import (
	"fmt"
	"math"
	"time"

	"github.com/rpratama123/warpbench/internal/stats"
)

// Direction says which way is better for a metric. It exists because a
// percentage change is meaningless without it: +138% download is an
// improvement, +138% latency is a disaster, and a renderer that coloured both
// green would be actively misleading.
type Direction int

const (
	// LowerIsBetter applies to latency, jitter, loss and setup timings.
	LowerIsBetter Direction = iota
	// HigherIsBetter applies to throughput.
	HigherIsBetter
)

// Better reports whether the change from baseline to warp is an improvement.
func (d Direction) Better(baseline, warp float64) bool {
	if d == HigherIsBetter {
		return warp > baseline
	}
	return warp < baseline
}

func (d Direction) String() string {
	if d == HigherIsBetter {
		return "higher-is-better"
	}
	return "lower-is-better"
}

// Delta is one metric's change between phases.
type Delta struct {
	Metric    string
	Direction Direction
	Baseline  float64
	Warp      float64

	// HasBaseline and HasWarp record which sides were actually measured. A
	// metric present in one phase only is an asymmetry worth reporting, not a
	// zero to be subtracted.
	HasBaseline bool
	HasWarp     bool

	AbsDiff   float64
	PctChange float64

	// Note explains why a delta is missing or unsafe to compare.
	Note string
}

// Comparable reports whether a delta can be computed at all.
func (d Delta) Comparable() bool { return d.HasBaseline && d.HasWarp }

// Improved reports whether WARP was better. Only meaningful when Comparable.
func (d Delta) Improved() bool {
	if !d.Comparable() {
		return false
	}
	return d.Direction.Better(d.Baseline, d.Warp)
}

// SameEpsilonPct is the relative change below which a difference is treated as
// noise rather than a finding.
//
// Without this a 0.01 ms latency change is reported as a regression, and a
// published report that calls noise a regression is not worth reading.
const SameEpsilonPct = 0.5

// Same reports whether the change is too small to call a difference.
func (d Delta) Same() bool {
	if !d.Comparable() {
		return false
	}
	if d.AbsDiff == 0 {
		return true
	}
	// Loss is measured in percentage points, where a relative comparison would
	// be meaningless at small values.
	if d.Metric == "loss" {
		return math.Abs(d.AbsDiff) < 0.05
	}
	return math.Abs(d.PctChange) < SameEpsilonPct
}

// Verdict renders the comparison in one word.
//
// The summary and the per-server table both read this, so they cannot disagree
// about what counts as an improvement.
func (d Delta) Verdict() string {
	switch {
	case !d.Comparable():
		return "n/a"
	case d.Same():
		return "same"
	case d.Improved():
		return "better"
	default:
		return "worse"
	}
}

// computeDelta fills in the derived fields.
func computeDelta(metric string, dir Direction, baseline float64, hasBaseline bool, warp float64, hasWarp bool) Delta {
	d := Delta{
		Metric:      metric,
		Direction:   dir,
		Baseline:    baseline,
		Warp:        warp,
		HasBaseline: hasBaseline,
		HasWarp:     hasWarp,
	}

	switch {
	case !hasBaseline && !hasWarp:
		d.Note = "not measured in either phase"
		return d
	case !hasBaseline:
		d.Note = "not measured on the ISP path"
		return d
	case !hasWarp:
		d.Note = "not measured over WARP"
		return d
	}

	d.AbsDiff = warp - baseline
	if baseline != 0 {
		d.PctChange = (warp - baseline) / baseline * 100
	} else {
		// A zero baseline makes a percentage meaningless; the absolute change
		// still carries information.
		d.Note = "baseline was zero, so no percentage change is meaningful"
	}
	return d
}

// ServerDelta is one server's comparison.
type ServerDelta struct {
	ID       string
	Name     string
	Group    string
	Protocol string

	Download *Delta
	Upload   *Delta
	Latency  *Delta
	Jitter   *Delta
	Loss     *Delta
	TTFB     *Delta

	// Notes records per-server asymmetries: a metric that failed in one phase,
	// a warning that appeared in only one, an address that moved.
	Notes []string
}

// Summary aggregates the comparison into the headline sentences.
type Summary struct {
	Download ServerTally
	Upload   ServerTally
	Latency  ServerTally
	Jitter   ServerTally
	Loss     ServerTally
}

// ServerTally counts how many servers improved, and by how much at the median.
type ServerTally struct {
	Improved int
	Total    int
	// MedianPct is the median percentage change across comparable servers.
	MedianPct float64
	// MedianAbs is the median absolute change, in the metric's own unit.
	MedianAbs float64
}

// Comparison is the whole comparison.
type Comparison struct {
	Baseline *File
	Warp     *File

	// Servers is ordered by the baseline file's order, which is the server
	// list's canonical order.
	Servers  []ServerDelta
	Summary  Summary
	Warnings []string
}

// CompareOptions configures Compare.
type CompareOptions struct {
	// Force allows comparing files measured against different server-list
	// revisions. Without it that is an error, because a list change invalidates
	// a like-for-like comparison.
	Force bool
}

// GapWarning is the threshold past which the two phases are far enough apart
// that time-of-day drift is a plausible explanation for any difference.
const GapWarning = 10 * time.Minute

// Compare pairs two phase files.
//
// The two files must be different phases: comparing a file with itself, or two
// files from the same phase, cannot mean anything and is rejected rather than
// producing a confident-looking table of noise.
func Compare(baseline, warp *File, opts CompareOptions) (*Comparison, error) {
	if baseline == nil || warp == nil {
		return nil, fmt.Errorf("two result files are required")
	}
	if baseline.Phase == warp.Phase {
		return nil, fmt.Errorf("both files are phase %q; compare a baseline file with a warp file", baseline.Phase)
	}

	c := &Comparison{Baseline: baseline, Warp: warp}

	if baseline.ServerList.Revision != warp.ServerList.Revision {
		msg := fmt.Sprintf("the two phases used different server-list revisions (%s and %s), so the targets may differ",
			baseline.ServerList.Revision, warp.ServerList.Revision)
		if !opts.Force {
			return nil, fmt.Errorf("%s; re-run both phases against one revision, or pass --force to compare anyway", msg)
		}
		c.Warnings = append(c.Warnings, msg+" (forced)")
	}

	if gap := warp.StartedAt.Sub(baseline.EndedAt); gap > GapWarning {
		c.Warnings = append(c.Warnings, fmt.Sprintf(
			"phases are %s apart, so any difference may partly reflect time-of-day variation rather than WARP",
			gap.Round(time.Minute)))
	}

	if len(warp.Servers) != len(baseline.Servers) {
		c.Warnings = append(c.Warnings, fmt.Sprintf(
			"the phases measured %d and %d servers", len(baseline.Servers), len(warp.Servers)))
	}

	// Order follows the baseline file, which the runner wrote in the server
	// list's canonical order.
	warpByID := warp.ServerByID()
	for _, base := range baseline.Servers {
		other, ok := warpByID[base.ID]
		if !ok {
			c.Warnings = append(c.Warnings, fmt.Sprintf("%s was measured on the ISP path but not over WARP", base.ID))
			c.Servers = append(c.Servers, deltaFor(base, Server{}, false))
			continue
		}
		c.Servers = append(c.Servers, deltaFor(base, other, true))
	}

	// A server present only in the warp phase is worth naming too.
	baseByID := baseline.ServerByID()
	for _, s := range warp.Servers {
		if _, ok := baseByID[s.ID]; !ok {
			c.Warnings = append(c.Warnings, fmt.Sprintf("%s was measured over WARP but not on the ISP path", s.ID))
		}
	}

	c.Summary = summarize(c.Servers)
	return c, nil
}

func deltaFor(base, other Server, haveOther bool) ServerDelta {
	d := ServerDelta{
		ID:       base.ID,
		Name:     base.Name,
		Group:    base.Group,
		Protocol: base.Protocol,
	}

	if !haveOther {
		// Everything is baseline-only; the deltas will say so individually.
		d.Download = throughputDelta("download", base.Download, nil)
		d.Upload = throughputDelta("upload", base.Upload, nil)
		d.Latency = latencyDelta(base.Ping, nil)
		d.Jitter = jitterDelta(base.Ping, nil)
		d.Loss = lossDelta(base.Ping, nil)
		d.TTFB = ttfbDelta(base.Timings, nil)
		d.Notes = append(d.Notes, "not present in the WARP phase")
		return d
	}

	d.Download = throughputDelta("download", base.Download, other.Download)
	d.Upload = throughputDelta("upload", base.Upload, other.Upload)
	d.Latency = latencyDelta(base.Ping, other.Ping)
	d.Jitter = jitterDelta(base.Ping, other.Ping)
	d.Loss = lossDelta(base.Ping, other.Ping)
	d.TTFB = ttfbDelta(base.Timings, other.Timings)

	// Asymmetric failures are themselves a result about WARP, so they are
	// reported rather than dropped.
	for _, m := range []struct {
		name string
		a, b *Series
	}{
		{"download", base.Download, other.Download},
		{"upload", base.Upload, other.Upload},
	} {
		if (m.a == nil) != (m.b == nil) {
			d.Notes = append(d.Notes, fmt.Sprintf("%s measured in only one phase", m.name))
		}
	}

	if len(other.Warnings) > 0 && len(base.Warnings) == 0 {
		d.Notes = append(d.Notes, "warnings appeared only over WARP")
	}
	if base.ResolvedIP != "" && other.ResolvedIP != "" && base.ResolvedIP != other.ResolvedIP {
		d.Notes = append(d.Notes, fmt.Sprintf("resolved address changed: %s to %s", base.ResolvedIP, other.ResolvedIP))
	}

	return d
}

// throughputDelta compares one direction. Both download and upload are
// higher-is-better.
func throughputDelta(metric string, a, b *Series) *Delta {
	var (
		av, bv     float64
		hasA, hasB bool
	)

	if a != nil && len(a.Samples) > 0 {
		av, hasA = a.MedianSteadyMbps, true
	}
	if b != nil && len(b.Samples) > 0 {
		bv, hasB = b.MedianSteadyMbps, true
	}

	d := computeDelta(metric, HigherIsBetter, av, hasA, bv, hasB)
	return &d
}

func latencyDelta(a, b *Ping) *Delta {
	d := computeDelta("latency_avg", LowerIsBetter, pingAvg(a), a != nil, pingAvg(b), b != nil)
	return &d
}

func jitterDelta(a, b *Ping) *Delta {
	return deltaPtr(computeDelta("jitter", LowerIsBetter, pingJitter(a), a != nil, pingJitter(b), b != nil))
}

func lossDelta(a, b *Ping) *Delta {
	return deltaPtr(computeDelta("loss", LowerIsBetter, pingLoss(a), a != nil, pingLoss(b), b != nil))
}

func ttfbDelta(a, b *Timings) *Delta {
	return deltaPtr(computeDelta("ttfb", LowerIsBetter, timingsTTFB(a), a != nil, timingsTTFB(b), b != nil))
}

func deltaPtr(d Delta) *Delta { return &d }

func pingAvg(p *Ping) float64 {
	if p == nil {
		return 0
	}
	return p.AvgMs
}

func pingJitter(p *Ping) float64 {
	if p == nil {
		return 0
	}
	return p.JitterMs
}

func pingLoss(p *Ping) float64 {
	if p == nil {
		return 0
	}
	return p.LossPct
}

func timingsTTFB(t *Timings) float64 {
	if t == nil {
		return 0
	}
	return t.TTFBMs
}

// summarize turns per-server deltas into the headline counts.
func summarize(servers []ServerDelta) Summary {
	pick := func(get func(ServerDelta) *Delta) ServerTally {
		var (
			pcts []float64
			abs  []float64
			t    ServerTally
		)
		for _, s := range servers {
			d := get(s)
			if d == nil || !d.Comparable() {
				continue
			}
			t.Total++
			// Counted through Verdict so the headline cannot claim an
			// improvement the table is calling noise.
			if d.Verdict() == "better" {
				t.Improved++
			}
			pcts = append(pcts, d.PctChange)
			abs = append(abs, d.AbsDiff)
		}
		t.MedianPct = stats.MedianFloat(pcts)
		t.MedianAbs = stats.MedianFloat(abs)
		return t
	}

	return Summary{
		Download: pick(func(s ServerDelta) *Delta { return s.Download }),
		Upload:   pick(func(s ServerDelta) *Delta { return s.Upload }),
		Latency:  pick(func(s ServerDelta) *Delta { return s.Latency }),
		Jitter:   pick(func(s ServerDelta) *Delta { return s.Jitter }),
		Loss:     pick(func(s ServerDelta) *Delta { return s.Loss }),
	}
}

// Headline renders the summary sentence the report leads with.
func (s Summary) Headline() string {
	describe := func(label string, t ServerTally) string {
		if t.Total == 0 {
			return ""
		}
		sign := "+"
		if t.MedianPct < 0 {
			sign = ""
		}
		return fmt.Sprintf("%s on %d/%d servers (median %s%.0f%%)",
			label, t.Improved, t.Total, sign, t.MedianPct)
	}

	parts := []string{}
	if p := describe("download improved", s.Download); p != "" {
		parts = append(parts, p)
	}
	if p := describe("upload improved", s.Upload); p != "" {
		parts = append(parts, p)
	}
	if s.Latency.Total > 0 {
		parts = append(parts, fmt.Sprintf("latency changed by a median of %+.1f ms", s.Latency.MedianAbs))
	}
	if s.Jitter.Total > 0 {
		parts = append(parts, fmt.Sprintf("jitter improved on %d/%d", s.Jitter.Improved, s.Jitter.Total))
	}
	return joinSentences(parts)
}

func joinSentences(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	if out == "" {
		return "no comparable measurements"
	}
	return out
}

// Round1 rounds to one decimal place, for stable rendering.
func Round1(v float64) float64 { return math.Round(v*10) / 10 }
