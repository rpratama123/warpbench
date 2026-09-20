package throughput

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rpratama123/warpbench/internal/iperf3"
	"github.com/rpratama123/warpbench/internal/serverlist"
)

func strptr(s string) *string { return &s }

// streamingServer serves an endless stream until the client disconnects.
func streamingServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		buf := make([]byte, 64<<10)
		for {
			if _, err := w.Write(buf); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			default:
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testOpts() Opts {
	return Opts{
		Duration:  250 * time.Millisecond,
		Warmup:    50 * time.Millisecond,
		MaxBytes:  1 << 30,
		MinBytes:  1,
		Parallel:  1,
		UserAgent: "warpbench-test",
	}
}

// --- transfer core ---------------------------------------------------------

func TestDownloadOnceMeasuresARate(t *testing.T) {
	srv := streamingServer(t)
	client := NewHTTPClient(0)

	got, proto, ip, err := downloadSample(context.Background(), client, srv.URL, testOpts())
	if err != nil {
		t.Fatalf("downloadSample() error = %v", err)
	}

	if got.bytes <= 0 {
		t.Fatal("no bytes were transferred")
	}
	if got.elapsed < 150*time.Millisecond {
		t.Errorf("elapsed = %v, want roughly the 250ms window", got.elapsed)
	}
	if got.elapsed > 5*time.Second {
		t.Errorf("elapsed = %v, the deadline did not stop the transfer", got.elapsed)
	}
	if got.steadyBytes <= 0 || got.steadyFor <= 0 {
		t.Errorf("no steady window: steadyBytes=%d steadyFor=%v", got.steadyBytes, got.steadyFor)
	}
	if got.steadyBytes > got.bytes {
		t.Errorf("steadyBytes %d exceeds total bytes %d", got.steadyBytes, got.bytes)
	}
	if proto == "" {
		t.Error("proto is empty; which HTTP version was used must be recorded")
	}
	if ip == "" {
		t.Error("remote IP is empty; the resolved address must be recorded")
	}
}

// The byte guard must stop a runaway transfer before the clock does.
func TestDownloadOnceRespectsMaxBytes(t *testing.T) {
	srv := streamingServer(t)
	client := NewHTTPClient(0)

	o := testOpts()
	o.Duration = 10 * time.Second
	o.MaxBytes = 256 << 10

	got, _, _, err := downloadSample(context.Background(), client, srv.URL, o)
	if err != nil {
		t.Fatalf("downloadSample() error = %v", err)
	}

	if !got.truncated {
		t.Error("truncated = false, want true when MaxBytes stopped the transfer")
	}
	if got.bytes > o.MaxBytes {
		t.Errorf("bytes = %d, want at most the %d guard", got.bytes, o.MaxBytes)
	}
	if got.bytes == 0 {
		t.Error("no bytes were transferred before the guard")
	}
	if got.elapsed >= o.Duration {
		t.Errorf("elapsed = %v, want the byte guard to end it well before %v", got.elapsed, o.Duration)
	}
}

func TestDownloadOnceRejectsErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, _, _, err := downloadSample(context.Background(), NewHTTPClient(0), srv.URL, testOpts())
	if err == nil {
		t.Fatal("downloadSample() accepted a 403")
	}
}

func TestUploadOnceMeasuresARate(t *testing.T) {
	var received int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		atomic.StoreInt64(&received, n)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	got, _, _, err := uploadOnce(context.Background(), NewHTTPClient(0), srv.URL, testOpts())
	if err != nil {
		t.Fatalf("uploadOnce() error = %v", err)
	}

	if got.bytes <= 0 {
		t.Fatal("no bytes were uploaded")
	}
	if got.elapsed <= 0 {
		t.Error("elapsed = 0")
	}
	if n := atomic.LoadInt64(&received); n == 0 {
		t.Error("the server received nothing")
	}
}

