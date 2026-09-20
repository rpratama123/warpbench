package trace

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A real capture from the endpoint, trimmed to the fields we parse.
const sampleTrace = `fl=412f362
h=cloudflare.com
ip=114.10.44.41
ts=1789838697.000
visit_scheme=https
uag=curl/8.14.1
colo=SIN
sliver=none
http=http/2
loc=ID
tls=TLSv1.3
sni=plaintext
warp=off
gateway=off
rbi=off
kex=X25519MLKEM768
`

func traceServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The reading must reflect the path now, never a cached copy.
		if cc := r.Header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
			t.Errorf("Cache-Control = %q, want a no-cache directive", cc)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchParsesEveryField(t *testing.T) {
	srv := traceServer(t, sampleTrace, http.StatusOK)

	got, err := Fetch(context.Background(), srv.Client(), srv.URL, "preflight")
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}

	if got.Stage != "preflight" {
		t.Errorf("Stage = %q, want preflight", got.Stage)
	}
	if got.Warp != StateOff {
		t.Errorf("Warp = %q, want off", got.Warp)
	}
	if got.WarpRaw != "off" {
		t.Errorf("WarpRaw = %q, want off", got.WarpRaw)
	}
	if got.Colo != "SIN" {
		t.Errorf("Colo = %q, want SIN", got.Colo)
	}
	if got.IP != "114.10.44.41" {
		t.Errorf("IP = %q", got.IP)
	}
	if got.Loc != "ID" {
		t.Errorf("Loc = %q, want ID", got.Loc)
	}
	if got.HTTP != "http/2" {
		t.Errorf("HTTP = %q", got.HTTP)
	}
	if got.TLS != "TLSv1.3" {
		t.Errorf("TLS = %q", got.TLS)
	}
	if got.Kex != "X25519MLKEM768" {
		t.Errorf("Kex = %q", got.Kex)
	}
	if got.Timestamp != "1789838697.000" {
		t.Errorf("Timestamp = %q", got.Timestamp)
	}
	if got.At.IsZero() {
		t.Error("At is zero; when the reading was taken must be recorded")
	}
	if got.Raw == "" {
		t.Error("Raw is empty; the full response should be kept")
	}
	if got.Err != "" {
		t.Errorf("Err = %q, want empty", got.Err)
	}
	if got.WarpEnabled() {
		t.Error("WarpEnabled() = true for warp=off")
	}
}

func TestParseState(t *testing.T) {
	tests := map[string]State{
		"off":     StateOff,
		"on":      StateOn,
		"plus":    StatePlus,
		"ON":      StateOn,
		" Plus ":  StatePlus,
		"":        StateUnknown,
		"unknown": StateUnknown,
		"partial": StateUnknown,
	}

	for raw, want := range tests {
		if got := parseState(raw); got != want {
			t.Errorf("parseState(%q) = %q, want %q", raw, got, want)
		}
	}
}

// "we could not tell" and "it is off" are different findings.
func TestUnknownStateIsNotConflatedWithOff(t *testing.T) {
	srv := traceServer(t, "colo=SIN\nwarp=\n", http.StatusOK)

	got, err := Fetch(context.Background(), srv.Client(), srv.URL, "preflight")
	if err != nil {
		t.Fatal(err)
	}
	if got.Warp != StateUnknown {
		t.Errorf("Warp = %q, want unknown for a missing warp field", got.Warp)
	}
	if got.Warp == StateOff {
		t.Error("a missing warp field was coerced to off")
	}
}

func TestWarpEnabled(t *testing.T) {
	for state, want := range map[State]bool{StateOn: true, StatePlus: true, StateOff: false, StateUnknown: false} {
		if got := (Result{Warp: state}).WarpEnabled(); got != want {
			t.Errorf("WarpEnabled() for %q = %v, want %v", state, got, want)
		}
	}
}

func TestZeroTrust(t *testing.T) {
	if (Result{Gateway: "off"}).ZeroTrust() {
		t.Error("gateway=off must not count as Zero Trust")
	}
	if !(Result{Gateway: "abc123"}).ZeroTrust() {
		t.Error("a present gateway should count as Zero Trust")
	}
	if (Result{}).ZeroTrust() {
		t.Error("an absent gateway must not count as Zero Trust")
	}
}

