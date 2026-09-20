// Package stats summarizes latency and throughput samples.
//
// Everything here is a pure function over a sample slice: no clock, no network,
// no I/O. That keeps the arithmetic that a published result depends on easy to
// test and easy to argue about.
package stats

import (
	"math"
	"sort"
	"time"
)

// Summary describes a set of latency probes.
type Summary struct {
	// Sent is how many probes were attempted and Received how many produced a
	// sample, so LossPct is meaningful even when every probe was lost.
	Sent     int
	Received int
	LossPct  float64

	Min    time.Duration
	Avg    time.Duration
	Median time.Duration
	Max    time.Duration
	P95    time.Duration
	StdDev time.Duration

	// Jitter is the mean absolute difference between consecutive samples, in
	// the style of RFC 3550's interarrival jitter. Reported alongside StdDev
	// because the two disagree in an informative way: a link with one large
	// spike has high StdDev but low jitter, and a link that oscillates evenly
	// has high jitter but modest StdDev.
	Jitter time.Duration
}

// Summarize computes a Summary over samples. sent is the number of probes
// attempted; pass len(samples) when every probe succeeded.
//
// The input slice is not modified.
func Summarize(samples []time.Duration, sent int) Summary {
	received := len(samples)

	sum := Summary{Sent: sent, Received: received}
	if sent < received {
		// Duplicate replies must not produce a negative loss figure.
		sum.Sent = received
	}
	if sum.Sent > 0 {
		sum.LossPct = float64(sum.Sent-received) / float64(sum.Sent) * 100
	}
	if received == 0 {
		return sum
	}

	sorted := Sorted(samples)
	sum.Min = sorted[0]
	sum.Max = sorted[len(sorted)-1]
	sum.Median = PercentileSorted(sorted, 50)
	sum.P95 = PercentileSorted(sorted, 95)
	sum.StdDev = StdDev(samples)
	sum.Jitter = Jitter(samples)
	sum.Avg = Average(samples)

	return sum
}

// Sorted returns a sorted copy of samples.
func Sorted(samples []time.Duration) []time.Duration {
	out := append([]time.Duration(nil), samples...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Average returns the arithmetic mean, or 0 for an empty slice.
func Average(samples []time.Duration) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	var total time.Duration
	for _, s := range samples {
		total += s
	}
	return total / time.Duration(len(samples))
}

// Percentile returns the nearest-rank percentile of samples, sorting a copy
// first. Nearest rank is used rather than interpolation because it can only
// ever return an actually-observed latency, which is what a reader expects from
// a p95 on a small sample count.
func Percentile(samples []time.Duration, p float64) time.Duration {
	return PercentileSorted(Sorted(samples), p)
}

// PercentileSorted is Percentile for an already-sorted slice.
func PercentileSorted(sorted []time.Duration, p float64) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	switch {
	case p <= 0:
		return sorted[0]
	case p >= 100:
		return sorted[n-1]
	}

	rank := int(math.Ceil(p / 100 * float64(n)))
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

// Jitter returns the mean absolute difference between consecutive samples.
func Jitter(samples []time.Duration) time.Duration {
	if len(samples) < 2 {
		return 0
	}
	var total time.Duration
	for i := 1; i < len(samples); i++ {
		d := samples[i] - samples[i-1]
		if d < 0 {
			d = -d
		}
		total += d
	}
	return total / time.Duration(len(samples)-1)
}

// StdDev returns the population standard deviation.
//
// Population rather than sample: these values are the entire measurement
// window, not a sample drawn from a larger population, so dividing by n is the
// honest choice.
func StdDev(samples []time.Duration) time.Duration {
	n := len(samples)
	if n < 2 {
		return 0
	}

	mean := float64(Average(samples))
	var sumSquares float64
	for _, s := range samples {
		d := float64(s) - mean
		sumSquares += d * d
	}
	return time.Duration(math.Sqrt(sumSquares / float64(n)))
}

// DropFirst returns samples without the first element. The first ICMP reply
// includes cold-path costs (ARP, route lookup, first-hop queueing) that later
// replies do not, so it is discarded per the methodology.
func DropFirst(samples []time.Duration) []time.Duration {
	if len(samples) < 2 {
		return nil
	}
	return samples[1:]
}

// RateMbps converts a byte count over a duration to megabits per second.
//
// 1e6, not 2^20: network rates are decimal, and a "100 Mbps" link that reports
// 104.9 Mbps because of binary megabits would look like a measurement error.
func RateMbps(bytes int64, d time.Duration) float64 {
	if bytes <= 0 || d <= 0 {
		return 0
	}
	return float64(bytes) * 8 / d.Seconds() / 1e6
}

// MedianFloat returns the median of values, or 0 for an empty slice.
func MedianFloat(values []float64) float64 {
	n := len(values)
	if n == 0 {
		return 0
	}

	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)

	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// MedianDuration returns the median of values, or 0 for an empty slice.
func MedianDuration(values []time.Duration) time.Duration {
	n := len(values)
	if n == 0 {
		return 0
	}

	sorted := Sorted(values)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
