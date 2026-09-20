package netprobe

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// coarseClock reports whether this platform's monotonic clock is too coarse to
// resolve a loopback setup phase.
//
// Windows measured a real connect as exactly 0ms in CI, then a real first byte
// as 0ms on the next run, while the occurrence flags fired correctly both times.
// Which phase rounds to zero is not deterministic, so only the occurrence flags
// are asserted portably and the duration assertions are limited to platforms
// whose clock can actually resolve them.
const coarseClock = runtime.GOOS == "windows"

// stubICMP replaces the ICMP probe for the duration of a test.
func stubICMP(t *testing.T, fn func(context.Context, string, PingOptions) (*PingResult, error)) {
	t.Helper()
	original := pingICMPFunc
	pingICMPFunc = fn
	t.Cleanup(func() { pingICMPFunc = original })
}

// tcpListener returns the address of a live TCP listener.
func tcpListener(t *testing.T) (host string, port int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	addr := l.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

// closedPort returns a port on localhost that nothing is listening on.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func fastOptions() PingOptions {
	return PingOptions{Count: 3, Interval: 5 * time.Millisecond, Timeout: 500 * time.Millisecond}
}

func TestPingUsesICMPWhenAvailable(t *testing.T) {
	want := &PingResult{Method: MethodICMP, Sent: 3, RTTs: []time.Duration{time.Millisecond}}
	stubICMP(t, func(context.Context, string, PingOptions) (*PingResult, error) {
		return want, nil
	})

	got, err := Ping(context.Background(), "example.com", fastOptions())
	if err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	if got != want {
		t.Error("Ping() did not return the ICMP result")
	}
	if got.Warning != "" {
		t.Errorf("Warning = %q, want empty when ICMP worked", got.Warning)
	}
}

// The fallback must be explicit about the substitution: an ICMP RTT and a TCP
// connect RTT are different quantities.
func TestPingFallsBackToTCPAndSaysSo(t *testing.T) {
	stubICMP(t, func(context.Context, string, PingOptions) (*PingResult, error) {
		return nil, errors.New("socket: permission denied")
	})

	host, port := tcpListener(t)
	opts := fastOptions()
	opts.TCPPort = port

	got, err := Ping(context.Background(), host, opts)
	if err != nil {
		t.Fatalf("Ping() error = %v", err)
	}

	if got.Method != MethodTCP {
		t.Errorf("Method = %q, want %q", got.Method, MethodTCP)
	}
	if len(got.RTTs) == 0 {
		t.Error("TCP fallback produced no RTTs against a live listener")
	}
	if got.Sent != opts.Count {
		t.Errorf("Sent = %d, want %d", got.Sent, opts.Count)
	}
	if got.Addr == "" {
		t.Error("Addr is empty; the contacted address must be recorded")
	}

	for _, want := range []string{"ICMP unavailable", "TCP connect", strconv.Itoa(port), "must not be compared"} {
		if !strings.Contains(got.Warning, want) {
			t.Errorf("Warning = %q, want it to mention %q", got.Warning, want)
		}
	}
}