// The body is sent chunked because its length is decided by the clock.
func TestUploadOnceUsesChunkedEncoding(t *testing.T) {
	var (
		chunked        bool
		contentLength  int64
		contentTypeSet bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, te := range r.TransferEncoding {
			if te == "chunked" {
				chunked = true
			}
		}
		contentLength = r.ContentLength
		contentTypeSet = r.Header.Get("Content-Type") == "application/octet-stream"
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if _, _, _, err := uploadOnce(context.Background(), NewHTTPClient(0), srv.URL, testOpts()); err != nil {
		t.Fatalf("uploadOnce() error = %v", err)
	}

	if !chunked {
		t.Error("upload was not chunked; a time-bounded body has no known length")
	}
	if contentLength != -1 {
		t.Errorf("ContentLength = %d, want -1 for an unknown-length body", contentLength)
	}
	if !contentTypeSet {
		t.Error("Content-Type was not set")
	}
}

// Random payload bytes, not zeros: a transparent compressor would otherwise
// inflate the measured rate on exactly the links this tool targets.
func TestUploadBodyIsNotCompressible(t *testing.T) {
	p := newProgress(time.Now(), Opts{Duration: time.Second, MaxBytes: 4096})
	body := newUploadBody(p)

	buf := make([]byte, 4096)
	if _, err := io.ReadFull(body, buf); err != nil {
		t.Fatalf("reading upload body: %v", err)
	}

	var zeroes int
	for _, b := range buf {
		if b == 0 {
			zeroes++
		}
	}
	if zeroes > len(buf)/4 {
		t.Errorf("%d of %d bytes were zero; the payload looks compressible", zeroes, len(buf))
	}
}

func TestUploadBodyStopsAtMaxBytes(t *testing.T) {
	p := newProgress(time.Now(), Opts{Duration: time.Minute, MaxBytes: 1024})
	body := newUploadBody(p)

	n, err := io.Copy(io.Discard, body)
	if err != nil {
		t.Fatalf("io.Copy: %v", err)
	}
	if n > 1024 {
		t.Errorf("uploaded %d bytes, want at most the 1024 guard", n)
	}
	if !p.truncated {
		t.Error("truncated = false, want true when the guard stopped the body")
	}
}

// Multi-stream runs are aggregated, and a total failure is still an error.
func TestParallelAggregates(t *testing.T) {
	var calls int32
	got, _, _, err := parallel(4, func() (counters, string, string, error) {
		atomic.AddInt32(&calls, 1)
		return counters{bytes: 100, steadyBytes: 80, steadyFor: time.Second, elapsed: time.Second}, "HTTP/1.1", "1.2.3.4", nil
	})
	if err != nil {
		t.Fatalf("parallel() error = %v", err)
	}

	if n := atomic.LoadInt32(&calls); n != 4 {
		t.Errorf("ran %d streams, want 4", n)
	}
	if got.bytes != 400 {
		t.Errorf("aggregate bytes = %d, want 400", got.bytes)
	}
	if got.steadyBytes != 320 {
		t.Errorf("aggregate steadyBytes = %d, want 320", got.steadyBytes)
	}
	if got.elapsed != time.Second {
		t.Errorf("aggregate elapsed = %v, want the longest stream (1s)", got.elapsed)
	}
}

func TestParallelAllFailedIsAnError(t *testing.T) {
	_, _, _, err := parallel(3, func() (counters, string, string, error) {
		return counters{}, "", "", errors.New("boom")
	})
	if err == nil {
		t.Fatal("parallel() succeeded when every stream failed")
	}
}

// One stream succeeding is a smaller sample, not a failure.
func TestParallelPartialFailure(t *testing.T) {
	var n int32
	got, _, _, err := parallel(4, func() (counters, string, string, error) {
		if atomic.AddInt32(&n, 1) == 1 {
			return counters{}, "", "", errors.New("one stream failed")
		}
		return counters{bytes: 50, elapsed: time.Second}, "HTTP/1.1", "", nil
	})
	if err != nil {
		t.Fatalf("parallel() error = %v, want nil when a stream succeeded", err)
	}
	if got.bytes != 150 {
		t.Errorf("aggregate bytes = %d, want 150 from the three survivors", got.bytes)
	}
}

// --- sample construction ---------------------------------------------------

func TestRateFlagsUndersized(t *testing.T) {
	o := Opts{MinBytes: 1000}

	// Both fixtures carry a usable steady window, so the only thing under test
	// here is the MinBytes comparison rather than the no-window fallback.
	small := rate("download", counters{
		bytes: 10, elapsed: time.Second, steadyBytes: 9, steadyFor: time.Second,
	}, o, "HTTP/1.1", "")
	if !small.Undersized {
		t.Error("a transfer below MinBytes should be flagged undersized")
	}

	big := rate("download", counters{
		bytes: 10_000, elapsed: time.Second, steadyBytes: 9_000, steadyFor: time.Second,
	}, o, "HTTP/1.1", "")
	if big.Undersized {
		t.Error("a transfer above MinBytes should not be flagged undersized")
	}
}

// With no usable steady window the overall figure is used as a fallback, and
// the sample is flagged, rather than reporting a zero rate.
func TestRateFallsBackWhenNoSteadyWindow(t *testing.T) {
	got := rate("download", counters{bytes: 5000, elapsed: time.Second}, Opts{MinBytes: 1}, "HTTP/1.1", "")

	if got.SteadyMbps != got.OverallMbps {
		t.Errorf("SteadyMbps = %v, want the overall fallback %v", got.SteadyMbps, got.OverallMbps)
	}
	if got.SteadyMbps <= 0 {
		t.Error("SteadyMbps = 0, want the fallback rate")
	}
	if !got.Undersized {
		t.Error("a sample with no steady window should be flagged")
	}
}

func TestRateComputesMbps(t *testing.T) {
	// 12.5 MB in one second is exactly 100 Mbps.
	got := rate("download", counters{
		bytes: 12_500_000, elapsed: time.Second,
		steadyBytes: 12_500_000, steadyFor: time.Second,
	}, Opts{MinBytes: 1}, "HTTP/1.1", "1.2.3.4")

	if got.SteadyMbps != 100 {
		t.Errorf("SteadyMbps = %v, want 100", got.SteadyMbps)
	}
	if got.OverallMbps != 100 {
		t.Errorf("OverallMbps = %v, want 100", got.OverallMbps)
	}
	if got.RemoteIP != "1.2.3.4" {
		t.Errorf("RemoteIP = %q", got.RemoteIP)
	}
}

func TestOptsDefaults(t *testing.T) {
	got := Opts{}.withDefaults()

	if got.Duration != DefaultDuration {
		t.Errorf("Duration = %v, want %v", got.Duration, DefaultDuration)
	}
	if got.Warmup != DefaultWarmup {
		t.Errorf("Warmup = %v, want %v", got.Warmup, DefaultWarmup)
	}
	if got.MaxBytes != DefaultMaxBytes {
		t.Errorf("MaxBytes = %d, want %d", got.MaxBytes, DefaultMaxBytes)
	}
	if got.MinBytes != DefaultMinBytes {
		t.Errorf("MinBytes = %d, want %d", got.MinBytes, DefaultMinBytes)
	}
	if got.Parallel != 1 {
		t.Errorf("Parallel = %d, want 1", got.Parallel)
	}
}

// --- HTTP client policy ----------------------------------------------------

// Keep-alives would let a sample inherit a warm congestion window from the
// previous one, which is not what a fresh measurement should show.
func TestClientDisablesConnectionReuse(t *testing.T) {
	var conns int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			atomic.AddInt32(&conns, 1)
		}
	}
	srv.Start()
	defer srv.Close()

	client := NewHTTPClient(0)
	for range 3 {
		req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	if n := atomic.LoadInt32(&conns); n != 3 {
		t.Errorf("server saw %d connections for 3 requests, want 3", n)
	}
}

