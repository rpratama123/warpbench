package netprobe

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"time"

	"github.com/rpratama123/warpbench/internal/stats"
)

// Timing is one connection-setup measurement.
//
// A zero DNS or TLS value means the phase did not occur for that request (a
// cached resolver answer, or a plaintext URL), not that it was instantaneous.
// The report must not present a missing phase as a zero-cost one.
type Timing struct {
	DNS        time.Duration
	Connect    time.Duration
	TLS        time.Duration
	TTFB       time.Duration
	Total      time.Duration
	RemoteIP   string
	Proto      string
	TLSVersion string
	Status     int
}

// TimingOptions configures a timings probe.
type TimingOptions struct {
	// Timeout per request.
	Timeout time.Duration
	// UserAgent identifies us to the server.
	UserAgent string
	// ProbeBytes is how much of the body to request, as a Range header. Small:
	// this measures setup cost, not throughput.
	ProbeBytes int64
}

func (o TimingOptions) withDefaults() TimingOptions {
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.ProbeBytes <= 0 {
		o.ProbeBytes = 1024
	}
	return o
}

// Timings measures DNS, TCP connect, TLS handshake and time-to-first-byte for
// one request to url.
//
// Each call forces a fresh connection (Connection: close) so that the setup
// phases are actually measured rather than being skipped by connection reuse.
func Timings(ctx context.Context, url string, client *http.Client, opts TimingOptions) (Timing, error) {
	opts = opts.withDefaults()

	var (
		result       Timing
		dnsStart     time.Time
		connectStart time.Time
		tlsStart     time.Time
		wroteRequest time.Time
	)

	trace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone: func(httptrace.DNSDoneInfo) {
			if !dnsStart.IsZero() {
				result.DNS = time.Since(dnsStart)
			}
		},
		ConnectStart: func(_, _ string) { connectStart = time.Now() },
		ConnectDone: func(_, _ string, err error) {
			if err == nil && !connectStart.IsZero() {
				result.Connect = time.Since(connectStart)
			}
		},
		TLSHandshakeStart: func() { tlsStart = time.Now() },
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			if err != nil {
				return
			}
			if !tlsStart.IsZero() {
				result.TLS = time.Since(tlsStart)
			}
			result.TLSVersion = tlsVersionName(state.Version)
		},
		GotConn: func(info httptrace.GotConnInfo) {
			if info.Conn != nil {
				result.RemoteIP = hostOnly(info.Conn.RemoteAddr().String())
			}
		},
		WroteRequest: func(httptrace.WroteRequestInfo) { wroteRequest = time.Now() },
		GotFirstResponseByte: func() {
			if !wroteRequest.IsZero() {
				result.TTFB = time.Since(wroteRequest)
			}
		},
	}

	reqCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	reqCtx = httptrace.WithClientTrace(reqCtx, trace)

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return Timing{}, err
	}
	// Force a fresh connection so connect/TLS are measured, not reused.
	req.Close = true
	if opts.UserAgent != "" {
		req.Header.Set("User-Agent", opts.UserAgent)
	}
	if opts.ProbeBytes > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", opts.ProbeBytes-1))
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return Timing{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	// Read a bounded amount so the connection can be reused or closed cleanly
	// without pulling a whole payload.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, opts.ProbeBytes))

	result.Total = time.Since(start)
	result.Status = resp.StatusCode
	result.Proto = resp.Proto

	if resp.StatusCode < 200 || resp.StatusCode > 399 {
		return result, fmt.Errorf("unexpected status %s", resp.Status)
	}
	return result, nil
}

// TimingsN runs n timing probes and returns them all, so the caller can take a
// median. Individual probe failures are returned as errors alongside whatever
// successes were collected.
func TimingsN(ctx context.Context, url string, client *http.Client, opts TimingOptions, n int) ([]Timing, []error) {
	if n < 1 {
		n = 1
	}

	results := make([]Timing, 0, n)
	var errs []error

	for range n {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		t, err := Timings(ctx, url, client, opts)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		results = append(results, t)
	}

	return results, errs
}

// MedianTiming returns the element-wise median of a set of timings, which is
// what the report shows.
func MedianTiming(timings []Timing) Timing {
	if len(timings) == 0 {
		return Timing{}
	}
	if len(timings) == 1 {
		return timings[0]
	}

	dns := make([]time.Duration, 0, len(timings))
	connect := make([]time.Duration, 0, len(timings))
	tlsHandshake := make([]time.Duration, 0, len(timings))
	ttfb := make([]time.Duration, 0, len(timings))
	total := make([]time.Duration, 0, len(timings))

	for _, t := range timings {
		dns = append(dns, t.DNS)
		connect = append(connect, t.Connect)
		tlsHandshake = append(tlsHandshake, t.TLS)
		ttfb = append(ttfb, t.TTFB)
		total = append(total, t.Total)
	}

	// The address, protocol and TLS version come from the last sample: if they
	// changed mid-run that is itself worth surfacing, and the caller compares
	// them rather than having a median invented for them.
	last := timings[len(timings)-1]

	return Timing{
		DNS:        stats.MedianDuration(dns),
		Connect:    stats.MedianDuration(connect),
		TLS:        stats.MedianDuration(tlsHandshake),
		TTFB:       stats.MedianDuration(ttfb),
		Total:      stats.MedianDuration(total),
		RemoteIP:   last.RemoteIP,
		Proto:      last.Proto,
		TLSVersion: last.TLSVersion,
		Status:     last.Status,
	}
}

// AddressChanged reports whether any sample contacted a different address than
// the first, which would indicate a route or anycast change mid-run.
func AddressChanged(timings []Timing) bool {
	if len(timings) < 2 {
		return false
	}
	first := timings[0].RemoteIP
	for _, t := range timings[1:] {
		if t.RemoteIP != first {
			return true
		}
	}
	return false
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS1.0"
	case tls.VersionTLS11:
		return "TLS1.1"
	case tls.VersionTLS12:
		return "TLS1.2"
	case tls.VersionTLS13:
		return "TLS1.3"
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}

// hostOnly strips the port from "host:port", tolerating IPv6 literals.
func hostOnly(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
