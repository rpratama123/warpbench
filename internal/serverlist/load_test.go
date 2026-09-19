package serverlist

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validList is a complete, schema-valid document.
func validList() []byte {
	return listWith(goodServer)
}

// serveList starts a server returning body with the given status.
func serveList(t *testing.T, status int, body []byte, headers map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLoadFromRemote(t *testing.T) {
	srv := serveList(t, http.StatusOK, validList(), map[string]string{"ETag": `"abc123"`})
	cache := t.TempDir()

	res, err := Load(context.Background(), Options{RemoteURL: srv.URL, CacheDir: cache})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if res.Source != SourceRemote {
		t.Errorf("Source = %q, want %q", res.Source, SourceRemote)
	}
	if res.Origin != srv.URL {
		t.Errorf("Origin = %q, want %q", res.Origin, srv.URL)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", res.Warnings)
	}

	// A successful fetch must populate the cache so a later failure still works.
	if _, err := os.Stat(cachePath(cache)); err != nil {
		t.Errorf("cache was not written: %v", err)
	}
	if got := readETag(cache); got != `"abc123"` {
		t.Errorf("stored ETag = %q, want %q", got, `"abc123"`)
	}
}

func TestLoadFallsBackToCacheWhenRemoteFails(t *testing.T) {
	cache := t.TempDir()
	writeCache(cache, validList())

	srv := serveList(t, http.StatusInternalServerError, nil, nil)

	res, err := Load(context.Background(), Options{RemoteURL: srv.URL, CacheDir: cache})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if res.Source != SourceCache {
		t.Errorf("Source = %q, want %q", res.Source, SourceCache)
	}
	if !hasWarning(res.Warnings, "falling back") {
		t.Errorf("Warnings = %v, want a fallback notice", res.Warnings)
	}
}

// A 304 means our cached copy is current, so the fetch must not be treated as a
// failure.
func TestLoadUsesCacheOnNotModified(t *testing.T) {
	cache := t.TempDir()
	writeCache(cache, validList())
	writeETag(cache, `"abc123"`)

	var gotIfNoneMatch string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIfNoneMatch = r.Header.Get("If-None-Match")
		w.WriteHeader(http.StatusNotModified)
	}))
	t.Cleanup(srv.Close)

	res, err := Load(context.Background(), Options{RemoteURL: srv.URL, CacheDir: cache})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if gotIfNoneMatch != `"abc123"` {
		t.Errorf("If-None-Match = %q, want the cached ETag", gotIfNoneMatch)
	}
	if res.Source != SourceCache {
		t.Errorf("Source = %q, want %q", res.Source, SourceCache)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("Warnings = %v; a 304 is success, not a fallback", res.Warnings)
	}
}

func TestLoadFallsBackToEmbedded(t *testing.T) {
	srv := serveList(t, http.StatusInternalServerError, nil, nil)
	cache := t.TempDir() // deliberately empty

	res, err := Load(context.Background(), Options{RemoteURL: srv.URL, CacheDir: cache})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if res.Source != SourceEmbedded {
		t.Errorf("Source = %q, want %q", res.Source, SourceEmbedded)
	}
	if len(res.List.Servers) == 0 {
		t.Error("embedded fallback produced an empty list")
	}
	if !hasWarning(res.Warnings, "falling back") {
		t.Errorf("Warnings = %v, want a fallback notice", res.Warnings)
	}
}

func TestLoadOfflineSkipsRemote(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(validList())
	}))
	t.Cleanup(srv.Close)

	res, err := Load(context.Background(), Options{RemoteURL: srv.URL, CacheDir: t.TempDir(), Offline: true})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if hit {
		t.Error("Offline still contacted the remote")
	}
	if res.Source != SourceEmbedded {
		t.Errorf("Source = %q, want %q", res.Source, SourceEmbedded)
	}
}

// An unusable remote document must fall through rather than be trusted: a
// malformed list is not a partial success.
func TestLoadFallsThroughOnUnusableRemote(t *testing.T) {
	cache := t.TempDir()
	writeCache(cache, validList())

	srv := serveList(t, http.StatusOK, []byte(`{"schema":1}`), nil)

	res, err := Load(context.Background(), Options{RemoteURL: srv.URL, CacheDir: cache})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if res.Source != SourceCache {
		t.Errorf("Source = %q, want %q after an unusable remote document", res.Source, SourceCache)
	}
	if !hasWarning(res.Warnings, "unusable") {
		t.Errorf("Warnings = %v, want a notice about the unusable remote list", res.Warnings)
	}
}

func TestLoadOverrideFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.json")
	if err := os.WriteFile(path, validList(), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := Load(context.Background(), Options{Override: path, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if res.Source != SourceOverride {
		t.Errorf("Source = %q, want %q", res.Source, SourceOverride)
	}
	if res.Origin != path {
		t.Errorf("Origin = %q, want %q", res.Origin, path)
	}
}

func TestLoadOverrideURL(t *testing.T) {
	srv := serveList(t, http.StatusOK, validList(), nil)

	res, err := Load(context.Background(), Options{Override: srv.URL, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if res.Source != SourceOverride {
		t.Errorf("Source = %q, want %q", res.Source, SourceOverride)
	}
}

func TestLoadOverrideErrorsAreFatal(t *testing.T) {
	tests := map[string]Options{
		"missing file": {Override: filepath.Join(t.TempDir(), "nope.json")},
		"bad json":     {},
	}

	badJSON := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(badJSON, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests["bad json"] = Options{Override: badJSON}

	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			opts.CacheDir = t.TempDir()
			// An explicit override must fail loudly, never silently fall back.
			if _, err := Load(context.Background(), opts); err == nil {
				t.Error("Load() succeeded, want an error")
			}
		})
	}
}

// An oversized body is a remote fetch failure, so it must be discarded and the
// chain must continue rather than the run failing outright.
func TestLoadRejectsOversizedBody(t *testing.T) {
	big := make([]byte, (4<<20)+16)
	for i := range big {
		big[i] = 'x'
	}
	srv := serveList(t, http.StatusOK, big, nil)

	res, err := Load(context.Background(), Options{RemoteURL: srv.URL, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if res.Source != SourceEmbedded {
		t.Errorf("Source = %q, want %q: an oversized list must not be used", res.Source, SourceEmbedded)
	}
	if !hasWarning(res.Warnings, "falling back") {
		t.Errorf("Warnings = %v, want a fallback notice", res.Warnings)
	}
}

// Per-entry skipping must survive the whole chain, not just parse().
func TestLoadRecordsEntryWarningsFromRemote(t *testing.T) {
	bad := `{"id":"bad","group":"sg","name":"X","protocol":"ftp","ping_host":"e.com","capabilities":["ping"],"tier":"quick"}`
	srv := serveList(t, http.StatusOK, listWith(goodServer, bad), nil)

	res, err := Load(context.Background(), Options{RemoteURL: srv.URL, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if res.Source != SourceRemote {
		t.Errorf("Source = %q, want %q", res.Source, SourceRemote)
	}
	if len(res.List.Servers) != 1 {
		t.Errorf("kept %d servers, want 1", len(res.List.Servers))
	}
	if !hasWarning(res.Warnings, "bad") {
		t.Errorf("Warnings = %v, want one naming the dropped entry", res.Warnings)
	}
}

func TestLoadSendsUserAgent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(validList())
	}))
	t.Cleanup(srv.Close)

	if _, err := Load(context.Background(), Options{
		RemoteURL: srv.URL,
		CacheDir:  t.TempDir(),
		UserAgent: "warpbench/test",
	}); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got != "warpbench/test" {
		t.Errorf("User-Agent = %q, want %q", got, "warpbench/test")
	}
}

// A cache we cannot write must not break a run; it should only cost us the
// cache.
func TestWriteCacheFailureIsNotFatal(t *testing.T) {
	srv := serveList(t, http.StatusOK, validList(), nil)

	// A regular file where the cache directory should be makes MkdirAll fail.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := Load(context.Background(), Options{
		RemoteURL: srv.URL,
		CacheDir:  filepath.Join(blocker, "cache"),
	})
	if err != nil {
		t.Fatalf("Load() error = %v, want success despite an unwritable cache", err)
	}
	if res.Source != SourceRemote {
		t.Errorf("Source = %q, want %q", res.Source, SourceRemote)
	}
}

func TestLoadHonoursContextCancellation(t *testing.T) {
	srv := serveList(t, http.StatusOK, validList(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Load(ctx, Options{RemoteURL: srv.URL, CacheDir: t.TempDir(), Offline: true})
	// Offline never dials, so this must still succeed from the embedded copy.
	if err != nil {
		t.Fatalf("offline load with a cancelled context: %v", err)
	}
}

func TestRevisionOfHandlesNil(t *testing.T) {
	var r *Result
	if got := r.RevisionOf(); got != "unknown" {
		t.Errorf("nil Result.RevisionOf() = %q, want unknown", got)
	}
	if got := (&Result{}).RevisionOf(); got != "unknown" {
		t.Errorf("empty Result.RevisionOf() = %q, want unknown", got)
	}
}

func TestMarshalForCacheRoundTrips(t *testing.T) {
	res := mustParse(t, embeddedList)

	data, err := MarshalForCache(res.List)
	if err != nil {
		t.Fatalf("MarshalForCache: %v", err)
	}

	again, err := parse(data, SourceCache, "roundtrip")
	if err != nil {
		t.Fatalf("re-parsing marshalled list: %v", err)
	}
	if len(again.List.Servers) != len(res.List.Servers) {
		t.Errorf("round trip changed the server count: %d -> %d", len(res.List.Servers), len(again.List.Servers))
	}
}

func hasWarning(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func TestRemoteURLEmptyFallsBackToDefault(t *testing.T) {
	if got := remoteURL(Options{}); got != DefaultURL {
		t.Errorf("remoteURL(Options{}) = %q, want DefaultURL", got)
	}
	if got := remoteURL(Options{RemoteURL: "http://x/y"}); got != "http://x/y" {
		t.Errorf("remoteURL override = %q", got)
	}
}

func TestCacheDirResolution(t *testing.T) {
	if got := cacheDir(Options{CacheDir: "/tmp/x"}); got != "/tmp/x" {
		t.Errorf("cacheDir with override = %q", got)
	}
	if got := cachePath(""); got != "" {
		t.Errorf("cachePath(\"\") = %q, want empty", got)
	}
	if got := etagPath(""); got != "" {
		t.Errorf("etagPath(\"\") = %q, want empty", got)
	}
	if got := readETag(""); got != "" {
		t.Errorf("readETag(\"\") = %q, want empty", got)
	}

	// These must be no-ops rather than panics when there is no cache dir.
	writeCache("", []byte("x"))
	writeETag("", "x")

	if _, err := readCache(""); err == nil {
		t.Error("readCache(\"\") should error")
	}
}

func TestIsURL(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"https://example.com/x", true},
		{"http://example.com/x", true},
		{"/tmp/servers.json", false},
		{"servers.json", false},
		{"ftp://example.com", false},
	} {
		if got := isURL(tc.in); got != tc.want {
			t.Errorf("isURL(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