// A redirect to another host would change what is being measured.
func TestClientDoesNotFollowRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := NewHTTPClient(0).Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want the 302 to be returned rather than followed", resp.StatusCode)
	}
}

// A stray HTTP_PROXY must not silently redirect the measurement through a
// proxy: that would measure the proxy, not the ISP path.
func TestClientIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := NewHTTPClient(0).Do(req)
	if err != nil {
		t.Fatalf("request failed, so the proxy from the environment was used: %v", err)
	}
	_ = resp.Body.Close()
}

// --- urls ------------------------------------------------------------------

func TestWithQueryParam(t *testing.T) {
	tests := map[string]struct {
		in      string
		key     string
		value   string
		want    string
		wantErr bool
	}{
		"append":           {"https://e.com/g.php", "ckSize", "1", "https://e.com/g.php?ckSize=1", false},
		"replace existing": {"https://e.com/g.php?ckSize=99", "ckSize", "1", "https://e.com/g.php?ckSize=1", false},
		"preserve others":  {"https://e.com/g.php?a=b", "ckSize", "1", "https://e.com/g.php?a=b&ckSize=1", false},
		"empty url":        {"", "ckSize", "1", "", true},
		"unparseable url":  {"://bad", "ckSize", "1", "", true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := withQueryParam(tc.in, tc.key, tc.value)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("withQueryParam(%q) succeeded, want an error", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("withQueryParam() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("withQueryParam() = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- adapters --------------------------------------------------------------

func httpFileServer(download string) serverlist.Server {
	return serverlist.Server{
		ID: "x", Group: "g", Name: "X", Protocol: "http-file",
		PingHost: "e.com", DownloadURL: strptr(download),
		Capabilities: []string{"ping", "download", "timings"}, Tier: "quick",
	}
}

func TestNewRejectsUnknownProtocol(t *testing.T) {
	_, err := New(serverlist.Server{Protocol: "carrier-pigeon"}, Deps{})
	if err == nil {
		t.Fatal("New() accepted an unknown protocol")
	}
	if !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Errorf("error = %v, want it to name the protocol", err)
	}
}

func TestNewRejectsIPerf3WithoutABlock(t *testing.T) {
	_, err := New(serverlist.Server{ID: "x", Protocol: "iperf3"}, Deps{})
	if err == nil {
		t.Fatal("New() accepted iperf3 without an iperf3 block")
	}
}

func TestHTTPFileCapsAndUploadUnsupported(t *testing.T) {
	a, err := New(httpFileServer("https://e.com/100MB.bin"), Deps{})
	if err != nil {
		t.Fatal(err)
	}

	caps := a.Caps()
	if !caps.Download || !caps.Timings || !caps.Ping {
		t.Errorf("Caps() = %+v, want download, timings and ping", caps)
	}
	if caps.Upload {
		t.Error("http-file must not claim upload support")
	}
	if got := a.TimingsURL(); got != "https://e.com/100MB.bin" {
		t.Errorf("TimingsURL() = %q", got)
	}

	_, err = a.Upload(context.Background(), Opts{})
	if !IsNotSupported(err) {
		t.Errorf("Upload() error = %v, want a not-supported error", err)
	}
}

// A download-only target must be reported as unsupported, never as zero.
func TestNotSupportedErrorsAreDistinguishable(t *testing.T) {
	err := notSupported("http-file", "upload")
	if !IsNotSupported(err) {
		t.Error("IsNotSupported() = false for an ErrNotSupported")
	}
	if IsNotSupported(errors.New("something else")) {
		t.Error("IsNotSupported() = true for an unrelated error")
	}
	if !strings.Contains(err.Error(), "http-file") || !strings.Contains(err.Error(), "upload") {
		t.Errorf("error = %v, want both the protocol and the metric named", err)
	}
}

func TestCloudflareTimingsURLIsSmall(t *testing.T) {
	s := serverlist.Server{
		Protocol: "cloudflare", PingHost: "speed.cloudflare.com",
		DownloadURL:  strptr("https://speed.cloudflare.com/__down"),
		UploadURL:    strptr("https://speed.cloudflare.com/__up"),
		Capabilities: []string{"ping", "download", "upload", "timings"}, Tier: "quick",
	}

	a, err := New(s, Deps{})
	if err != nil {
		t.Fatal(err)
	}

	got := a.TimingsURL()
	if !strings.Contains(got, "bytes=1024") {
		t.Errorf("TimingsURL() = %q, want a small probe rather than a throughput payload", got)
	}
	if !a.Caps().Upload {
		t.Error("cloudflare should support upload")
	}
}

func TestLibreSpeedURLsGetChunkSize(t *testing.T) {
	s := serverlist.Server{
		Protocol: "librespeed", PingHost: "ls.example.com",
		DownloadURL:  strptr("https://ls.example.com/backend/garbage.php"),
		UploadURL:    strptr("https://ls.example.com/backend/empty.php"),
		Capabilities: []string{"ping", "download", "upload", "timings"}, Tier: "quick",
	}

	a, err := New(s, Deps{})
	if err != nil {
		t.Fatal(err)
	}

	if got := a.TimingsURL(); !strings.Contains(got, "ckSize=1") {
		t.Errorf("TimingsURL() = %q, want a 1 MB probe", got)
	}
	if !a.Caps().Upload {
		t.Error("librespeed should support upload")
	}
}

func TestLibreSpeedDownloadRequestsALargeChunk(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 1024))
	}))
	defer srv.Close()

	s := serverlist.Server{
		Protocol: "librespeed", PingHost: "ls.example.com",
		DownloadURL:  strptr(srv.URL + "/garbage.php"),
		Capabilities: []string{"ping", "download"}, Tier: "quick",
	}

	a, err := New(s, Deps{Client: NewHTTPClient(0)})
	if err != nil {
		t.Fatal(err)
	}

	o := testOpts()
	o.MaxBytes = 4096
	if _, err := a.Download(context.Background(), o); err != nil {
		t.Fatalf("Download() error = %v", err)
	}

	if !strings.Contains(query, "ckSize=") {
		t.Errorf("query = %q, want a ckSize parameter", query)
	}
	if strings.Contains(query, "ckSize=1") {
		t.Errorf("query = %q, want a large chunk so the clock ends the sample", query)
	}
}

