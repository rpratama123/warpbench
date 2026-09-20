// Package results defines the on-disk result format.
//
// The types here are deliberately separate from the internal measurement types:
// this is a published, versioned artefact that other tools and later versions
// read, so it must not shift every time an internal struct is refactored. The
// JSON schema in schema/results.schema.json is the contract, and Load enforces
// it.
//
// Durations are milliseconds as floats. A raw Go duration string would be
// precise but unreadable, and every consumer wants to plot or compare the
// numbers rather than re-parse them.
package results

import (
	"time"
)

// SchemaVersion is the version of the document this package writes. Bump only
// for a breaking change; readers reject anything they do not understand.
const SchemaVersion = 1

// File is one phase's complete result.
type File struct {
	Schema        int           `json:"schema"`
	Tool          Tool          `json:"tool"`
	Phase         string        `json:"phase"`
	StartedAt     time.Time     `json:"started_at"`
	EndedAt       time.Time     `json:"ended_at"`
	DurationSec   float64       `json:"duration_seconds"`
	Configuration Configuration `json:"configuration"`
	ServerList    ServerListRef `json:"server_list"`
	Environment   Environment   `json:"environment"`
	Traces        []Trace       `json:"traces"`
	Servers       []Server      `json:"servers"`
	Warnings      []string      `json:"warnings"`
}

// Tool identifies the build that produced the file.
type Tool struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Go      string `json:"go"`
}

// Configuration is the effective configuration, recorded in full so a run can
// be reproduced from the file alone.
type Configuration struct {
	Mode         string   `json:"mode"`
	Groups       []string `json:"groups"`
	ServerIDs    []string `json:"server_ids"`
	Parallel     int      `json:"parallel"`
	IPv6         bool     `json:"ipv6"`
	Masked       bool     `json:"masked"`
	DownloadSecs float64  `json:"download_seconds"`
	UploadSecs   float64  `json:"upload_seconds"`
	DownloadRuns int      `json:"download_samples"`
	UploadRuns   int      `json:"upload_samples"`
	PingCount    int      `json:"ping_count"`
	TimingRuns   int      `json:"timing_samples"`
	WarmupSecs   float64  `json:"warmup_seconds"`
	MinBytes     int64    `json:"min_bytes"`
	MaxBytes     int64    `json:"max_bytes"`
}

// ServerListRef records which server list was measured against. A report
// without this cannot be attributed to a set of targets, which makes the result
// unreproducible.
type ServerListRef struct {
	Schema   int    `json:"schema"`
	Revision string `json:"revision"`
	Source   string `json:"source"`
	Origin   string `json:"origin"`
}

// Environment records where the run happened.
type Environment struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Go        string `json:"go"`
	LocalTime string `json:"local_time"`
	Timezone  string `json:"timezone"`
}

// Trace is one WARP-state reading.
type Trace struct {
	Stage       string `json:"stage"`
	At          string `json:"at"`
	Warp        string `json:"warp"`
	WarpRaw     string `json:"warp_raw"`
	Colo        string `json:"colo"`
	IP          string `json:"ip"`
	Gateway     string `json:"gateway"`
	Loc         string `json:"loc"`
	HTTP        string `json:"http"`
	TLS         string `json:"tls"`
	Kex         string `json:"kex"`
	RBI         string `json:"rbi"`
	VisitScheme string `json:"visit_scheme"`
	Timestamp   string `json:"ts"`
	Err         string `json:"err"`
}

// Server is one measured target.
type Server struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Group      string `json:"group"`
	Protocol   string `json:"protocol"`
	Provider   string `json:"provider"`
	City       string `json:"city"`
	Country    string `json:"country"`
	ResolvedIP string `json:"resolved_ip"`
	// Flags are copied from the server list, so the report can explain a target
	// rather than inferring its nature from its id.
	Flags    []string `json:"flags,omitempty"`
	Ping     *Ping    `json:"ping"`
	Timings  *Timings `json:"timings"`
	Download *Series  `json:"download"`
	Upload   *Series  `json:"upload"`
	// Warnings carries per-server problems: an unusable entry, an ICMP
	// substitution, a skipped metric.
	Warnings []string `json:"warnings"`
}

