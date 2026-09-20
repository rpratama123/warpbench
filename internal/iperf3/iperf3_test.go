package iperf3

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeDoer serves canned bytes so the download path can be exercised without
// touching the network.
type fakeDoer struct {
	body   []byte
	status int
	calls  int
	err    error
}

func (f *fakeDoer) Do(*http.Request) (*http.Response, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	status := f.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:       io.NopCloser(bytes.NewReader(f.body)),
		Header:     make(http.Header),
	}, nil
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// --- pin table -------------------------------------------------------------

// Every platform build we claim to support must be pinned with a real hash. An
// empty or malformed entry would silently disable verification.
func TestEveryAssetIsPinned(t *testing.T) {
	if len(assets) == 0 {
		t.Fatal("the asset table is empty")
	}

	for platform, a := range assets {
		t.Run(platform, func(t *testing.T) {
			if len(a.sha256) != 64 {
				t.Errorf("sha256 = %q, want 64 hex characters", a.sha256)
			}
			if _, err := hex.DecodeString(a.sha256); err != nil {
				t.Errorf("sha256 is not valid hex: %v", err)
			}
			if a.name == "" {
				t.Error("name is empty")
			}
			if a.binary == "" {
				t.Error("binary is empty")
			}
			if a.archive && len(a.members) == 0 {
				t.Error("an archive asset must name the members to extract")
			}
		})
	}
}

func TestAssetURL(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64"} {
		url, ok := AssetURL(strings.Split(platform, "/")[0], strings.Split(platform, "/")[1])
		if !ok {
			t.Errorf("AssetURL(%s) reported no build", platform)
			continue
		}
		if !strings.Contains(url, Version) {
			t.Errorf("AssetURL(%s) = %q, want it to reference version %s", platform, url, Version)
		}
	}

	// windows/arm64 genuinely has no upstream build; that must be reported
	// rather than papered over.
	if _, ok := AssetURL("windows", "arm64"); ok {
		t.Error("AssetURL(windows/arm64) claimed a build, but upstream publishes none")
	}
}

// The Windows asset is a Cygwin build, so the DLL must be extracted alongside
// the executable or it cannot start.
func TestWindowsAssetExtractsTheCygwinDLL(t *testing.T) {
	a, ok := assets["windows/amd64"]
	if !ok {
		t.Fatal("windows/amd64 is not pinned")
	}

	want := map[string]bool{"iperf3.exe": false, "cygwin1.dll": false}
	for _, m := range a.members {
		if _, ok := want[m]; ok {
			want[m] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("windows asset does not extract %s", name)
		}
	}
}

// --- Ensure ----------------------------------------------------------------

func TestEnsureDownloadsAndVerifies(t *testing.T) {
	payload := []byte("#!/bin/sh\necho iperf3\n")
	a := asset{name: "iperf3-test", sha256: hashOf(payload), binary: "iperf3"}
	doer := &fakeDoer{body: payload}
	dir := t.TempDir()

	got, err := ensureAsset(context.Background(), dir, a, doer)
	if err != nil {
		t.Fatalf("ensureAsset() error = %v", err)
	}

	if got != filepath.Join(dir, "iperf3") {
		t.Errorf("path = %q, want %q", got, filepath.Join(dir, "iperf3"))
	}
	onDisk, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, payload) {
		t.Error("installed bytes do not match the downloaded asset")
	}
	if doer.calls != 1 {
		t.Errorf("downloaded %d times, want 1", doer.calls)
	}
}

// A cached copy is re-hashed rather than trusted, so a second call must not
// re-download.
func TestEnsureUsesVerifiedCache(t *testing.T) {
	payload := []byte("binary")
	a := asset{name: "iperf3-test", sha256: hashOf(payload), binary: "iperf3"}
	dir := t.TempDir()

	doer := &fakeDoer{body: payload}
	if _, err := ensureAsset(context.Background(), dir, a, doer); err != nil {
		t.Fatal(err)
	}

	second := &fakeDoer{body: payload}
	if _, err := ensureAsset(context.Background(), dir, a, second); err != nil {
		t.Fatal(err)
	}

	if second.calls != 0 {
		t.Errorf("re-downloaded %d times despite a valid cache", second.calls)
	}
}