// DoH-only mode reports warp=off even when the user believes WARP is on; that
// is the most likely reason for a user being stuck, so it is called out.
func TestExplainCoversTheStuckCases(t *testing.T) {
	tests := map[string]struct {
		result Result
		want   string
	}{
		"unreachable":  {Result{Err: "dial tcp: timeout"}, "could not be reached"},
		"unknown warp": {Result{Warp: StateUnknown, WarpRaw: "weird"}, "does not recognise"},
		"off hints at DoH": {
			Result{Warp: StateOff},
			"DNS-only",
		},
		"plus":       {Result{Warp: StatePlus}, "WARP+"},
		"zero trust": {Result{Warp: StateOn, Gateway: "gw1"}, "Zero Trust"},
		"on is fine": {Result{Warp: StateOn}, ""},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := tc.result.Explain()
			if tc.want == "" {
				if got != "" {
					t.Errorf("Explain() = %q, want empty", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("Explain() = %q, want it to mention %q", got, tc.want)
			}
		})
	}
}

func TestDescribe(t *testing.T) {
	got := Result{Warp: StateOn, Colo: "SIN", Loc: "ID", IP: "203.0.113.9"}.Describe()
	for _, want := range []string{"warp=on", "colo=SIN", "loc=ID", "ip=203.0.113.9"} {
		if !strings.Contains(got, want) {
			t.Errorf("Describe() = %q, want %q", got, want)
		}
	}

	if got := (Result{Err: "boom"}).Describe(); !strings.Contains(got, "trace failed") {
		t.Errorf("Describe() for a failure = %q", got)
	}
}

func TestFetchRecordsTransportFailure(t *testing.T) {
	doer := failingDoer{err: errors.New("dial tcp: connection refused")}

	got, err := Fetch(context.Background(), doer, "https://example.invalid/x", "preflight")
	if err == nil {
		t.Fatal("Fetch() succeeded, want an error")
	}
	if got.Err == "" {
		t.Error("the failure was not recorded on the Result")
	}
	if got.Stage != "preflight" {
		t.Errorf("Stage = %q, want it to survive a failure", got.Stage)
	}
}

type failingDoer struct{ err error }

func (f failingDoer) Do(*http.Request) (*http.Response, error) { return nil, f.err }

func TestFetchRejectsNon200(t *testing.T) {
	srv := traceServer(t, "nope", http.StatusNotFound)

	got, err := Fetch(context.Background(), srv.Client(), srv.URL, "preflight")
	if err == nil {
		t.Fatal("Fetch() accepted a 404")
	}

	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error = %v, want a StatusError", err)
	}
	if statusErr.Code != http.StatusNotFound {
		t.Errorf("Code = %d, want 404", statusErr.Code)
	}
	if got.Err == "" {
		t.Error("the status failure was not recorded on the Result")
	}
}

func TestFetchBoundsTheBody(t *testing.T) {
	big := strings.Repeat("x", maxBodyBytes+1024)
	srv := traceServer(t, big, http.StatusOK)

	got, err := Fetch(context.Background(), srv.Client(), srv.URL, "preflight")
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(got.Raw) > maxBodyBytes {
		t.Errorf("Raw is %d bytes, want at most %d", len(got.Raw), maxBodyBytes)
	}
}

func TestMaskIP(t *testing.T) {
	tests := map[string]string{
		"114.10.44.41":   "114.10.44.0/24",
		"203.0.113.9":    "203.0.113.0/24",
		"2606:4700:1::1": "2606:4700:1::/64",
		"":               "",
		// Unparseable input keeps only its shape, and an address with no colons
		// is reported as an address rather than an IPv6 one.
		"not-an-address": "masked-ip",
	}

	for in, want := range tests {
		if got := MaskIP(in); got != want {
			t.Errorf("MaskIP(%q) = %q, want %q", in, got, want)
		}
	}
}

// A published report must not carry the author's full address.
func TestMaskIPHidesTheHostPart(t *testing.T) {
	masked := MaskIP("114.10.44.41")
	if strings.Contains(masked, "41") && !strings.Contains(masked, "0/24") {
		t.Errorf("MaskIP() = %q, which still contains the host part", masked)
	}
	if !strings.HasSuffix(masked, "/24") {
		t.Errorf("MaskIP() = %q, want a prefix length so the masking is explicit", masked)
	}
}

func TestParseIgnoresMalformedLines(t *testing.T) {
	fields := parse("warp=on\nnonsense\n\ncolo=SIN\n=novalue\n")

	if fields["warp"] != "on" || fields["colo"] != "SIN" {
		t.Errorf("parse() = %v", fields)
	}
	if _, ok := fields["nonsense"]; ok {
		t.Error("a line without '=' should be ignored")
	}
}

// --- endpoint fallback -----------------------------------------------------

// routingDoer serves a trace body for one URL and fails every other.
type routingDoer struct {
	okURL   string
	body    string
	failErr error
	calls   []string
}

func (d *routingDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls = append(d.calls, req.URL.String())
	if d.failErr == nil {
		// An http.Client never returns (nil, nil); make that impossible here so
		// a fixture mistake cannot masquerade as a nil-response panic.
		d.failErr = errors.New("request failed")
	}
	if req.URL.String() == d.okURL {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(d.body)),
			Header:     make(http.Header),
		}, nil
	}
	return nil, d.failErr
}

