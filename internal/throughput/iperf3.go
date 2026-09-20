package throughput

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/rpratama123/warpbench/internal/iperf3"
	"github.com/rpratama123/warpbench/internal/serverlist"
	"github.com/rpratama123/warpbench/internal/stats"
)

// iperf3PortAttempts bounds how many ports in the advertised range are tried.
//
// Public iperf3 servers advertise a range precisely because individual ports
// are often busy, so retrying is expected. The bound matters because each
// attempt costs a full test duration.
const iperf3PortAttempts = 3

// iperf3MinDuration is the shortest test worth running: iperf3's -O 1 already
// discards the first second, so anything shorter would measure nothing.
const iperf3MinDuration = 3 * time.Second

// iperf3 measures against a public iperf3 server using the real iperf3 binary.
//
// This protocol carries APAC upload coverage, because the LibreSpeed community
// list has no usable APAC server. Public iperf3 servers are often busy, so every
// result is marked best-effort in the server list and the report.
type iperf3Adapter struct {
	server serverlist.Server
	deps   Deps
}

func newIPerf3(server serverlist.Server, deps Deps) (Adapter, error) {
	if server.IPerf3 == nil {
		return nil, fmt.Errorf("server %s declares protocol iperf3 without an iperf3 block", server.ID)
	}
	if len(server.IPerf3.PortRange) != 2 {
		return nil, fmt.Errorf("server %s has a malformed iperf3 port_range: %v", server.ID, server.IPerf3.PortRange)
	}
	return &iperf3Adapter{server: server, deps: deps}, nil
}

func (a *iperf3Adapter) ID() string { return "iperf3" }

func (a *iperf3Adapter) Caps() Caps {
	return Caps{Ping: true, Download: true, Upload: true, Timings: false}
}

// TimingsURL is empty: iperf3 has no HTTP surface, so httptrace has nothing to
// measure. The server list must not declare the timings capability here, and
// the loader rejects it if it does.
func (a *iperf3Adapter) TimingsURL() string { return "" }

func (a *iperf3Adapter) Download(ctx context.Context, o Opts) (Sample, error) {
	return a.run(ctx, o, true)
}

func (a *iperf3Adapter) Upload(ctx context.Context, o Opts) (Sample, error) {
	return a.run(ctx, o, false)
}

func (a *iperf3Adapter) run(ctx context.Context, o Opts, reverse bool) (Sample, error) {
	metric := "upload"
	if reverse {
		metric = "download"
	}

	if a.deps.IPerf3Bin == nil {
		return Sample{}, notSupported(a.ID(), metric)
	}

	bin, err := a.deps.IPerf3Bin(ctx)
	if err != nil {
		return Sample{}, fmt.Errorf("resolving the iperf3 binary: %w", err)
	}

	o = o.withDefaults()

	var lastErr error
	for _, port := range a.portsToTry() {
		sample, err := a.attempt(ctx, bin, port, o, reverse, metric)
		if err == nil {
			return sample, nil
		}
		lastErr = err

		if ctx.Err() != nil {
			break
		}
	}

	return Sample{}, fmt.Errorf("iperf3 %s against %s failed on every port tried: %w",
		metric, a.server.IPerf3.Host, lastErr)
}

func (a *iperf3Adapter) attempt(ctx context.Context, bin string, port int, o Opts, reverse bool, metric string) (Sample, error) {
	raw, err := iperf3.Run(ctx, bin, a.args(port, o, reverse)...)
	if err != nil {
		return Sample{}, err
	}

	var result iperf3.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return Sample{}, fmt.Errorf("parsing iperf3 JSON: %w", err)
	}
	if result.Error != "" {
		return Sample{}, errors.New(result.Error)
	}

	sum := result.Upload()
	if reverse {
		sum = result.Download()
	}
	if sum.Bytes <= 0 || sum.Seconds <= 0 {
		return Sample{}, errors.New("iperf3 reported no transferred bytes")
	}

	return a.sampleFrom(sum, result.RemoteHost(), o, metric), nil
}

func (a *iperf3Adapter) sampleFrom(sum iperf3.Sum, remoteHost string, o Opts, metric string) Sample {
	elapsed := time.Duration(sum.Seconds * float64(time.Second))
	rateMbps := stats.RateMbps(sum.Bytes, elapsed)

	// iperf3's -O flag performs the slow-start exclusion inside the tool, so its
	// reported window is already steady-state and the two figures coincide.
	// Keeping both fields populated means downstream code needs no special case.
	return Sample{
		Metric:      metric,
		Bytes:       sum.Bytes,
		Elapsed:     elapsed,
		SteadyBytes: sum.Bytes,
		SteadyFor:   elapsed,
		SteadyMbps:  rateMbps,
		OverallMbps: rateMbps,
		Parallel:    o.Parallel,
		Proto:       "iperf3",
		RemoteIP:    remoteHost,
		Undersized:  float64(sum.Bytes) < float64(o.MinBytes),
	}
}

func (a *iperf3Adapter) args(port int, o Opts, reverse bool) []string {
	duration := o.Duration
	if duration < iperf3MinDuration {
		duration = iperf3MinDuration
	}

	args := []string{
		"-c", a.server.IPerf3.Host,
		"-p", strconv.Itoa(port),
		"-t", strconv.Itoa(int(duration.Seconds())),
		"-J",
		"--connect-timeout", "5000",
	}

	// iperf3's native equivalent of the warm-up trim the HTTP adapters do.
	if duration >= 2*time.Second {
		args = append(args, "-O", "1")
	}
	if reverse {
		args = append(args, "-R")
	}
	if o.Parallel > 1 {
		args = append(args, "-P", strconv.Itoa(o.Parallel))
	}

	return args
}

// portsToTry walks the advertised range from its lower bound.
func (a *iperf3Adapter) portsToTry() []int {
	low, high := a.server.IPerf3.PortRange[0], a.server.IPerf3.PortRange[1]
	if low > high {
		low, high = high, low
	}

	var ports []int
	for p := low; p <= high && len(ports) < iperf3PortAttempts; p++ {
		ports = append(ports, p)
	}
	return ports
}