func TestEnsureRejectsChecksumMismatch(t *testing.T) {
	a := asset{name: "iperf3-test", sha256: hashOf([]byte("expected")), binary: "iperf3"}
	doer := &fakeDoer{body: []byte("something else entirely")}
	dir := t.TempDir()

	_, err := ensureAsset(context.Background(), dir, a, doer)
	if err == nil {
		t.Fatal("ensureAsset() accepted a checksum mismatch")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error = %v, want it to name the checksum mismatch", err)
	}

	// A binary that failed verification must not be left behind.
	if _, statErr := os.Stat(filepath.Join(dir, "iperf3")); statErr == nil {
		t.Error("a binary that failed verification was left on disk")
	}
}

// A tampered cache entry must be replaced, not executed.
func TestEnsureReplacesCorruptedCache(t *testing.T) {
	payload := []byte("the real binary")
	a := asset{name: "iperf3-test", sha256: hashOf(payload), binary: "iperf3"}
	dir := t.TempDir()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "iperf3"), []byte("tampered"), 0o700); err != nil {
		t.Fatal(err)
	}

	doer := &fakeDoer{body: payload}
	got, err := ensureAsset(context.Background(), dir, a, doer)
	if err != nil {
		t.Fatalf("ensureAsset() error = %v", err)
	}
	if doer.calls != 1 {
		t.Errorf("downloaded %d times, want 1 after a corrupted cache hit", doer.calls)
	}

	onDisk, _ := os.ReadFile(got)
	if !bytes.Equal(onDisk, payload) {
		t.Error("the corrupted cache entry was not replaced")
	}
}

func TestEnsureRejectsHTTPErrorStatus(t *testing.T) {
	a := asset{name: "iperf3-test", sha256: hashOf([]byte("x")), binary: "iperf3"}
	doer := &fakeDoer{body: []byte("x"), status: http.StatusNotFound}

	if _, err := ensureAsset(context.Background(), t.TempDir(), a, doer); err == nil {
		t.Fatal("ensureAsset() accepted a 404")
	} else if !strings.Contains(err.Error(), "404") {
		t.Errorf("error = %v, want it to mention the status", err)
	}
}

func TestEnsureFileIsExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("execute bits are not meaningful on Windows")
	}

	payload := []byte("binary")
	a := asset{name: "iperf3-test", sha256: hashOf(payload), binary: "iperf3"}
	got, err := ensureAsset(context.Background(), t.TempDir(), a, &fakeDoer{body: payload})
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("mode = %v, want the owner execute bit set", info.Mode().Perm())
	}
}

// --- archive extraction ----------------------------------------------------

func makeZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestEnsureExtractsArchiveMembers(t *testing.T) {
	archive := makeZip(t, map[string][]byte{
		"iperf3.exe":  []byte("the executable"),
		"cygwin1.dll": []byte("the runtime"),
	})

	a := asset{
		name:    "iperf3-amd64-win.zip",
		sha256:  hashOf(archive),
		binary:  "iperf3.exe",
		archive: true,
		members: []string{"iperf3.exe", "cygwin1.dll"},
	}

	dir := t.TempDir()
	got, err := ensureAsset(context.Background(), dir, a, &fakeDoer{body: archive})
	if err != nil {
		t.Fatalf("ensureAsset() error = %v", err)
	}

	if filepath.Base(got) != "iperf3.exe" {
		t.Errorf("returned %q, want iperf3.exe", got)
	}
	for _, name := range []string{"iperf3.exe", "cygwin1.dll"} {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s was not extracted: %v", name, err)
			continue
		}
		if len(content) == 0 {
			t.Errorf("%s is empty", name)
		}
	}
}