// Ping is a summarised latency run.
type Ping struct {
	Method   string  `json:"method"`
	Target   string  `json:"target"`
	Sent     int     `json:"sent"`
	Received int     `json:"received"`
	LossPct  float64 `json:"loss_pct"`
	MinMs    float64 `json:"min_ms"`
	AvgMs    float64 `json:"avg_ms"`
	MedianMs float64 `json:"median_ms"`
	MaxMs    float64 `json:"max_ms"`
	P95Ms    float64 `json:"p95_ms"`
	StdDevMs float64 `json:"stddev_ms"`
	JitterMs float64 `json:"jitter_ms"`
	// Warning explains a substitution, such as falling back to TCP connect
	// because ICMP is unavailable. It must reach the report.
	Warning string `json:"warning"`
}

// HasRTT reports whether the ping produced at least one reply to measure.
//
// A ping that received nothing is not a measurement of zero latency, it is the
// absence of a measurement, and the two must not be confused: the probe is a
// TCP connect to port 443, so a target that does not listen there answers
// nothing in either phase. Reporting that as 0.0 ms, or as 100% packet loss,
// would state something about the network that was never observed -- and
// because the value is zero it would also drag any median towards "no change".
func (p *Ping) HasRTT() bool {
	return p != nil && p.Received > 0
}

// Timings is a summarised connection-setup measurement.
type Timings struct {
	DNSMs        float64 `json:"dns_ms"`
	ConnectMs    float64 `json:"connect_ms"`
	TLSMs        float64 `json:"tls_ms"`
	TTFBMs       float64 `json:"ttfb_ms"`
	TotalMs      float64 `json:"total_ms"`
	RemoteIP     string  `json:"remote_ip"`
	Proto        string  `json:"proto"`
	TLSVersion   string  `json:"tls_version"`
	Samples      int     `json:"samples"`
	DNSRan       bool    `json:"dns_ran"`
	ConnectRan   bool    `json:"connect_ran"`
	TLSRan       bool    `json:"tls_ran"`
	FirstByteRan bool    `json:"first_byte_ran"`
}

// Series is a set of throughput samples for one direction.
//
// Every raw sample is kept, not just the summary: a reader who distrusts the
// headline must be able to recompute it, and a later version must be able to
// re-render without re-measuring.
type Series struct {
	Metric            string   `json:"metric"`
	Samples           []Sample `json:"samples"`
	MedianSteadyMbps  float64  `json:"median_steady_mbps"`
	MedianOverallMbps float64  `json:"median_overall_mbps"`
	// Parallel records the stream count this series was measured with. A
	// multi-stream result is a different measurement and must never be silently
	// merged with a single-stream one.
	Parallel int `json:"parallel"`
}

// Sample is one transfer.
type Sample struct {
	Bytes       int64   `json:"bytes"`
	ElapsedMs   float64 `json:"elapsed_ms"`
	SteadyBytes int64   `json:"steady_bytes"`
	SteadyForMs float64 `json:"steady_for_ms"`
	SteadyMbps  float64 `json:"steady_mbps"`
	OverallMbps float64 `json:"overall_mbps"`
	// Parallel is omitted when unset: zero is not a stream count, and the
	// schema requires at least one whenever the field is present.
	Parallel   int    `json:"parallel,omitempty"`
	Proto      string `json:"proto"`
	RemoteIP   string `json:"remote_ip"`
	Undersized bool   `json:"undersized"`
	Truncated  bool   `json:"truncated"`
	Partial    bool   `json:"partial"`
	Error      string `json:"error"`
}

// HasMeasurement reports whether the file contains anything worth comparing.
func (f *File) HasMeasurement() bool {
	for _, s := range f.Servers {
		if s.Download != nil || s.Upload != nil || s.Ping != nil {
			return true
		}
	}
	return false
}

// ServerByID indexes the servers in a file.
func (f *File) ServerByID() map[string]Server {
	out := make(map[string]Server, len(f.Servers))
	for _, s := range f.Servers {
		out[s.ID] = s
	}
	return out
}

// Ms converts a duration to the milliseconds the format uses.
func Ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// Secs converts a duration to seconds.
func Secs(d time.Duration) float64 { return d.Seconds() }
