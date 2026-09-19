// Package serverlist loads, validates and caches the curated list of public
// measurement targets.
//
// Resolution order is remote -> cache -> embedded, with an explicit override
// (--servers <path|url>) bypassing all three. The embedded copy is compiled into
// the binary so warpbench still works with no network at all.
package serverlist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rpratama123/warpbench/internal/config"
)

// DefaultURL is the canonical list, served from the repository's main branch so
// that adding or retiring a target needs no release.
const DefaultURL = "https://raw.githubusercontent.com/rpratama123/warpbench/main/servers.json"

// fetchTimeout bounds the remote fetch, per PLAN.md section 4.
const fetchTimeout = 30 * time.Second

// Source identifies where a list came from. It is recorded in every report: a
// result measured against an embedded list of unknown age is weaker evidence
// than one measured against a dated remote revision.
type Source string

const (
	SourceRemote   Source = "remote"
	SourceCache    Source = "cache"
	SourceEmbedded Source = "embedded"
	SourceOverride Source = "override"
)

// List is a parsed servers.json document.
type List struct {
	Schema   int      `json:"schema"`
	Revision string   `json:"revision"`
	Groups   []Group  `json:"groups"`
	Servers  []Server `json:"servers"`
}

// Group is a geographic grouping. Groups are data, not code: the TUI derives its
// grouping from this array, so a new one needs no release.
type Group struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Server is one measurement target.
type Server struct {
	ID           string       `json:"id"`
	Group        string       `json:"group"`
	Name         string       `json:"name"`
	Provider     string       `json:"provider,omitempty"`
	City         string       `json:"city,omitempty"`
	Country      string       `json:"country,omitempty"`
	Protocol     string       `json:"protocol"`
	PingHost     string       `json:"ping_host"`
	DownloadURL  *string      `json:"download_url"`
	UploadURL    *string      `json:"upload_url"`
	Capabilities []string     `json:"capabilities"`
	Tier         string       `json:"tier"`
	Flags        []string     `json:"flags,omitempty"`
	IPerf3       *IPerf3      `json:"iperf3,omitempty"`
	AdapterOpts  *AdapterOpts `json:"adapter_opts,omitempty"`
	Notes        string       `json:"notes,omitempty"`
}

// IPerf3 carries the iperf3-specific addressing. Public servers advertise a port
// range because individual ports are frequently busy.
type IPerf3 struct {
	Host            string `json:"host"`
	PortRange       []int  `json:"port_range"`
	ReverseDownload bool   `json:"reverse_download"`
}

// AdapterOpts tunes a protocol adapter per server.
type AdapterOpts struct {
	// MinBytes flags an undersized sample instead of silently averaging a
	// measurement too short to be meaningful.
	MinBytes int64 `json:"min_bytes,omitempty"`
	// MaxBytes is a runaway guard only; sample duration governs the window.
	MaxBytes int64 `json:"max_bytes,omitempty"`
}

