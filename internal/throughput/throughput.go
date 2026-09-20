// Package throughput measures download and upload rates against the protocols
// the server list declares.
//
// One adapter per protocol hides the wire format behind a single interface, so
// the runner, the TUI and the report never learn how a number was obtained.
package throughput

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rpratama123/warpbench/internal/serverlist"
)

// Caps describes what a target can be asked to do. It mirrors the server list's
// capabilities but is derived from the protocol, so a mis-declared entry is
// caught rather than silently skipped.
type Caps struct {
	Ping     bool
	Download bool
	Upload   bool
	Timings  bool
}

// Opts configures one throughput sample.
type Opts struct {
	// Duration is the authoritative measurement window. The byte caps below are
	// guards, not the thing that ends a sample: a byte cap alone would make a
	// fast link's sample far too short to mean anything.
	Duration time.Duration
	// Warmup is excluded from the steady-state figure, which is the headline.
	// TCP slow start is a ramp, not capacity, and averaging it in understates
	// fast links.
	Warmup time.Duration
	// MaxBytes is a runaway guard for the transfer.
	MaxBytes int64
	// MinBytes is the floor below which a sample is flagged as undersized
	// rather than quietly published as a rate.
	MinBytes int64
	// Parallel is the number of concurrent streams. Values above 1 are reported
	// as a separate series and never mixed into the single-stream headline.
	Parallel int
	// UserAgent identifies us to the server.
	UserAgent string
}

// Defaults, reconciled with the run-time budget in PLAN.md section 5.7.
const (
	DefaultDuration = 12 * time.Second
	DefaultWarmup   = 1 * time.Second
	DefaultMaxBytes = 500 << 20
	DefaultMinBytes = 25 << 20
)