// The iperf3 adapter must refuse to run without a resolved binary rather than
// silently reporting zero.
func TestIPerf3WithoutBinaryIsNotSupported(t *testing.T) {
	s := serverlist.Server{
		ID: "x", Protocol: "iperf3", PingHost: "i.example.com",
		Capabilities: []string{"ping", "download", "upload"}, Tier: "quick",
		IPerf3: &serverlist.IPerf3{Host: "i.example.com", PortRange: []int{5201, 5210}, ReverseDownload: true},
	}

	a, err := New(s, Deps{})
	if err != nil {
		t.Fatal(err)
	}

	if a.TimingsURL() != "" {
		t.Error("iperf3 has no HTTP surface, so TimingsURL must be empty")
	}
	if a.Caps().Timings {
		t.Error("iperf3 must not claim the timings capability")
	}

	_, err = a.Download(context.Background(), Opts{})
	if !IsNotSupported(err) {
		t.Errorf("Download() error = %v, want a not-supported error without a binary", err)
	}
}

func TestIPerf3PortsToTry(t *testing.T) {
	s := serverlist.Server{
		ID: "x", Protocol: "iperf3",
		IPerf3: &serverlist.IPerf3{Host: "i.example.com", PortRange: []int{5201, 5210}},
	}
	a, err := newIPerf3(s, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	adapter := a.(*iperf3Adapter)

	got := adapter.portsToTry()
	if len(got) != iperf3PortAttempts {
		t.Errorf("portsToTry() = %v, want %d attempts", got, iperf3PortAttempts)
	}
	if got[0] != 5201 {
		t.Errorf("first port = %d, want the lower bound 5201", got[0])
	}

	// A reversed range must still be walked from the lower number.
	reversed, err := newIPerf3(serverlist.Server{
		ID: "y", Protocol: "iperf3",
		IPerf3: &serverlist.IPerf3{Host: "i.example.com", PortRange: []int{5210, 5201}},
	}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if got := reversed.(*iperf3Adapter).portsToTry()[0]; got != 5201 {
		t.Errorf("first port for a reversed range = %d, want 5201", got)
	}
}

func TestIPerf3Args(t *testing.T) {
	s := serverlist.Server{
		ID: "x", Protocol: "iperf3",
		IPerf3: &serverlist.IPerf3{Host: "i.example.com", PortRange: []int{5201, 5210}},
	}
	a, _ := newIPerf3(s, Deps{})
	adapter := a.(*iperf3Adapter)

	args := strings.Join(adapter.args(5203, Opts{Duration: 10 * time.Second}, true), " ")

	for _, want := range []string{"-c i.example.com", "-p 5203", "-t 10", "-J", "-R", "-O 1"} {
		if !strings.Contains(args, want) {
			t.Errorf("args = %q, want it to contain %q", args, want)
		}
	}

	// Upload is the default direction, so -R must be absent.
	uploadArgs := strings.Join(adapter.args(5203, Opts{Duration: 10 * time.Second}, false), " ")
	if strings.Contains(uploadArgs, "-R") {
		t.Errorf("upload args = %q, want no -R", uploadArgs)
	}

	// Parallel runs add -P.
	parallelArgs := strings.Join(adapter.args(5203, Opts{Duration: 10 * time.Second, Parallel: 4}, false), " ")
	if !strings.Contains(parallelArgs, "-P 4") {
		t.Errorf("parallel args = %q, want -P 4", parallelArgs)
	}
}

// iperf3's -O 1 discards the first second, so a shorter test would measure
// nothing; the adapter must enforce a floor.
func TestIPerf3EnforcesMinimumDuration(t *testing.T) {
	s := serverlist.Server{
		ID: "x", Protocol: "iperf3",
		IPerf3: &serverlist.IPerf3{Host: "i.example.com", PortRange: []int{5201, 5201}},
	}
	a, _ := newIPerf3(s, Deps{})
	adapter := a.(*iperf3Adapter)

	args := strings.Join(adapter.args(5201, Opts{Duration: time.Millisecond}, false), " ")
	if !strings.Contains(args, "-t 3") {
		t.Errorf("args = %q, want the duration floored to 3 seconds", args)
	}
}

func TestIPerf3SampleFromUsesToolAccounting(t *testing.T) {
	s := serverlist.Server{
		ID: "x", Protocol: "iperf3",
		IPerf3: &serverlist.IPerf3{Host: "i.example.com", PortRange: []int{5201, 5201}},
	}
	a, _ := newIPerf3(s, Deps{})
	adapter := a.(*iperf3Adapter)

	// 12.5 MB in one second is 100 Mbps.
	got := adapter.sampleFrom(iperf3.Sum{Bytes: 12_500_000, Seconds: 1, BitsPerSecond: 100e6}, "1.2.3.4", Opts{MinBytes: 1}, "download")

	if got.SteadyMbps != 100 {
		t.Errorf("SteadyMbps = %v, want 100", got.SteadyMbps)
	}
	if got.Proto != "iperf3" {
		t.Errorf("Proto = %q, want iperf3", got.Proto)
	}
	if got.RemoteIP != "1.2.3.4" {
		t.Errorf("RemoteIP = %q, want the server address from the JSON", got.RemoteIP)
	}
	if got.Metric != "download" {
		t.Errorf("Metric = %q", got.Metric)
	}
}

func TestDeclaredCaps(t *testing.T) {
	caps := declaredCaps(serverlist.Server{Capabilities: []string{"ping", "download"}})
	if !caps.Ping || !caps.Download || caps.Upload || caps.Timings {
		t.Errorf("declaredCaps() = %+v", caps)
	}
}

func TestDepsClientDefaults(t *testing.T) {
	if got := (Deps{}).client(); got == nil {
		t.Error("Deps{}.client() = nil, want a usable default")
	}
}

// --- window filling and partial transfers ----------------------------------

// A target serving a finite payload must not end the sample early: the window
// is filled by issuing further requests, each on its own fresh connection.
func TestDownloadSampleFillsTheWindowAcrossRequests(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 64<<10)) // small, finite payload
	}))
	defer srv.Close()

	got, _, _, err := downloadSample(context.Background(), NewHTTPClient(0), srv.URL, testOpts())
	if err != nil {
		t.Fatalf("downloadSample() error = %v", err)
	}

	if n := atomic.LoadInt32(&requests); n < 2 {
		t.Errorf("issued %d request(s); a finite payload should be followed by another to fill the window", n)
	}
	if got.bytes < 2*(64<<10) {
		t.Errorf("bytes = %d, want the sum across requests", got.bytes)
	}
	if got.partial {
		t.Error("partial = true; completing the payload is normal, not a failure")
	}
}

