package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rpratama123/warpbench/internal/serverlist"
)

// readAllLimited reads a request body without trusting its length.
func readAllLimited(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, 1<<20))
}

// writeTemp writes a server list to a temporary file and returns its path.
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "servers.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func strptr(s string) *string { return &s }

func hasKind(checks []check, serverID, kind string) bool {
	for _, c := range checks {
		if c.server == serverID && c.kind == kind {
			return true
		}
	}
	return false
}

// An iperf3 server has no HTTP endpoints, so it must get a DNS check and a TCP
// check -- and specifically must NOT get an upload check, which would fail for
// lack of a URL.
func TestBuildChecksForIPerf3(t *testing.T) {
	servers := []serverlist.Server{{
		ID:           "sg-iperf",
		Protocol:     "iperf3",
		PingHost:     "iperf.example.com",
		Capabilities: []string{"ping", "download", "upload"},
		IPerf3:       &serverlist.IPerf3{Host: "iperf.example.com", PortRange: []int{5201, 5210}},
	}}

	checks := buildChecks(servers)

	if !hasKind(checks, "sg-iperf", "dns") {
		t.Error("iperf3 server has no DNS check")
	}
	if !hasKind(checks, "sg-iperf", "iperf3") {
		t.Error("iperf3 server has no TCP check; it would go completely unvalidated")
	}
	if hasKind(checks, "sg-iperf", "upload") {
		t.Error("iperf3 server got an HTTP upload check, but it has no upload URL")
	}
	if hasKind(checks, "sg-iperf", "download") {
		t.Error("iperf3 server got an HTTP download check, but it has no download URL")
	}

	for _, c := range checks {
		if c.kind == "iperf3" && c.target != "iperf.example.com:5201" {
			t.Errorf("iperf3 target = %q, want the lower bound of the range", c.target)
		}
	}
}

func TestBuildChecksForHTTPFile(t *testing.T) {
	servers := []serverlist.Server{{
		ID:           "sg-file",
		Protocol:     "http-file",
		PingHost:     "file.example.com",
		DownloadURL:  strptr("https://file.example.com/100MB.bin"),
		Capabilities: []string{"ping", "download"},
	}}

	checks := buildChecks(servers)

	for _, want := range []string{"dns", "download"} {
		if !hasKind(checks, "sg-file", want) {
			t.Errorf("missing %s check", want)
		}
	}
	if hasKind(checks, "sg-file", "upload") {
		t.Error("download-only server got an upload check")
	}
	for _, c := range checks {
		if c.kind == "download" && c.target != "https://file.example.com/100MB.bin" {
			t.Errorf("download target = %q, want the stored URL unchanged", c.target)
		}
	}
}