func (o Opts) withDefaults() Opts {
	if o.Duration <= 0 {
		o.Duration = DefaultDuration
	}
	if o.Warmup < 0 {
		o.Warmup = 0
	} else if o.Warmup == 0 {
		o.Warmup = DefaultWarmup
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if o.MinBytes <= 0 {
		o.MinBytes = DefaultMinBytes
	}
	if o.Parallel < 1 {
		o.Parallel = 1
	}
	return o
}

// Sample is one measured transfer.
//
// Steady is the headline figure and Overall is reported alongside it so a
// reader can see how much the slow-start window moved the number.
type Sample struct {
	Metric  string
	Bytes   int64
	Elapsed time.Duration
	// SteadyBytes and SteadyFor describe the window the headline rate came
	// from, so the exclusion is auditable rather than a bare number.
	SteadyBytes int64
	SteadyFor   time.Duration

	SteadyMbps  float64
	OverallMbps float64

	Parallel int
	Proto    string
	RemoteIP string

	// Undersized is set when the transfer ended below MinBytes, which usually
	// means the sample was too short to be trustworthy.
	Undersized bool
	// Truncated is set when MaxBytes stopped the transfer before Duration did.
	Truncated bool
	// Partial is set when an error ended the window early after data had
	// already moved. The rate is usable but the window is shorter than
	// requested, which the report must disclose rather than present as a
	// full-length sample.
	Partial bool
}

// Adapter measures one protocol.
type Adapter interface {
	// ID is the protocol name from the server list.
	ID() string
	// Caps reports what this protocol supports.
	Caps() Caps
	// TimingsURL returns a small GET target for httptrace, or "" when the
	// protocol has no HTTP surface to trace.
	TimingsURL() string
	Download(ctx context.Context, o Opts) (Sample, error)
	Upload(ctx context.Context, o Opts) (Sample, error)
}

// ErrNotSupported is returned when an adapter cannot perform the requested
// metric. Callers must render this as "N/A" with the reason, never as zero:
// "could not measure" and "measured zero" are different findings.
type ErrNotSupported struct {
	Protocol string
	Metric   string
}

func (e *ErrNotSupported) Error() string {
	return fmt.Sprintf("%s does not support %s", e.Protocol, e.Metric)
}

// IsNotSupported reports whether err means the metric is unavailable.
func IsNotSupported(err error) bool {
	var target *ErrNotSupported
	return errors.As(err, &target)
}

// Deps carries what adapters need from outside this package.
type Deps struct {
	// Client is used for HTTP protocols. Adapters do not mutate it.
	Client *HTTPClient
	// UserAgent identifies us to the servers.
	UserAgent string
	// IPerf3Bin resolves an executable iperf3, downloading and verifying it on
	// first use. Nil disables the iperf3 adapter.
	IPerf3Bin func(ctx context.Context) (string, error)
}

// New builds the adapter for a server list entry.
func New(server serverlist.Server, deps Deps) (Adapter, error) {
	switch server.Protocol {
	case "http-file":
		return newHTTPFile(server, deps), nil
	case "cloudflare":
		return newCloudflare(server, deps), nil
	case "librespeed":
		return newLibreSpeed(server, deps), nil
	case "iperf3":
		return newIPerf3(server, deps)
	default:
		return nil, fmt.Errorf("no adapter for protocol %q", server.Protocol)
	}
}

// declaredCaps maps the server list's capabilities onto Caps, so an adapter can
// assert it can actually deliver what the list promised.
func declaredCaps(server serverlist.Server) Caps {
	return Caps{
		Ping:     server.Has("ping"),
		Download: server.Has("download"),
		Upload:   server.Has("upload"),
		Timings:  server.Has("timings"),
	}
}

// rate builds a Sample from transfer counters.
func rate(metric string, c counters, o Opts, proto, remoteIP string) Sample {
	s := Sample{
		Metric:      metric,
		Bytes:       c.bytes,
		Elapsed:     c.elapsed,
		SteadyBytes: c.steadyBytes,
		SteadyFor:   c.steadyFor,
		OverallMbps: mbps(c.bytes, c.elapsed),
		SteadyMbps:  mbps(c.steadyBytes, c.steadyFor),
		Parallel:    o.Parallel,
		Proto:       proto,
		RemoteIP:    remoteIP,
		Truncated:   c.truncated,
		Partial:     c.partial,
	}

	// Fall back to the overall figure when there is no usable steady window,
	// rather than reporting zero: a short sample is still better than nothing,
	// and Undersized flags it.
	if s.SteadyFor <= 0 || s.SteadyBytes <= 0 {
		s.SteadyMbps = s.OverallMbps
		s.SteadyBytes = c.bytes
		s.SteadyFor = c.elapsed
		s.Undersized = true
	}

	if c.bytes < o.MinBytes {
		s.Undersized = true
	}
	return s
}

func mbps(bytes int64, d time.Duration) float64 {
	if bytes <= 0 || d <= 0 {
		return 0
	}
	return float64(bytes) * 8 / d.Seconds() / 1e6
}

// client returns the HTTP client to use, defaulting to the standard
// configuration so an adapter can never accidentally run with a zero Deps.
func (d Deps) client() *HTTPClient {
	if d.Client == nil {
		return NewHTTPClient(0)
	}
	return d.Client
}

// measure runs one or more streams of a transfer and turns the counters into a
// Sample. Every adapter routes through here so the single-stream and parallel
// paths cannot drift apart.
func measure(label string, o Opts, one func() (counters, string, string, error)) (Sample, error) {
	if o.Parallel > 1 {
		c, proto, ip, err := parallel(o.Parallel, one)
		if err != nil {
			return Sample{}, err
		}
		return rate(label, c, o, proto, ip), nil
	}

	c, proto, ip, err := one()
	if err != nil {
		return Sample{}, err
	}
	return rate(label, c, o, proto, ip), nil
}

// derefOrEmpty reads an optional URL field.
func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// notSupported builds the error a caller must render as "N/A" rather than zero.
func notSupported(protocol, metric string) error {
	return &ErrNotSupported{Protocol: protocol, Metric: metric}
}