func TestExtractFailsOnMissingMember(t *testing.T) {
	archive := makeZip(t, map[string][]byte{"iperf3.exe": []byte("exe")})

	err := extract(archive, t.TempDir(), []string{"iperf3.exe", "cygwin1.dll"})
	if err == nil {
		t.Fatal("extract() succeeded without the required DLL")
	}
	if !strings.Contains(err.Error(), "cygwin1.dll") {
		t.Errorf("error = %v, want it to name the missing member", err)
	}
}

func TestExtractRejectsNonZip(t *testing.T) {
	if err := extract([]byte("not a zip"), t.TempDir(), []string{"iperf3.exe"}); err == nil {
		t.Fatal("extract() accepted non-zip data")
	}
}

// --- subprocess ------------------------------------------------------------

func TestRunReportsStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}

	_, err := Run(context.Background(), "/bin/sh", "-c", "echo 'the server is busy' >&2; exit 1")
	if err == nil {
		t.Fatal("Run() succeeded on a failing command")
	}
	if !strings.Contains(err.Error(), "the server is busy") {
		t.Errorf("error = %v, want iperf3's own message, which it writes to stderr", err)
	}
}

func TestRunReturnsStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}

	out, err := Run(context.Background(), "/bin/sh", "-c", "echo hello")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.TrimSpace(string(out)) != "hello" {
		t.Errorf("stdout = %q, want hello", out)
	}
}

func TestRunMissingBinary(t *testing.T) {
	if _, err := Run(context.Background(), "/nonexistent/iperf3", "-v"); err == nil {
		t.Fatal("Run() succeeded with a missing binary")
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("one\ntwo"); got != "one" {
		t.Errorf("firstLine() = %q, want one", got)
	}
	if got := firstLine("only"); got != "only" {
		t.Errorf("firstLine() = %q, want only", got)
	}
}

// --- JSON parsing ----------------------------------------------------------

// Captured from iperf3 -J, trimmed to the fields this tool reads.
const sampleJSON = `{
  "start": {
    "connected": [{"socket": 5, "remote_host": "203.0.113.9", "remote_port": 5201}],
    "test_start": {"timestamp": {"timesecs": 1789838697}}
  },
  "end": {
    "sum_sent":     {"bytes": 12500000, "seconds": 1.0, "bits_per_second": 100000000},
    "sum_received": {"bytes": 25000000, "seconds": 1.0, "bits_per_second": 200000000}
  }
}`

func TestResultParsing(t *testing.T) {
	var got Result
	if err := json.Unmarshal([]byte(sampleJSON), &got); err != nil {
		t.Fatalf("parsing: %v", err)
	}

	if got.Upload().Bytes != 12_500_000 {
		t.Errorf("Upload().Bytes = %d, want sum_sent", got.Upload().Bytes)
	}
	if got.Download().Bytes != 25_000_000 {
		t.Errorf("Download().Bytes = %d, want sum_received", got.Download().Bytes)
	}
	if got.RemoteHost() != "203.0.113.9" {
		t.Errorf("RemoteHost() = %q, want the connected host", got.RemoteHost())
	}
}

func TestResultRemoteHostEmpty(t *testing.T) {
	var got Result
	if got.RemoteHost() != "" {
		t.Errorf("RemoteHost() = %q, want empty when nothing is connected", got.RemoteHost())
	}
}

// GitHub serves release assets through a redirect, so a client that refuses
// redirects fails here. The error must say so rather than reporting a bare 302.
func TestFetchExplainsARefusedRedirect(t *testing.T) {
	a := asset{name: "iperf3-test", sha256: hashOf([]byte("x")), binary: "iperf3"}
	doer := &fakeDoer{body: []byte(""), status: http.StatusFound}

	_, err := ensureAsset(context.Background(), t.TempDir(), a, doer)
	if err == nil {
		t.Fatal("ensureAsset() accepted a 302")
	}
	if !strings.Contains(err.Error(), "does not follow redirects") {
		t.Errorf("error = %v, want it to explain the redirect problem", err)
	}
	if !strings.Contains(err.Error(), "302") {
		t.Errorf("error = %v, want it to name the status", err)
	}
}