// A server that refuses the body has measured nothing, so reporting a rate from
// the bytes sent before the refusal would be a fabricated number.
func TestUploadOnceTreatsNon2xxAsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Consume part of the body, then refuse: this is what LibreSpeed
		// deployments do at their configured cap.
		_, _ = io.CopyN(io.Discard, r.Body, 32<<10)
		w.WriteHeader(http.StatusRequestEntityTooLarge)
	}))
	defer srv.Close()

	_, _, _, err := uploadOnce(context.Background(), NewHTTPClient(0), srv.URL, testOpts())
	if err == nil {
		t.Fatal("uploadOnce() reported success for a 413")
	}
	if !strings.Contains(err.Error(), "413") {
		t.Errorf("error = %v, want it to name the status", err)
	}
	if !strings.Contains(err.Error(), "rejected") {
		t.Errorf("error = %v, want it to say the upload was rejected", err)
	}
}

// An error part-way through still leaves a usable measurement, but the shortened
// window must be visible rather than passed off as a full-length sample.
func TestDownloadSampleFlagsPartialOnMidTransferError(t *testing.T) {
	var hijackable atomic.Bool
	hijackable.Store(true)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			hijackable.Store(false)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		// Promise a large body, deliver a little, then hang up.
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 10000000\r\n\r\n")
		_, _ = buf.Write(make([]byte, 32<<10))
		_ = buf.Flush()
		_ = conn.Close()
	}))
	defer srv.Close()

	got, _, _, err := downloadSample(context.Background(), NewHTTPClient(0), srv.URL, testOpts())
	if !hijackable.Load() {
		t.Skip("the test server does not support connection hijacking")
	}
	if err != nil {
		t.Fatalf("downloadSample() error = %v, want the partial measurement", err)
	}

	if !got.partial {
		t.Error("partial = false, want the truncated window to be flagged")
	}
	if got.bytes == 0 {
		t.Error("no bytes were recorded before the connection dropped")
	}
}