// LibreSpeed and Cloudflare base URLs omit the size parameter, so the probe must
// add one or the endpoint returns an error instead of a payload.
func TestDownloadProbeURLAddsSizeParameter(t *testing.T) {
	tests := map[string]struct {
		server serverlist.Server
		want   string
	}{
		"librespeed": {
			server: serverlist.Server{Protocol: "librespeed", DownloadURL: strptr("https://ls.example.com/backend/garbage.php")},
			want:   "https://ls.example.com/backend/garbage.php?ckSize=1",
		},
		"cloudflare": {
			server: serverlist.Server{Protocol: "cloudflare", DownloadURL: strptr("https://speed.cloudflare.com/__down")},
			want:   "https://speed.cloudflare.com/__down?bytes=1000",
		},
		"http-file untouched": {
			server: serverlist.Server{Protocol: "http-file", DownloadURL: strptr("https://f.example.com/100MB.bin")},
			want:   "https://f.example.com/100MB.bin",
		},
		"existing query preserved": {
			server: serverlist.Server{Protocol: "librespeed", DownloadURL: strptr("https://ls.example.com/g.php?ckSize=1")},
			want:   "https://ls.example.com/g.php?ckSize=1",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := downloadProbeURL(tc.server); got != tc.want {
				t.Errorf("downloadProbeURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProbeDownloadAcceptsPartialContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Range"); got != "bytes=0-1023" {
			t.Errorf("Range header = %q, want a bounded request", got)
		}
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "warpbench-validate/") {
			t.Errorf("User-Agent = %q, want the validator to identify itself", ua)
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer srv.Close()

	if err := probeDownload(srv.URL)(context.Background(), srv.Client()); err != nil {
		t.Errorf("probeDownload() error = %v, want nil for 206", err)
	}
}

func TestProbeDownloadRejectsErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if err := probeDownload(srv.URL)(context.Background(), srv.Client()); err == nil {
		t.Error("probeDownload() accepted a 404")
	}
}

func TestProbeUploadSendsBoundedBody(t *testing.T) {
	var gotLen int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAllLimited(r)
		gotLen = int64(len(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := probeUpload(srv.URL)(context.Background(), srv.Client()); err != nil {
		t.Fatalf("probeUpload() error = %v", err)
	}
	if gotLen != uploadProbeBytes {
		t.Errorf("uploaded %d bytes, want exactly %d", gotLen, uploadProbeBytes)
	}
}

func TestProbeUploadRejectsErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if err := probeUpload(srv.URL)(context.Background(), srv.Client()); err == nil {
		t.Error("probeUpload() accepted a 500")
	}
}

// The worker pool must preserve input order so weekly output is diffable.
func TestExecutePreservesOrder(t *testing.T) {
	checks := make([]check, 12)
	for i := range checks {
		i := i
		checks[i] = check{
			server: string(rune('a' + i)),
			kind:   "dns",
			target: "x",
			run: func(context.Context, *http.Client) error {
				time.Sleep(time.Duration(12-i) * time.Millisecond)
				return nil
			},
		}
	}

	results := execute(checks, 4, time.Second, &http.Client{})

	for i, r := range results {
		if want := string(rune('a' + i)); r.check.server != want {
			t.Errorf("results[%d].server = %q, want %q (order not preserved)", i, r.check.server, want)
		}
	}
}

func TestExecuteRecordsFailures(t *testing.T) {
	checks := []check{{
		server: "x", kind: "dns", target: "x",
		run: func(context.Context, *http.Client) error { return context.DeadlineExceeded },
	}}

	results := execute(checks, 1, time.Second, &http.Client{})

	if len(results) != 1 || results[0].err == nil {
		t.Fatalf("execute() did not record the failure: %+v", results)
	}
}

func TestFirstPort(t *testing.T) {
	tests := map[string]struct {
		in   []int
		want int
	}{
		"range":    {[]int{5201, 5210}, 5201},
		"single":   {[]int{5201, 5201}, 5201},
		"reversed": {[]int{5210, 5201}, 5201},
		"empty":    {nil, 0},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := firstPort(tc.in); got != tc.want {
				t.Errorf("firstPort(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestSortedIDs(t *testing.T) {
	got := sortedIDs([]serverlist.Server{{ID: "b"}, {ID: "a"}, {ID: "c"}})
	want := []string{"a", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortedIDs() = %v, want %v", got, want)
		}
	}
}

func TestRunRejectsListWithDroppedEntries(t *testing.T) {
	// One good entry and one that the loader must drop.
	list := `{"schema":2,"revision":"2026-09-20","groups":[{"id":"sg","name":"S"}],"servers":[
		{"id":"sg-ok","group":"sg","name":"OK","protocol":"http-file","ping_host":"e.com",
		 "download_url":"https://e.com/f","capabilities":["ping","download"],"tier":"quick"},
		{"id":"bad","group":"sg","name":"Bad","protocol":"ftp","ping_host":"e.com",
		 "capabilities":["ping"],"tier":"quick"}]}`

	path := writeTemp(t, list)

	var out strings.Builder
	err := run(path, time.Second, 2, false, &out)

	if err == nil {
		t.Fatal("run() accepted a list with a dropped entry")
	}
	if !strings.Contains(out.String(), "bad") {
		t.Errorf("output = %q, want it to name the dropped entry", out.String())
	}
}
