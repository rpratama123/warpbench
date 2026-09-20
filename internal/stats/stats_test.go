package stats

import (
	"math"
	"testing"
	"time"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func approx(t *testing.T, got time.Duration, want time.Duration, tolerance time.Duration, label string) {
	t.Helper()
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	if diff > tolerance {
		t.Errorf("%s = %v, want %v (±%v)", label, got, want, tolerance)
	}
}

func TestSummarizeHandComputed(t *testing.T) {
	// Chosen so every statistic is exact by hand:
	//   mean 30ms, deviations -20/-10/0/+10/+20 => variance 200ms^2
	//   consecutive differences all 10ms
	samples := []time.Duration{ms(10), ms(20), ms(30), ms(40), ms(50)}

	got := Summarize(samples, len(samples))

	if got.Sent != 5 || got.Received != 5 {
		t.Errorf("Sent/Received = %d/%d, want 5/5", got.Sent, got.Received)
	}
	if got.LossPct != 0 {
		t.Errorf("LossPct = %v, want 0", got.LossPct)
	}
	if got.Min != ms(10) {
		t.Errorf("Min = %v, want 10ms", got.Min)
	}
	if got.Max != ms(50) {
		t.Errorf("Max = %v, want 50ms", got.Max)
	}
	if got.Avg != ms(30) {
		t.Errorf("Avg = %v, want 30ms", got.Avg)
	}
	if got.Median != ms(30) {
		t.Errorf("Median = %v, want 30ms", got.Median)
	}
	// nearest-rank p95 of 5 samples: ceil(0.95*5) = 5 -> the largest.
	if got.P95 != ms(50) {
		t.Errorf("P95 = %v, want 50ms", got.P95)
	}
	if got.Jitter != ms(10) {
		t.Errorf("Jitter = %v, want 10ms", got.Jitter)
	}
	approx(t, got.StdDev, time.Duration(math.Sqrt(200)*float64(time.Millisecond)), 100*time.Microsecond, "StdDev")
}

func TestSummarizePacketLoss(t *testing.T) {
	got := Summarize([]time.Duration{ms(10), ms(20), ms(30)}, 5)

	if got.Sent != 5 || got.Received != 3 {
		t.Errorf("Sent/Received = %d/%d, want 5/3", got.Sent, got.Received)
	}
	if got.LossPct != 40 {
		t.Errorf("LossPct = %v, want 40", got.LossPct)
	}
}

// A probe run where every packet was lost must report total loss, not a
// division by zero or a misleading zero.
func TestSummarizeTotalLoss(t *testing.T) {
	got := Summarize(nil, 10)

	if got.Sent != 10 || got.Received != 0 {
		t.Errorf("Sent/Received = %d/%d, want 10/0", got.Sent, got.Received)
	}
	if got.LossPct != 100 {
		t.Errorf("LossPct = %v, want 100", got.LossPct)
	}
	if got.Min != 0 || got.Avg != 0 || got.P95 != 0 {
		t.Errorf("stats should be zero with no samples, got %+v", got)
	}
}

// Duplicate replies (received > sent) must not yield negative loss.
func TestSummarizeDuplicateRepliesClampLoss(t *testing.T) {
	got := Summarize([]time.Duration{ms(1), ms(2), ms(3)}, 2)

	if got.LossPct < 0 {
		t.Errorf("LossPct = %v, want non-negative", got.LossPct)
	}
	if got.Sent != 3 {
		t.Errorf("Sent = %d, want it clamped up to Received (3)", got.Sent)
	}
}

func TestSummarizeEmpty(t *testing.T) {
	got := Summarize(nil, 0)

	if got.Sent != 0 || got.Received != 0 || got.LossPct != 0 {
		t.Errorf("empty summary = %+v, want all zero", got)
	}
}

func TestSummarizeDoesNotMutateInput(t *testing.T) {
	samples := []time.Duration{ms(30), ms(10), ms(20)}
	before := append([]time.Duration(nil), samples...)

	_ = Summarize(samples, len(samples))

	for i := range samples {
		if samples[i] != before[i] {
			t.Fatalf("Summarize reordered the caller's slice: %v, want %v", samples, before)
		}
	}
}

func TestPercentileNearestRank(t *testing.T) {
	sorted := []time.Duration{ms(1), ms(2), ms(3), ms(4), ms(5), ms(6), ms(7), ms(8), ms(9), ms(10)}

	tests := map[string]struct {
		p    float64
		want time.Duration
	}{
		"p0":   {0, ms(1)},
		"p50":  {50, ms(5)},
		"p95":  {95, ms(10)},
		"p100": {100, ms(10)},
		"p10":  {10, ms(1)},
		"p99":  {99, ms(10)},
		"p45":  {45, ms(5)},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := PercentileSorted(sorted, tc.p); got != tc.want {
				t.Errorf("PercentileSorted(p=%v) = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}

func TestPercentileEdgeCases(t *testing.T) {
	if got := PercentileSorted(nil, 50); got != 0 {
		t.Errorf("PercentileSorted(nil) = %v, want 0", got)
	}
	if got := Percentile(nil, 95); got != 0 {
		t.Errorf("Percentile(nil) = %v, want 0", got)
	}
	if got := Percentile([]time.Duration{ms(7)}, 95); got != ms(7) {
		t.Errorf("Percentile of one sample = %v, want 7ms", got)
	}
	// Unsorted input must still work.
	if got := Percentile([]time.Duration{ms(30), ms(10), ms(20)}, 50); got != ms(20) {
		t.Errorf("Percentile(unsorted) = %v, want 20ms", got)
	}
}

func TestJitterAndStdDevEdges(t *testing.T) {
	if got := Jitter(nil); got != 0 {
		t.Errorf("Jitter(nil) = %v, want 0", got)
	}
	if got := Jitter([]time.Duration{ms(5)}); got != 0 {
		t.Errorf("Jitter(single) = %v, want 0", got)
	}
	if got := StdDev(nil); got != 0 {
		t.Errorf("StdDev(nil) = %v, want 0", got)
	}
	if got := StdDev([]time.Duration{ms(5)}); got != 0 {
		t.Errorf("StdDev(single) = %v, want 0", got)
	}
}

// Jitter and StdDev are not redundant: which one comes out larger depends on
// the shape of the disturbance, so a report that showed only one of them could
// rank two very different links the wrong way round.
//
// This is the justification for reporting both, so it is pinned down exactly.
func TestJitterAndStdDevRankPatternsDifferently(t *testing.T) {
	// One spike. mean 108ms; variance is exactly 38416ms^2 => StdDev 196ms.
	// Consecutive differences are 0,0,0,490 => Jitter 122.5ms.
	spike := []time.Duration{ms(10), ms(10), ms(10), ms(10), ms(500)}

	if got := Jitter(spike); got != 122500*time.Microsecond {
		t.Errorf("Jitter(spike) = %v, want 122.5ms", got)
	}
	if got := StdDev(spike); got != ms(196) {
		t.Errorf("StdDev(spike) = %v, want exactly 196ms", got)
	}
	if StdDev(spike) <= Jitter(spike) {
		t.Error("for a single spike, StdDev should exceed Jitter")
	}

	// Even oscillation. Jitter is 40ms; variance is 384ms^2 => StdDev ~19.6ms.
	oscillating := []time.Duration{ms(10), ms(50), ms(10), ms(50), ms(10)}

	if got := Jitter(oscillating); got != ms(40) {
		t.Errorf("Jitter(oscillating) = %v, want 40ms", got)
	}
	approx(t, StdDev(oscillating), time.Duration(math.Sqrt(384)*float64(time.Millisecond)), 10*time.Microsecond, "StdDev(oscillating)")
	if Jitter(oscillating) <= StdDev(oscillating) {
		t.Error("for even oscillation, Jitter should exceed StdDev")
	}
}

// A link that oscillates evenly should show high jitter relative to a stable
// one, even when both have the same mean.
func TestJitterDetectsOscillation(t *testing.T) {
	stable := []time.Duration{ms(30), ms(30), ms(30), ms(30)}
	oscillating := []time.Duration{ms(10), ms(50), ms(10), ms(50)}

	if Average(stable) != Average(oscillating) {
		t.Fatalf("fixture is wrong: means differ (%v vs %v)", Average(stable), Average(oscillating))
	}
	if Jitter(oscillating) != ms(40) {
		t.Errorf("Jitter(oscillating) = %v, want 40ms", Jitter(oscillating))
	}
	if Jitter(stable) != 0 {
		t.Errorf("Jitter(stable) = %v, want 0", Jitter(stable))
	}
}

func TestDropFirst(t *testing.T) {
	got := DropFirst([]time.Duration{ms(1), ms(2), ms(3)})
	if len(got) != 2 || got[0] != ms(2) {
		t.Errorf("DropFirst = %v, want [2ms 3ms]", got)
	}
	if got := DropFirst([]time.Duration{ms(1)}); got != nil {
		t.Errorf("DropFirst(single) = %v, want nil", got)
	}
	if got := DropFirst(nil); got != nil {
		t.Errorf("DropFirst(nil) = %v, want nil", got)
	}
}

// 12.5 MB in one second is exactly 100 Mbps in decimal units.
func TestRateMbpsIsDecimal(t *testing.T) {
	got := RateMbps(12_500_000, time.Second)
	if math.Abs(got-100) > 1e-9 {
		t.Errorf("RateMbps(12.5MB, 1s) = %v, want 100", got)
	}

	if got := RateMbps(0, time.Second); got != 0 {
		t.Errorf("RateMbps(0 bytes) = %v, want 0", got)
	}
	if got := RateMbps(1000, 0); got != 0 {
		t.Errorf("RateMbps(zero duration) = %v, want 0", got)
	}
	if got := RateMbps(-1, time.Second); got != 0 {
		t.Errorf("RateMbps(negative) = %v, want 0", got)
	}
}

func TestMedianFloat(t *testing.T) {
	if got := MedianFloat(nil); got != 0 {
		t.Errorf("MedianFloat(nil) = %v, want 0", got)
	}
	if got := MedianFloat([]float64{3, 1, 2}); got != 2 {
		t.Errorf("MedianFloat(odd) = %v, want 2", got)
	}
	if got := MedianFloat([]float64{4, 1, 3, 2}); got != 2.5 {
		t.Errorf("MedianFloat(even) = %v, want 2.5", got)
	}
}

func TestMedianDuration(t *testing.T) {
	if got := MedianDuration(nil); got != 0 {
		t.Errorf("MedianDuration(nil) = %v, want 0", got)
	}
	if got := MedianDuration([]time.Duration{ms(30), ms(10), ms(20)}); got != ms(20) {
		t.Errorf("MedianDuration(odd) = %v, want 20ms", got)
	}
	if got := MedianDuration([]time.Duration{ms(40), ms(10), ms(30), ms(20)}); got != ms(25) {
		t.Errorf("MedianDuration(even) = %v, want 25ms", got)
	}
}

func TestAverageEmpty(t *testing.T) {
	if got := Average(nil); got != 0 {
		t.Errorf("Average(nil) = %v, want 0", got)
	}
}

func TestSortedDoesNotMutate(t *testing.T) {
	in := []time.Duration{ms(3), ms(1), ms(2)}
	_ = Sorted(in)

	if in[0] != ms(3) {
		t.Errorf("Sorted mutated its input: %v", in)
	}
}
