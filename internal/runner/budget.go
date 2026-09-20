package runner

import (
	"time"

	"github.com/rpratama123/warpbench/internal/serverlist"
	"github.com/rpratama123/warpbench/internal/throughput"
)

// Mode selects how much measurement to do.
type Mode string

const (
	ModeQuick    Mode = "quick"
	ModeExtended Mode = "extended"
	ModeCustom   Mode = "custom"
)

// timingSampleCost is what one httptrace sample is expected to cost. Setup
// against an international host is dominated by TCP and TLS round trips, so a
// second per sample is a deliberately conservative allowance.
const timingSampleCost = 1 * time.Second

// Budget is the per-mode measurement budget.
//
// It is the single source of truth for both what the runner executes and the
// duration it advertises before starting, so the estimate a user sees cannot
// drift from what the run then does.
type Budget struct {
	PingCount        int
	PingInterval     time.Duration
	PingTimeout      time.Duration
	TimingSamples    int
	DownloadSamples  int
	UploadSamples    int
	DownloadDuration time.Duration
	UploadDuration   time.Duration
	Warmup           time.Duration
	MinBytes         int64
	MaxBytes         int64
	Parallel         int
}

// Quick and extended budgets, reconciled with the run-time targets in PLAN.md
// section 5.7: quick aims at single-digit minutes for both phases, extended at
// a genuinely thorough run.
var (
	quickBudget = Budget{
		PingCount:        10,
		PingInterval:     200 * time.Millisecond,
		PingTimeout:      2 * time.Second,
		TimingSamples:    3,
		DownloadSamples:  1,
		UploadSamples:    1,
		DownloadDuration: 12 * time.Second,
		UploadDuration:   8 * time.Second,
		Warmup:           1 * time.Second,
		MinBytes:         25 << 20,
		MaxBytes:         500 << 20,
		Parallel:         1,
	}

	extendedBudget = Budget{
		PingCount:        30,
		PingInterval:     200 * time.Millisecond,
		PingTimeout:      2 * time.Second,
		TimingSamples:    7,
		DownloadSamples:  3,
		UploadSamples:    3,
		DownloadDuration: 15 * time.Second,
		UploadDuration:   10 * time.Second,
		Warmup:           1 * time.Second,
		MinBytes:         25 << 20,
		MaxBytes:         500 << 20,
		Parallel:         1,
	}
)

// BudgetFor returns the budget for a mode. Custom mode starts from quick and is
// expected to be adjusted by the caller.
func BudgetFor(mode Mode) Budget {
	switch mode {
	case ModeExtended:
		return extendedBudget
	default:
		return quickBudget
	}
}

// WithParallel returns a copy configured for a stream count.
func (b Budget) WithParallel(n int) Budget {
	if n < 1 {
		n = 1
	}
	b.Parallel = n
	return b
}

// PerServer estimates the time one server will take.
//
// It uses the capabilities the server list declares rather than asking an
// adapter, so the estimate available before a run matches the selection the
// user is looking at.
func (b Budget) PerServer(s serverlist.Server) time.Duration {
	total := time.Duration(b.PingCount)*b.PingInterval + b.PingTimeout

	if s.Has("timings") {
		total += time.Duration(b.TimingSamples) * timingSampleCost
	}
	if s.Has("download") {
		total += time.Duration(b.DownloadSamples) * b.DownloadDuration
	}
	if s.Has("upload") {
		total += time.Duration(b.UploadSamples) * b.UploadDuration
	}
	return total
}

// Estimate returns how long one phase over these servers is expected to take.
//
// The runner never publishes a hard-coded total: a selection of 30 servers is
// legitimately an hour-long run, and claiming otherwise would set a user up to
// abandon a half-finished measurement.
func Estimate(servers []serverlist.Server, b Budget) time.Duration {
	var total time.Duration
	for _, s := range servers {
		total += b.PerServer(s)
	}
	return total
}

// downloadOpts maps the budget onto the adapter's options.
func (b Budget) downloadOpts(userAgent string) throughput.Opts {
	return throughput.Opts{
		Duration:  b.DownloadDuration,
		Warmup:    b.Warmup,
		MaxBytes:  b.MaxBytes,
		MinBytes:  b.MinBytes,
		Parallel:  b.Parallel,
		UserAgent: userAgent,
	}
}

func (b Budget) uploadOpts(userAgent string) throughput.Opts {
	return throughput.Opts{
		Duration:  b.UploadDuration,
		Warmup:    b.Warmup,
		MaxBytes:  b.MaxBytes,
		MinBytes:  b.MinBytes,
		Parallel:  b.Parallel,
		UserAgent: userAgent,
	}
}