// The check matters most while WARP is being switched on, which is also when
// the resolver is being reconfigured. A hostname lookup failing there must not
// leave the WARP state unknowable.
func TestFallsBackToTheAddressEndpointWhenTheHostnameFails(t *testing.T) {
	doer := &routingDoer{
		okURL:   FallbackURL,
		body:    "colo=SIN\nwarp=on\nip=203.0.113.9\ngateway=off\n",
		failErr: errors.New("dial tcp: lookup cloudflare.com: i/o timeout"),
	}

	got, err := Fetch(context.Background(), doer, "", "warp-check")
	if err != nil {
		t.Fatalf("Fetch() error = %v, want the fallback to succeed", err)
	}

	if !got.Fallback {
		t.Error("Fallback = false, want the reading marked as coming from the fallback")
	}
	if got.Source != FallbackURL {
		t.Errorf("Source = %q, want %q", got.Source, FallbackURL)
	}
	if got.Warp != StateOn {
		t.Errorf("Warp = %q, want on: the fallback serves the same document", got.Warp)
	}
	if got.Colo != "SIN" || got.IP != "203.0.113.9" {
		t.Errorf("fields not parsed from the fallback: %+v", got)
	}
	if !strings.Contains(got.Describe(), "via-fallback") {
		t.Errorf("Describe() = %q, want it to disclose the fallback", got.Describe())
	}

	if len(doer.calls) != 2 {
		t.Fatalf("made %d calls, want the canonical endpoint then the fallback", len(doer.calls))
	}
	if doer.calls[0] != DefaultURL || doer.calls[1] != FallbackURL {
		t.Errorf("call order = %v", doer.calls)
	}
}

func TestUsesTheCanonicalEndpointWhenItWorks(t *testing.T) {
	doer := &routingDoer{okURL: DefaultURL, body: "colo=SIN\nwarp=off\n"}

	got, err := Fetch(context.Background(), doer, "", "preflight")
	if err != nil {
		t.Fatal(err)
	}
	if got.Fallback {
		t.Error("Fallback = true although the canonical endpoint answered")
	}
	if len(doer.calls) != 1 {
		t.Errorf("made %d calls, want exactly one", len(doer.calls))
	}
}

// An explicit endpoint is the caller's choice and must not be second-guessed.
func TestExplicitEndpointGetsNoFallback(t *testing.T) {
	doer := &routingDoer{okURL: FallbackURL, body: "warp=on\n", failErr: errors.New("boom")}

	_, err := Fetch(context.Background(), doer, "https://example.invalid/trace", "preflight")
	if err == nil {
		t.Fatal("Fetch() succeeded, want the explicit endpoint's failure")
	}
	for _, call := range doer.calls {
		if call == FallbackURL {
			t.Error("an explicit endpoint was silently replaced by the fallback")
		}
	}
}

// When both endpoints fail, the canonical failure is the more useful one.
func TestBothEndpointsFailingReportsTheCanonicalError(t *testing.T) {
	doer := &routingDoer{okURL: "nothing", failErr: errors.New("network is unreachable")}

	got, err := Fetch(context.Background(), doer, "", "warp-check")
	if err == nil {
		t.Fatal("Fetch() succeeded with both endpoints failing")
	}
	if got.Warp != StateUnknown {
		t.Errorf("Warp = %q, want unknown", got.Warp)
	}
	if got.Err == "" {
		t.Error("the failure was not recorded on the Result")
	}
	if len(doer.calls) != 2 {
		t.Errorf("made %d calls, want both endpoints tried", len(doer.calls))
	}
}

func TestFallbackURLCarriesTheFieldsWeParse(t *testing.T) {
	// Every field warpbench reads must be present at the address endpoint too,
	// or the fallback would silently produce a different reading.
	doer := &routingDoer{
		okURL: FallbackURL,
		body:  "colo=SIN\nwarp=plus\nip=203.0.113.9\ngateway=gw1\nloc=ID\nts=1\nhttp=http/2\ntls=TLSv1.3\nkex=X\nrbi=off\nvisit_scheme=https\n",
	}

	got, err := Fetch(context.Background(), doer, "", "warp-check")
	if err != nil {
		t.Fatal(err)
	}

	checks := map[string]string{
		"warp": got.WarpRaw, "colo": got.Colo, "ip": got.IP, "gateway": got.Gateway,
		"loc": got.Loc, "ts": got.Timestamp, "http": got.HTTP, "tls": got.TLS,
		"kex": got.Kex, "rbi": got.RBI, "visit_scheme": got.VisitScheme,
	}
	for field, value := range checks {
		if value == "" {
			t.Errorf("%s was not parsed from the fallback response", field)
		}
	}
}