// The endpoint rejects bytes >= 100,000,000 with a bare 403, which is invisible
// in its documentation. Pin the ceiling so a future edit cannot silently
// reintroduce it.
func TestCloudflarePayloadStaysUnderTheEndpointCeiling(t *testing.T) {
	const ceiling = 100_000_000

	if cloudflarePayloadBytes >= ceiling {
		t.Errorf("cloudflarePayloadBytes = %d, but the endpoint 403s at %d", cloudflarePayloadBytes, ceiling)
	}
	if cloudflarePayloadBytes <= 0 {
		t.Errorf("cloudflarePayloadBytes = %d, want a positive payload", cloudflarePayloadBytes)
	}
}

// The window must not be filled by an unbounded number of requests if a target
// answers instantly with nothing.
func TestDownloadSampleIsBoundedForAnEmptyTarget(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusOK) // 200 with an empty body
	}))
	defer srv.Close()

	o := testOpts()
	o.Duration = 50 * time.Millisecond

	if _, _, _, err := downloadSample(context.Background(), NewHTTPClient(0), srv.URL, o); err != nil {
		t.Fatalf("downloadSample() error = %v", err)
	}

	if n := atomic.LoadInt32(&requests); n > maxRequestsPerSample {
		t.Errorf("issued %d requests, want at most %d", n, maxRequestsPerSample)
	}
}

func TestRatePropagatesPartial(t *testing.T) {
	got := rate("download", counters{
		bytes: 1000, elapsed: time.Second, steadyBytes: 900, steadyFor: time.Second, partial: true,
	}, Opts{MinBytes: 1}, "HTTP/1.1", "")

	if !got.Partial {
		t.Error("Sample.Partial = false, want the counters' partial flag to carry through")
	}
}
