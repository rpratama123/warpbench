package throughput

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"
)

// HTTPClient is the HTTP configuration used for every throughput sample.
//
// Three choices are deliberate and load-bearing for fidelity:
//
//   - Keep-alives are disabled. Every request opens a fresh connection, so a
//     sample cannot inherit a warm congestion window from an earlier one.
//   - HTTP/2 is not attempted. Otherwise a sample could be silently multiplexed
//     with others, and the recorded protocol would vary between phases.
//   - Proxies are ignored. Measuring through a proxy measures the proxy, not
//     the ISP path, and a stray HTTP_PROXY would silently invalidate a result.
type HTTPClient struct {
	client *http.Client
}

// NewHTTPClient builds the client used for throughput and timing samples.
func NewHTTPClient(timeout time.Duration) *HTTPClient {
	transport := &http.Transport{
		Proxy:               nil,
		DisableKeepAlives:   true,
		ForceAttemptHTTP2:   false,
		MaxIdleConnsPerHost: 0,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: -1,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &HTTPClient{client: &http.Client{
		Transport: transport,
		Timeout:   timeout,
		// A redirect to a different host would change what is being measured.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// Do performs a request, recording the peer address.
func (c *HTTPClient) Do(req *http.Request) (*http.Response, error) {
	return c.client.Do(req)
}

// maxRequestsPerSample bounds how many requests one sample may issue.
//
// The loop exists to fill the measurement window when a target serves a finite
// payload, but a target answering instantly with nothing must not spin.
const maxRequestsPerSample = 64

// attachRemoteIP instruments a request so the connection's peer is recorded.
func attachRemoteIP(req *http.Request, dst *string) *http.Request {
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			if info.Conn != nil {
				*dst = hostOf(info.Conn.RemoteAddr().String())
			}
		},
	}
	return req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
}

func hostOf(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// counters is the raw outcome of one streamed transfer.
type counters struct {
	bytes       int64
	steadyBytes int64
	steadyFor   time.Duration
	elapsed     time.Duration
	truncated   bool
	// partial is set when the window was cut short by an error after some data
	// had already moved. The rate is usable but the window is not what was
	// asked for, which the report must disclose.
	partial bool
}

// progress tracks a transfer against its deadline and byte guard.
//
// The deadline, not the byte guard, normally ends a sample: a byte cap alone
// would make a fast link's measurement window far too short to be meaningful,
// which is why the cap is only a runaway guard.
type progress struct {
	start     time.Time
	deadline  time.Time
	warmup    time.Duration
	limit     int64
	bytes     int64
	warmAt    int64
	warmFor   time.Duration
	warmed    bool
	truncated bool
}

func newProgress(start time.Time, o Opts) *progress {
	return &progress{
		start:    start,
		deadline: start.Add(o.Duration),
		warmup:   o.Warmup,
		limit:    o.MaxBytes,
	}
}

// note records n newly transferred bytes and snapshots the warm-up boundary the
// first time it is crossed.
func (p *progress) note(n int) {
	p.bytes += int64(n)
	if p.warmed {
		return
	}
	if p.warmup <= 0 {
		p.warmed = true
		return
	}
	if elapsed := time.Since(p.start); elapsed >= p.warmup {
		p.warmed = true
		p.warmAt = p.bytes
		p.warmFor = elapsed
	}
}

// exhausted reports whether the transfer should stop.
func (p *progress) exhausted() bool {
	if p.limit > 0 && p.bytes >= p.limit {
		p.truncated = true
		return true
	}
	if !p.deadline.IsZero() && !time.Now().Before(p.deadline) {
		return true
	}
	return false
}

// clamp limits a read buffer to what remains under the byte guard.
func (p *progress) clamp(b []byte) []byte {
	if p.limit <= 0 {
		return b
	}
	remaining := p.limit - p.bytes
	if remaining <= 0 {
		return nil
	}
	if int64(len(b)) > remaining {
		return b[:remaining]
	}
	return b
}

func (p *progress) counters(end time.Time) counters {
	c := counters{
		bytes:     p.bytes,
		elapsed:   end.Sub(p.start),
		truncated: p.truncated,
	}
	if p.warmed && p.bytes > p.warmAt {
		c.steadyBytes = p.bytes - p.warmAt
		c.steadyFor = end.Sub(p.start) - p.warmFor
	}
	return c
}

// countingReader ends a request at the deadline or the byte guard, so the
// measurement window is bounded without relying on the server to stop.
type countingReader struct {
	r io.Reader
	p *progress
}

func (c *countingReader) Read(b []byte) (int, error) {
	if c.p.exhausted() {
		return 0, io.EOF
	}
	b = c.p.clamp(b)
	if len(b) == 0 {
		return 0, io.EOF
	}
	n, err := c.r.Read(b)
	c.p.note(n)
	return n, err
}

// uploadBody generates an upload payload.
//
// The bytes are random rather than zeroed: a transparent compressor on the path
// would otherwise inflate the measured rate, and the congested international
// links this tool exists to measure are exactly where that is most likely.
type uploadBody struct {
	p      *progress
	src    io.Reader
	done   bool
	doneAt time.Time
}

func newUploadBody(p *progress) *uploadBody {
	return &uploadBody{p: p, src: rand.Reader}
}

func (u *uploadBody) Read(b []byte) (int, error) {
	if u.done {
		return 0, io.EOF
	}
	if u.p.exhausted() {
		u.finish()
		return 0, io.EOF
	}
	b = u.p.clamp(b)
	if len(b) == 0 {
		u.finish()
		return 0, io.EOF
	}

	n, err := io.ReadFull(u.src, b)
	u.p.note(n)
	if err != nil {
		u.finish()
		return n, err
	}
	return n, nil
}

func (u *uploadBody) finish() {
	if !u.done {
		u.done = true
		u.doneAt = time.Now()
	}
}

// Close makes uploadBody an io.ReadCloser, which http.NewRequest requires for
// a body of unknown length.
func (u *uploadBody) Close() error {
	u.finish()
	return nil
}

// slack is added to the per-request timeout so the deadline-based stop happens
// before the transport gives up, keeping a stalled transfer from surfacing as
// an error instead of the partial measurement it actually is.
const slack = 30 * time.Second

// downloadSample streams from url until the measurement window closes or the
// byte guard trips.
//
// Targets that serve a finite payload -- a 100 MB file, or a Cloudflare
// response capped well below what a fast link moves in the window -- would
// otherwise end the sample early and report a window far shorter than the one
// requested. When a response completes before the deadline another request is
// issued, each still on its own fresh connection.
func downloadSample(ctx context.Context, c *HTTPClient, url string, o Opts) (counters, string, string, error) {
	start := time.Now()
	p := newProgress(start, o)

	var (
		proto    string
		remoteIP string
		lastErr  error
	)

	for requests := 0; !p.exhausted() && requests < maxRequestsPerSample; requests++ {
		reqProto, reqIP, err := downloadRequest(ctx, c, url, o, p)
		if reqProto != "" {
			proto = reqProto
		}
		if reqIP != "" {
			remoteIP = reqIP
		}
		if err != nil {
			lastErr = err
			break
		}
	}

	result := p.counters(time.Now())
	if lastErr != nil {
		if p.bytes == 0 {
			return counters{}, proto, remoteIP, lastErr
		}
		result.partial = true
	}

	return result, proto, remoteIP, nil
}

// downloadRequest performs one request, feeding its body into the shared
// progress so the window spans every request in the sample.
func downloadRequest(ctx context.Context, c *HTTPClient, url string, o Opts, p *progress) (string, string, error) {
	var remoteIP string

	reqCtx, cancel := context.WithTimeout(ctx, o.Duration+slack)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	if o.UserAgent != "" {
		req.Header.Set("User-Agent", o.UserAgent)
	}
	// Discourage an intermediary or the origin from serving a cached object:
	// a cache hit measures the cache, not the path.
	req.Header.Set("Cache-Control", "no-store")
	req = attachRemoteIP(req, &remoteIP)

	resp, err := c.Do(req)
	if err != nil {
		return "", remoteIP, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.Proto, remoteIP, errors.New("unexpected status " + resp.Status)
	}

	if _, err := io.Copy(io.Discard, &countingReader{r: resp.Body, p: p}); err != nil {
		return resp.Proto, remoteIP, err
	}
	return resp.Proto, remoteIP, nil
}

// uploadOnce runs a single upload stream and returns its counters.
//
// The upload is sent chunked, because its length is decided by the clock rather
// than known in advance.
//
// A non-2xx response is a hard error rather than a partial result: a server
// that refuses the body (LibreSpeed deployments commonly cap it, answering 413)
// has not measured anything, and reporting a rate from the bytes sent before
// the refusal would be a fabricated number.
func uploadOnce(ctx context.Context, c *HTTPClient, url string, o Opts) (counters, string, string, error) {
	var remoteIP string

	start := time.Now()
	p := newProgress(start, o)
	body := newUploadBody(p)

	reqCtx, cancel := context.WithTimeout(ctx, o.Duration+slack)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, body)
	if err != nil {
		return counters{}, "", "", err
	}
	req.ContentLength = -1 // unknown: the clock decides when to stop
	req.Header.Set("Content-Type", "application/octet-stream")
	if o.UserAgent != "" {
		req.Header.Set("User-Agent", o.UserAgent)
	}
	req.Header.Set("Cache-Control", "no-store")
	req = attachRemoteIP(req, &remoteIP)

	// Sending finished when the body reported EOF; the response arriving later
	// must not be charged to the upload rate.
	endOfBody := func() time.Time {
		if body.doneAt.IsZero() {
			return time.Now()
		}
		return body.doneAt
	}

	resp, err := c.Do(req)
	if err != nil {
		if p.bytes == 0 {
			return counters{}, "", remoteIP, err
		}
		result := p.counters(endOfBody())
		result.partial = true
		return result, "", remoteIP, nil
	}
	defer func() { _ = resp.Body.Close() }()

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return counters{}, resp.Proto, remoteIP, fmt.Errorf("upload rejected: %s", resp.Status)
	}

	return p.counters(endOfBody()), resp.Proto, remoteIP, nil
}

// parallel runs n streams of fn concurrently and aggregates them.
//
// Multi-stream results are reported as a separate series and never mixed into
// the single-stream headline, so the distinction stays visible.
func parallel(n int, fn func() (counters, string, string, error)) (counters, string, string, error) {
	type result struct {
		counters
		proto string
		ip    string
		err   error
	}

	results := make([]result, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, proto, ip, err := fn()
			results[i] = result{counters: c, proto: proto, ip: ip, err: err}
		}(i)
	}
	wg.Wait()

	var (
		agg      counters
		proto    string
		ip       string
		firstErr error
		ok       int
	)
	for _, r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		ok++
		agg.bytes += r.bytes
		agg.steadyBytes += r.steadyBytes
		if r.elapsed > agg.elapsed {
			agg.elapsed = r.elapsed
		}
		if r.steadyFor > agg.steadyFor {
			agg.steadyFor = r.steadyFor
		}
		agg.truncated = agg.truncated || r.truncated
		agg.partial = agg.partial || r.partial
		if proto == "" {
			proto = r.proto
		}
		if ip == "" {
			ip = r.ip
		}
	}

	// A total failure is an error; a partial one is a smaller sample.
	if ok == 0 {
		return counters{}, proto, ip, firstErr
	}
	return agg, proto, ip, nil
}
