// Package trace reads Cloudflare's connection-trace endpoint to determine
// whether WARP is actually on.
//
// The whole comparison rests on the two phases really being ISP-direct and
// WARP-tunnelled. Asking the user to flip a switch and trusting that it worked
// would make every result unfalsifiable, so the state is read from Cloudflare
// itself and recorded at four points per run.
//
// Endpoint notes, established by probing:
//   - GET works; HEAD returns 404, so a HEAD-based check would always look like
//     a failure.
//   - speed.cloudflare.com/meta returns 403 and is not used.
package trace

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultURL is the canonical trace endpoint.
const DefaultURL = "https://cloudflare.com/cdn-cgi/trace"

// FallbackURL serves the same document from the resolver address itself.
//
// It exists because this check matters most at the moment WARP is being
// switched on, which is also when the resolver is being reconfigured and a
// hostname lookup can fail. A user reported exactly that: "lookup
// cloudflare.com: i/o timeout" on the pause screen. The address endpoint needs
// no DNS and returns an identical field set.
//
// It is the fallback rather than the default because some ISPs block 1.1.1.1
// outright, and cloudflare.com is the path less likely to be interfered with.
const FallbackURL = "https://1.1.1.1/cdn-cgi/trace"

// maxBodyBytes bounds the response. The real document is a couple of hundred
// bytes; anything much larger is not the trace endpoint.
const maxBodyBytes = 64 << 10

// State is the interpreted value of the warp field.
type State string

const (
	// StateOff means traffic is not going through WARP.
	StateOff State = "off"
	// StateOn means WARP is on.
	StateOn State = "on"
	// StatePlus means WARP+ (the paid tier).
	StatePlus State = "plus"
	// StateUnknown covers any other value, including a missing field. It is
	// deliberately distinct from StateOff: "we could not tell" and "it is off"
	// are different findings and must not be conflated.
	StateUnknown State = "unknown"
)

// Result is one trace reading.
type Result struct {
	// Stage names where in the run this was taken: preflight, before-baseline,
	// before-warp or after-warp.
	Stage string
	// At is when the reading was taken.
	At time.Time

	// Warp is the interpreted state and WarpRaw the literal field, kept so a
	// value we do not recognise is visible rather than lost.
	Warp    State
	WarpRaw string

	Colo        string
	IP          string
	Gateway     string
	Loc         string
	Timestamp   string
	HTTP        string
	TLS         string
	Kex         string
	RBI         string
	VisitScheme string

	// Err records a failed reading. A trace that could not be taken is itself
	// worth recording, because it changes what the run can claim.
	Err string

	// Raw is the full response body, so a field not yet parsed is not lost.
	Raw string

	// Source is the URL this reading came from. It differs from the canonical
	// endpoint only when the fallback was needed, which is worth knowing.
	Source string
	// Fallback records that the canonical hostname could not be reached.
	Fallback bool
}

// Doer is the subset of http.Client this package needs.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Fetch reads the trace endpoint, falling back to a DNS-free address if the
// canonical hostname cannot be reached.
//
// A transport failure is returned as an error; the caller decides whether that
// is fatal (it usually is not, but it must be recorded).
func Fetch(ctx context.Context, doer Doer, url, stage string) (Result, error) {
	// An explicit endpoint is the caller's choice and must not be
	// second-guessed by a silent substitution.
	explicit := url != "" && url != DefaultURL
	if url == "" {
		url = DefaultURL
	}
	if doer == nil {
		doer = &http.Client{Timeout: 10 * time.Second}
	}

	result, err := fetchOnce(ctx, doer, url, stage)
	if err == nil || explicit {
		return result, err
	}

	// The canonical hostname failed. The most likely cause is the one this
	// check exists to survive: the resolver being reconfigured while WARP is
	// switched on, where a hostname lookup times out but an address still
	// works.
	fallback, fallbackErr := fetchOnce(ctx, doer, FallbackURL, stage)
	if fallbackErr == nil {
		fallback.Fallback = true
		return fallback, nil
	}

	// Both failed. The canonical failure is the more useful of the two.
	return result, err
}

// fetchOnce performs a single attempt against one endpoint.
func fetchOnce(ctx context.Context, doer Doer, url, stage string) (Result, error) {
	// Default to unknown rather than the zero value: if the fetch fails before
	// parsing, the state is genuinely unknown, and "" is not a valid state.
	result := Result{Stage: stage, At: time.Now(), Warp: StateUnknown, Source: url}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		result.Err = err.Error()
		return result, err
	}
	// The trace must reflect the path right now, never a cached copy.
	req.Header.Set("Cache-Control", "no-cache, no-store")
	req.Header.Set("Pragma", "no-cache")

	resp, err := doer.Do(req)
	if err != nil {
		result.Err = err.Error()
		return result, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		result.Err = err.Error()
		return result, err
	}
	if resp.StatusCode != http.StatusOK {
		result.Err = "unexpected status " + resp.Status
		return result, &StatusError{Status: resp.Status, Code: resp.StatusCode}
	}

	result.Raw = string(body)
	result.apply(parse(result.Raw))

	return result, nil
}

