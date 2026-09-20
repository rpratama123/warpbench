// Package netprobe measures latency and connection-setup timings.
//
// Latency is reported with the method that produced it. An ICMP round trip and
// a TCP-connect round trip are different quantities measured against different
// layers, and presenting one as the other would silently invalidate a
// comparison, so the method travels with every result.
package netprobe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

// Method identifies how latency was measured.
type Method string

const (
	// MethodICMP is an ICMP echo round trip.
	MethodICMP Method = "icmp"
	// MethodTCP is a TCP connect round trip, used when ICMP is unavailable.
	MethodTCP Method = "tcp"
)

// PingResult is one latency run against one host.
type PingResult struct {
	Method Method
	Target string
	// Addr is the address actually contacted, so a route or anycast change
	// between phases is visible.
	Addr string
	// Sent is probes attempted and RTTs the replies received. Sent > len(RTTs)
	// means loss.
	Sent int
	RTTs []time.Duration
	// Warning explains a substitution or a degraded measurement. It belongs in
	// the report, not just the log.
	Warning string
}

// PingOptions configures Ping. Zero values take the methodology defaults.
type PingOptions struct {
	// Count is how many probes to attempt. 10 quick, 30 extended.
	Count int
	// Interval between probes; 200ms per the methodology.
	Interval time.Duration
	// Timeout is the per-probe timeout; 2s per the methodology.
	Timeout time.Duration
	// TCPPort is the TCP-connect fallback port. 443 by default.
	TCPPort int
}

func (o PingOptions) withDefaults() PingOptions {
	if o.Count <= 0 {
		o.Count = 10
	}
	if o.Interval <= 0 {
		o.Interval = 200 * time.Millisecond
	}
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Second
	}
	if o.TCPPort == 0 {
		o.TCPPort = 443
	}
	return o
}

// pingICMPFunc is a variable so tests can exercise the fallback path without
// depending on whether the test host permits unprivileged ICMP.
var pingICMPFunc = pingICMP

// Ping measures latency to host, preferring ICMP and falling back to TCP
// connect when ICMP is unavailable.
//
// It returns an error only when neither method could produce a result. A run
// where every probe was lost is a successful measurement of 100% loss, not an
// error, because that is a real and publishable finding.
func Ping(ctx context.Context, host string, opts PingOptions) (*PingResult, error) {
	opts = opts.withDefaults()

	res, icmpErr := pingICMPFunc(ctx, host, opts)
	if icmpErr == nil {
		return res, nil
	}

	tcpRes, tcpErr := pingTCP(ctx, host, opts)
	if tcpErr != nil {
		// Both errors are wrapped: Go 1.20+ allows multiple %w verbs, and a
		// caller should be able to inspect either failure.
		return nil, fmt.Errorf("ICMP ping failed (%w) and the TCP fallback also failed: %w", icmpErr, tcpErr)
	}

	tcpRes.Warning = fmt.Sprintf(
		"ICMP unavailable (%v), so latency is TCP connect time to port %d. That is a different quantity from ICMP RTT and must not be compared with an ICMP result.",
		icmpErr, opts.TCPPort)
	if hint := PingCapabilityHint(); hint != "" {
		tcpRes.Warning += " " + hint
	}
	return tcpRes, nil
}

func pingICMP(ctx context.Context, host string, opts PingOptions) (*PingResult, error) {
	pinger, err := probing.NewPinger(host)
	if err != nil {
		return nil, err
	}

	// Unprivileged UDP-ICMP where the OS allows it; this is what keeps warpbench
	// free of any root requirement.
	pinger.SetPrivileged(false)
	pinger.Count = opts.Count
	pinger.Interval = opts.Interval
	pinger.RecordRtts = true
	// Overall deadline: the send window plus one per-probe timeout for the last
	// reply, plus slack for scheduling.
	pinger.Timeout = time.Duration(opts.Count)*opts.Interval + opts.Timeout + time.Second

	if err := pinger.RunWithContext(ctx); err != nil {
		return nil, err
	}

	stats := pinger.Statistics()
	if stats.PacketsSent == 0 {
		return nil, errors.New("no ICMP probes were sent")
	}

	addr := host
	if stats.IPAddr != nil {
		addr = stats.IPAddr.String()
	}

	return &PingResult{
		Method: MethodICMP,
		Target: host,
		Addr:   addr,
		Sent:   stats.PacketsSent,
		RTTs:   stats.Rtts,
	}, nil
}

func pingTCP(ctx context.Context, host string, opts PingOptions) (*PingResult, error) {
	// Resolve first so that a DNS failure is reported as such instead of
	// looking like 100% packet loss.
	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("resolving %s: no addresses", host)
	}

	addr := net.JoinHostPort(host, strconv.Itoa(opts.TCPPort))
	res := &PingResult{Method: MethodTCP, Target: host}

	for i := range opts.Count {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(opts.Interval):
			}
		}

		res.Sent++

		dialer := net.Dialer{Timeout: opts.Timeout}
		start := time.Now()
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue // counts as loss, not as a fatal error
		}
		elapsed := time.Since(start)

		if res.Addr == "" {
			res.Addr = conn.RemoteAddr().String()
		}
		_ = conn.Close()
		res.RTTs = append(res.RTTs, elapsed)
	}

	return res, nil
}

// PingCapabilityHint returns an actionable explanation when unprivileged ICMP
// looks unavailable, or "" when there is nothing useful to say.
//
// It deliberately only reads: enabling this needs root, and a measurement tool
// silently escalating privileges would be indefensible.
func PingCapabilityHint() string {
	if runtime.GOOS != "linux" {
		return ""
	}

	data, err := os.ReadFile("/proc/sys/net/ipv4/ping_group_range")
	if err != nil {
		return ""
	}

	value := strings.TrimSpace(string(data))
	if groupRangeDisabled(value) {
		return fmt.Sprintf(
			"Unprivileged ICMP is disabled here (net.ipv4.ping_group_range = %q). To enable it: sudo sysctl -w net.ipv4.ping_group_range='0 2147483647'",
			value)
	}
	return ""
}

// groupRangeDisabled reports whether a ping_group_range value excludes every
// group, which distributions commonly express as the inverted range "1 0".
func groupRangeDisabled(value string) bool {
	fields := strings.Fields(value)
	if len(fields) != 2 {
		return false
	}
	low, errLow := strconv.ParseInt(fields[0], 10, 64)
	high, errHigh := strconv.ParseInt(fields[1], 10, 64)
	if errLow != nil || errHigh != nil {
		return false
	}
	return low > high
}