func TestPingErrorsWhenBothMethodsFail(t *testing.T) {
	stubICMP(t, func(context.Context, string, PingOptions) (*PingResult, error) {
		return nil, errors.New("icmp is unavailable")
	})

	// A name that cannot resolve makes the TCP fallback fail too.
	_, err := Ping(context.Background(), "this-host-does-not-exist.invalid", fastOptions())
	if err == nil {
		t.Fatal("Ping() succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "TCP fallback also failed") {
		t.Errorf("error = %v, want it to explain that both methods failed", err)
	}
	if !strings.Contains(err.Error(), "icmp is unavailable") {
		t.Errorf("error = %v, want it to preserve the ICMP failure reason", err)
	}
}

// Total packet loss is a measurement, not an error.
func TestPingTCPReportsLossRatherThanFailing(t *testing.T) {
	stubICMP(t, func(context.Context, string, PingOptions) (*PingResult, error) {
		return nil, errors.New("no icmp")
	})

	opts := fastOptions()
	opts.TCPPort = closedPort(t)

	got, err := Ping(context.Background(), "127.0.0.1", opts)
	if err != nil {
		t.Fatalf("Ping() error = %v, want a 100%% loss result", err)
	}
	if got.Sent != opts.Count {
		t.Errorf("Sent = %d, want %d", got.Sent, opts.Count)
	}
	if len(got.RTTs) != 0 {
		t.Errorf("RTTs = %v, want none when nothing is listening", got.RTTs)
	}
}

func TestPingHonoursContextCancellation(t *testing.T) {
	stubICMP(t, func(context.Context, string, PingOptions) (*PingResult, error) {
		return nil, errors.New("no icmp")
	})

	host, port := tcpListener(t)
	opts := fastOptions()
	opts.Count = 100
	opts.Interval = 50 * time.Millisecond
	opts.TCPPort = port

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(60 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := Ping(ctx, host, opts)

	if err == nil {
		t.Fatal("Ping() ignored cancellation")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Ping() took %v to notice cancellation", elapsed)
	}
}

func TestPingOptionsDefaults(t *testing.T) {
	got := PingOptions{}.withDefaults()

	if got.Count != 10 {
		t.Errorf("Count = %d, want 10", got.Count)
	}
	if got.Interval != 200*time.Millisecond {
		t.Errorf("Interval = %v, want 200ms", got.Interval)
	}
	if got.Timeout != 2*time.Second {
		t.Errorf("Timeout = %v, want 2s", got.Timeout)
	}
	if got.TCPPort != 443 {
		t.Errorf("TCPPort = %d, want 443", got.TCPPort)
	}
}

func TestGroupRangeDisabled(t *testing.T) {
	tests := map[string]struct {
		value string
		want  bool
	}{
		"inverted range excludes everyone": {"1\t0", true},
		"space separated inverted":         {"1 0", true},
		"everything allowed":               {"0\t2147483647", false},
		"single group allowed":             {"1000\t1000", false},
		"malformed":                        {"nonsense", false},
		"too few fields":                   {"1", false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := groupRangeDisabled(tc.value); got != tc.want {
				t.Errorf("groupRangeDisabled(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestPingCapabilityHintDoesNotPanic(t *testing.T) {
	// Non-empty only on Linux with a disabled range; the contract is that it is
	// always safe to call.
	_ = PingCapabilityHint()
}

// --- timings ---------------------------------------------------------------

func TestTimingsMeasureSetupPhases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua != "warpbench-test" {
			t.Errorf("User-Agent = %q, want warpbench-test", ua)
		}
		if got := r.Header.Get("Range"); got != "bytes=0-1023" {
			t.Errorf("Range = %q, want a 1 KiB probe", got)
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()

	got, err := Timings(context.Background(), srv.URL, srv.Client(), TimingOptions{UserAgent: "warpbench-test"})
	if err != nil {
		t.Fatalf("Timings() error = %v", err)
	}

	// Occurrence, not duration, is the invariant. A loopback connect can finish
	// faster than the platform clock resolves: Windows measured it as exactly
	// 0ms in CI while TTFB on the same request was non-zero.
	if !got.ConnectRan {
		t.Error("ConnectRan = false, the TCP connect trace did not fire")
	}
	if !got.FirstByteRan {
		t.Error("FirstByteRan = false, the first-byte trace did not fire")
	}
	if !coarseClock {
		if got.Connect <= 0 {
			t.Error("Connect = 0, want a measured TCP connect")
		}
		if got.TTFB <= 0 {
			t.Error("TTFB = 0, want a measured first byte")
		}
	}
	if got.Total < got.Connect {
		t.Errorf("Total %v < Connect %v", got.Total, got.Connect)
	}
	if got.Total <= 0 {
		t.Error("Total = 0, want a measurable request duration")
	}
	if got.RemoteIP == "" {
		t.Error("RemoteIP is empty; the resolved address must be recorded")
	}
	if strings.Contains(got.RemoteIP, ":") {
		t.Errorf("RemoteIP = %q, want the host without a port", got.RemoteIP)
	}
	if got.Status != http.StatusPartialContent {
		t.Errorf("Status = %d, want 206", got.Status)
	}
	if got.Proto == "" {
		t.Error("Proto is empty; which HTTP version was used must be recorded")
	}
	// Plaintext HTTP, so the TLS phase must be reported as not having run,
	// which is distinct from having run in zero time.
	if got.TLSRan {
		t.Error("TLSRan = true for a plaintext request")
	}
	if got.TLS != 0 {
		t.Errorf("TLS = %v, want 0 for a plaintext request", got.TLS)
	}
}

func TestTimingsCaptureTLSHandshake(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	got, err := Timings(context.Background(), srv.URL, srv.Client(), TimingOptions{})
	if err != nil {
		t.Fatalf("Timings() error = %v", err)
	}

	if !got.TLSRan {
		t.Error("TLSRan = false, want the handshake trace to have fired")
	}
	if !coarseClock && got.TLS <= 0 {
		t.Error("TLS = 0, want a measured handshake")
	}
	if !strings.HasPrefix(got.TLSVersion, "TLS1.") {
		t.Errorf("TLSVersion = %q, want a TLS1.x name", got.TLSVersion)
	}
}

func TestTimingsRejectsErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	got, err := Timings(context.Background(), srv.URL, srv.Client(), TimingOptions{})
	if err == nil {
		t.Fatal("Timings() accepted a 500")
	}
	// The phases still happened, so they are still reported.
	if !got.ConnectRan {
		t.Error("ConnectRan = false even though the connection succeeded")
	}
	if got.Status != http.StatusInternalServerError {
		t.Errorf("Status = %d, want 500", got.Status)
	}
}

// Connection reuse would make the setup phases vanish from the measurement, so
// every probe must force a fresh connection.
func TestTimingsForcesAFreshConnectionEachSample(t *testing.T) {
	var (
		mu    sync.Mutex
		conns int
	)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			mu.Lock()
			conns++
			mu.Unlock()
		}
	}
	srv.Start()
	defer srv.Close()

	client := srv.Client()
	for range 3 {
		if _, err := Timings(context.Background(), srv.URL, client, TimingOptions{}); err != nil {
			t.Fatalf("Timings() error = %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if conns != 3 {
		t.Errorf("server saw %d connections for 3 probes, want 3 (connection reuse hides setup cost)", conns)
	}
}

func TestTimingsNHonoursContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results, errs := TimingsN(ctx, srv.URL, srv.Client(), TimingOptions{}, 3)

	if len(results) != 0 {
		t.Errorf("got %d results with a cancelled context, want 0", len(results))
	}
	if len(errs) == 0 {
		t.Error("want at least one error recorded for a cancelled context")
	}
}

func TestTimingsNCollectsSuccessesAndFailures(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	results, errs := TimingsN(context.Background(), srv.URL, srv.Client(), TimingOptions{}, 3)

	if len(results) != 2 {
		t.Errorf("got %d successes, want 2", len(results))
	}
	if len(errs) != 1 {
		t.Errorf("got %d errors, want 1", len(errs))
	}
}

func TestTimingsNDefaultsToOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	results, errs := TimingsN(context.Background(), srv.URL, srv.Client(), TimingOptions{}, 0)

	if len(results) != 1 || len(errs) != 0 {
		t.Errorf("TimingsN(0) = %d results, %d errors; want 1 and 0", len(results), len(errs))
	}
}

func TestMedianTiming(t *testing.T) {
	if got := MedianTiming(nil); got != (Timing{}) {
		t.Errorf("MedianTiming(nil) = %+v, want zero", got)
	}

	single := Timing{Connect: 5 * time.Millisecond}
	if got := MedianTiming([]Timing{single}); got != single {
		t.Errorf("MedianTiming(single) = %+v, want %+v", got, single)
	}

	got := MedianTiming([]Timing{
		{Connect: 10 * time.Millisecond, TTFB: 30 * time.Millisecond, RemoteIP: "a", Proto: "HTTP/1.1"},
		{Connect: 30 * time.Millisecond, TTFB: 10 * time.Millisecond, RemoteIP: "b", Proto: "HTTP/2.0"},
		{Connect: 20 * time.Millisecond, TTFB: 20 * time.Millisecond, RemoteIP: "c", Proto: "HTTP/1.1"},
	})

	if got.Connect != 20*time.Millisecond {
		t.Errorf("Connect = %v, want the median 20ms", got.Connect)
	}
	if got.TTFB != 20*time.Millisecond {
		t.Errorf("TTFB = %v, want the median 20ms", got.TTFB)
	}
	// Descriptive fields come from the last sample rather than being invented.
	if got.RemoteIP != "c" || got.Proto != "HTTP/1.1" {
		t.Errorf("descriptive fields = %q/%q, want the last sample's", got.RemoteIP, got.Proto)
	}
}

// Occurrence is a union across samples: claiming a phase ran when it never did
// would misdescribe the connection.
func TestMedianTimingUnionsPhaseOccurrence(t *testing.T) {
	got := MedianTiming([]Timing{
		{ConnectRan: true, TLSRan: false, Total: time.Millisecond},
		{ConnectRan: true, TLSRan: true, Total: 3 * time.Millisecond},
		{ConnectRan: false, TLSRan: false, Total: 2 * time.Millisecond},
	})

	if !got.ConnectRan {
		t.Error("ConnectRan = false, want true when any sample observed it")
	}
	if !got.TLSRan {
		t.Error("TLSRan = false, want true when any sample observed it")
	}
	if got.DNSRan {
		t.Error("DNSRan = true, but no sample observed it")
	}
}

// A mid-run address change means the route moved, which invalidates a clean
// before/after comparison and must be surfaced.
func TestAddressChanged(t *testing.T) {
	tests := map[string]struct {
		timings []Timing
		want    bool
	}{
		"empty":       {nil, false},
		"single":      {[]Timing{{RemoteIP: "a"}}, false},
		"all same":    {[]Timing{{RemoteIP: "a"}, {RemoteIP: "a"}}, false},
		"changed":     {[]Timing{{RemoteIP: "a"}, {RemoteIP: "b"}}, true},
		"changed mid": {[]Timing{{RemoteIP: "a"}, {RemoteIP: "a"}, {RemoteIP: "b"}}, true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := AddressChanged(tc.timings); got != tc.want {
				t.Errorf("AddressChanged() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTLSVersionName(t *testing.T) {
	if got := tlsVersionName(0x0303); got != "TLS1.2" {
		t.Errorf("tlsVersionName(TLS1.2) = %q", got)
	}
	if got := tlsVersionName(0x0304); got != "TLS1.3" {
		t.Errorf("tlsVersionName(TLS1.3) = %q", got)
	}
	if got := tlsVersionName(0x9999); got != "0x9999" {
		t.Errorf("tlsVersionName(unknown) = %q, want a hex fallback", got)
	}
}

func TestHostOnly(t *testing.T) {
	tests := map[string]string{
		"93.184.216.34:443":  "93.184.216.34",
		"[2606:2800::1]:443": "2606:2800::1",
		"no-port":            "no-port",
	}
	for in, want := range tests {
		if got := hostOnly(in); got != want {
			t.Errorf("hostOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTimingOptionsDefaults(t *testing.T) {
	got := TimingOptions{}.withDefaults()

	if got.Timeout != 10*time.Second {
		t.Errorf("Timeout = %v, want 10s", got.Timeout)
	}
	if got.ProbeBytes != 1024 {
		t.Errorf("ProbeBytes = %d, want 1024", got.ProbeBytes)
	}
}

// --- opt-in real-network integration ---------------------------------------

// These need network access and a host that permits unprivileged ICMP, so they
// are opt-in. Run them with WARPBENCH_INTEGRATION=1.
func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("WARPBENCH_INTEGRATION") == "" {
		t.Skip("set WARPBENCH_INTEGRATION=1 to run network integration tests")
	}
}

func TestIntegrationPingRealHost(t *testing.T) {
	requireIntegration(t)

	got, err := Ping(context.Background(), "cloudflare.com", PingOptions{
		Count:    6,
		Interval: 200 * time.Millisecond,
		Timeout:  2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Ping() error = %v", err)
	}

	t.Logf("method=%s target=%s addr=%s sent=%d received=%d warning=%q",
		got.Method, got.Target, got.Addr, got.Sent, len(got.RTTs), got.Warning)

	if got.Sent == 0 {
		t.Error("no probes were sent")
	}
	if got.Method == MethodICMP && len(got.RTTs) > 0 && got.Warning != "" {
		t.Errorf("ICMP succeeded but carried a warning: %q", got.Warning)
	}
}

func TestIntegrationTimingsRealHost(t *testing.T) {
	requireIntegration(t)

	client := &http.Client{Timeout: 15 * time.Second}
	timings, errs := TimingsN(context.Background(), "https://speed.cloudflare.com/__down?bytes=1024", client, TimingOptions{}, 3)
	if len(timings) == 0 {
		t.Fatalf("no timing samples; errors = %v", errs)
	}

	median := MedianTiming(timings)
	t.Logf("dns=%v connect=%v tls=%v ttfb=%v total=%v ip=%s proto=%s tlsver=%s",
		median.DNS, median.Connect, median.TLS, median.TTFB, median.Total,
		median.RemoteIP, median.Proto, median.TLSVersion)

	if !median.ConnectRan {
		t.Error("ConnectRan = false against a real host")
	}
	if !median.TLSRan {
		t.Error("TLSRan = false against an HTTPS host")
	}
	// A remote connect and handshake are tens of milliseconds, far above any
	// plausible clock granularity, so these hold on every platform.
	if median.Connect <= 0 {
		t.Error("Connect = 0 against a real host")
	}
	if median.TLS <= 0 {
		t.Error("TLS = 0 against an HTTPS host")
	}
}