// StatusError reports a non-200 trace response.
type StatusError struct {
	Status string
	Code   int
}

func (e *StatusError) Error() string { return "trace endpoint returned " + e.Status }

// parse splits the "key=value" lines.
func parse(body string) map[string]string {
	fields := make(map[string]string)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return fields
}

func (r *Result) apply(fields map[string]string) {
	r.WarpRaw = fields["warp"]
	r.Warp = parseState(r.WarpRaw)

	r.Colo = fields["colo"]
	r.IP = fields["ip"]
	r.Gateway = fields["gateway"]
	r.Loc = fields["loc"]
	r.Timestamp = fields["ts"]
	r.HTTP = fields["http"]
	r.TLS = fields["tls"]
	r.Kex = fields["kex"]
	r.RBI = fields["rbi"]
	r.VisitScheme = fields["visit_scheme"]
}

// parseState interprets the warp field, keeping anything unrecognised visible
// as unknown rather than coercing it to off.
func parseState(raw string) State {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "off":
		return StateOff
	case "on":
		return StateOn
	case "plus":
		return StatePlus
	default:
		return StateUnknown
	}
}

// WarpEnabled reports whether this reading shows WARP carrying traffic.
func (r Result) WarpEnabled() bool {
	return r.Warp == StateOn || r.Warp == StatePlus
}

// ZeroTrust reports whether a gateway is present, which means the connection is
// routed by a Cloudflare Zero Trust policy rather than plain WARP.
func (r Result) ZeroTrust() bool {
	return r.Gateway != "" && r.Gateway != "off"
}

// Describe renders a one-line summary for progress output and reports.
func (r Result) Describe() string {
	if r.Err != "" {
		return "trace failed: " + r.Err
	}
	parts := []string{"warp=" + string(r.Warp)}
	if r.Fallback {
		parts = append(parts, "via-fallback")
	}
	if r.Colo != "" {
		parts = append(parts, "colo="+r.Colo)
	}
	if r.Loc != "" {
		parts = append(parts, "loc="+r.Loc)
	}
	if r.ZeroTrust() {
		parts = append(parts, "gateway="+r.Gateway)
	}
	if r.IP != "" {
		parts = append(parts, "ip="+r.IP)
	}
	return strings.Join(parts, " ")
}

// Explain returns guidance for a state that would block a run, or "" when the
// state is fine for its purpose. This is what the pause screen shows a stuck
// user, and it exists because the DoH-only mode in particular reports warp=off
// even when the user has "turned WARP on".
func (r Result) Explain() string {
	switch {
	case r.Err != "":
		return "The trace endpoint could not be reached, so WARP state is unknown. Check connectivity and retry."
	case r.Warp == StateUnknown:
		return "The trace endpoint reported warp=" + r.WarpRaw + ", which this version does not recognise. Treating the state as unknown."
	case r.ZeroTrust() && r.WarpEnabled():
		return "WARP is on and a gateway is present, so traffic is routed by a Cloudflare Zero Trust policy."
	case r.Warp == StatePlus:
		return "WARP+ is active."
	case r.Warp == StateOff:
		return "WARP reports off. If you believe it is on, check whether the client is in DNS-only (DoH) mode: that mode encrypts DNS but does not tunnel traffic, and it reports warp=off."
	default:
		return ""
	}
}

// MaskIP hides the host part of an address while keeping enough of it to
// recognise the network.
//
// Saved reports are published, and a full public IP is both a privacy leak and
// a fingerprint of the author's connection.
func MaskIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}

	if parsed := net.ParseIP(ip); parsed != nil {
		if v4 := parsed.To4(); v4 != nil {
			return strings.Join([]string{
				strconv.Itoa(int(v4[0])), strconv.Itoa(int(v4[1])), strconv.Itoa(int(v4[2])), "0",
			}, ".") + "/24"
		}

		// IPv6: keep the routing prefix, drop the interface identifier. Masking
		// on the byte representation rather than on the text form avoids the
		// compressed-notation trap ("2606:4700:1::1" must not become
		// "2606:4700:1::::").
		if v6 := parsed.To16(); v6 != nil {
			masked := make(net.IP, net.IPv6len)
			copy(masked, v6[:8])
			return masked.String() + "/64"
		}
		return parsed.String()
	}

	// Not an address we can parse; keep only its shape.
	if strings.Contains(ip, ":") {
		return "masked-ipv6"
	}
	return "masked-ip"
}