// Has reports whether the server declares a capability.
func (s Server) Has(capability string) bool {
	for _, c := range s.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

// String renders "id (name)" for diagnostics.
func (s Server) String() string { return s.ID + " (" + s.Name + ")" }

// Options configures Load.
type Options struct {
	// Override is the --servers value: a filesystem path or an http(s) URL.
	// When set, the remote -> cache -> embedded chain is bypassed.
	Override string
	// RemoteURL overrides the canonical list URL. Unlike Override, the
	// remote -> cache -> embedded chain still applies. Used for self-hosting
	// and by tests.
	RemoteURL string
	// CacheDir overrides the default per-user cache location.
	CacheDir string
	// Client is used for remote fetches; a 30s-timeout client is used if nil.
	Client *http.Client
	// Offline skips the remote fetch, starting at the cache.
	Offline bool
	// UserAgent identifies us to the raw.githubusercontent.com endpoint.
	UserAgent string
}

// Result is a loaded list plus provenance.
type Result struct {
	List     *List
	Source   Source
	Origin   string
	Revision string
	// Warnings records every non-fatal problem: a failed remote fetch, or a
	// server entry that was skipped. They belong in the report.
	Warnings []string
}

// Load resolves the server list. It only returns an error when every source
// failed, so a caller can always count on having a usable list when err == nil.
func Load(ctx context.Context, opts Options) (*Result, error) {
	if opts.Override != "" {
		return loadOverride(ctx, opts)
	}

	var warnings []string

	if !opts.Offline {
		data, err := fetchRemote(ctx, opts)
		switch {
		case err == nil:
			res, perr := parse(data, SourceRemote, remoteURL(opts))
			if perr == nil {
				writeCache(cacheDir(opts), data) // best effort; a failure is not fatal
				return res, nil
			}
			warnings = append(warnings, fmt.Sprintf("remote server list is unusable: %v", perr))
		case errors.Is(err, errNotModified):
			if cached, cerr := readCache(cacheDir(opts)); cerr == nil {
				if res, perr := parse(cached, SourceCache, cachePath(cacheDir(opts))); perr == nil {
					res.Warnings = append(warnings, res.Warnings...)
					return res, nil
				}
			}
			warnings = append(warnings, "remote list unchanged but no usable cache entry")
		default:
			warnings = append(warnings, fmt.Sprintf("could not fetch the server list (%v); falling back", err))
		}
	}

	if cached, err := readCache(cacheDir(opts)); err == nil {
		if res, perr := parse(cached, SourceCache, cachePath(cacheDir(opts))); perr == nil {
			res.Warnings = append(warnings, res.Warnings...)
			return res, nil
		}
		warnings = append(warnings, "cached server list is unusable; using the embedded copy")
	}

	res, err := parse(embeddedList, SourceEmbedded, "embedded:servers.json")
	if err != nil {
		return nil, fmt.Errorf("embedded server list is unusable, which is a build bug: %w", err)
	}
	res.Warnings = append(warnings, res.Warnings...)
	return res, nil
}

func loadOverride(ctx context.Context, opts Options) (*Result, error) {
	if isURL(opts.Override) {
		data, err := httpGet(ctx, opts, opts.Override, "")
		if err != nil {
			return nil, fmt.Errorf("fetching --servers %s: %w", opts.Override, err)
		}
		return parse(data, SourceOverride, opts.Override)
	}

	data, err := os.ReadFile(opts.Override)
	if err != nil {
		return nil, fmt.Errorf("reading --servers %s: %w", opts.Override, err)
	}
	return parse(data, SourceOverride, opts.Override)
}

var errNotModified = errors.New("not modified")

// remoteURL returns the list URL to fetch.
func remoteURL(opts Options) string {
	if opts.RemoteURL != "" {
		return opts.RemoteURL
	}
	return DefaultURL
}

// fetchRemote retrieves the list, sending If-None-Match when a cached ETag
// exists. It returns errNotModified when the server answers 304.
func fetchRemote(ctx context.Context, opts Options) ([]byte, error) {
	etag := readETag(cacheDir(opts))
	data, err := httpGet(ctx, opts, remoteURL(opts), etag)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func httpGet(ctx context.Context, opts Options, url, etag string) ([]byte, error) {
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if opts.UserAgent != "" {
		req.Header.Set("User-Agent", opts.UserAgent)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotModified {
		return nil, errNotModified
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}

	// Bound the read: the list is a few tens of KB, and a hostile or broken
	// endpoint should not be able to exhaust memory.
	const maxListBytes = 4 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxListBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxListBytes {
		return nil, fmt.Errorf("server list exceeds %d bytes", maxListBytes)
	}

	if tag := resp.Header.Get("ETag"); tag != "" {
		writeETag(cacheDir(opts), tag)
	}
	return body, nil
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// --- cache -----------------------------------------------------------------

func cacheDir(opts Options) string {
	if opts.CacheDir != "" {
		return opts.CacheDir
	}
	dir, err := config.CacheDir()
	if err != nil {
		return ""
	}
	return dir
}

func cachePath(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "servers.json")
}

func etagPath(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "servers.json.etag")
}

func readCache(dir string) ([]byte, error) {
	path := cachePath(dir)
	if path == "" {
		return nil, errors.New("no cache directory")
	}
	return os.ReadFile(path)
}

// writeCache stores the list. Failures are deliberately swallowed by callers:
// an unwritable cache should degrade performance, never break a run.
func writeCache(dir string, data []byte) {
	path := cachePath(dir)
	if path == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		_ = os.Remove(tmp)
		return
	}
	_ = os.Rename(tmp, path)
}

func readETag(dir string) string {
	path := etagPath(dir)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func writeETag(dir, tag string) {
	path := etagPath(dir)
	if path == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(tag), 0o600)
}

// RevisionOf is a small helper for report headers.
func (r *Result) RevisionOf() string {
	if r == nil || r.List == nil {
		return "unknown"
	}
	return r.List.Revision
}

// GroupByID indexes groups by id.
func (l *List) GroupByID() map[string]Group {
	out := make(map[string]Group, len(l.Groups))
	for _, g := range l.Groups {
		out[g.ID] = g
	}
	return out
}

// Selected returns the servers in the given groups, or all of them when groups
// is empty.
func (l *List) Selected(groups []string) []Server {
	if len(groups) == 0 {
		return l.Servers
	}
	want := make(map[string]bool, len(groups))
	for _, g := range groups {
		want[g] = true
	}
	var out []Server
	for _, s := range l.Servers {
		if want[s.Group] {
			out = append(out, s)
		}
	}
	return out
}

// ByTier returns the servers in the given tier.
func (l *List) ByTier(tier string) []Server {
	var out []Server
	for _, s := range l.Servers {
		if s.Tier == tier {
			out = append(out, s)
		}
	}
	return out
}

// MarshalForCache is used by tests to round-trip a list.
func MarshalForCache(l *List) ([]byte, error) { return json.Marshal(l) }
