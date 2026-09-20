// Package iperf3 manages the pinned external iperf3 binary.
//
// iperf3 is used as a real subprocess rather than reimplemented in Go: a
// benchmark's credibility rests on using the reference implementation, and
// numbers produced by a bespoke client are exactly what a sceptical reader
// would question.
//
// Upstream (ESnet) publishes no binaries at all, so this uses a third-party
// static build. That build publishes no checksums either, which means the only
// meaningful verification is a hash pinned in this source file: a hash fetched
// from the same place as the binary would prove nothing.
//
// To refresh the pin, download the new assets and update the table below:
//
//	for a in iperf3-amd64 iperf3-arm64v8 iperf3-amd64-osx-15 \
//	         iperf3-arm64-osx-14 iperf3-amd64-win.zip; do
//	  curl -sSL -o "$a" \
//	    "https://github.com/userdocs/iperf3-static/releases/download/<ver>/$a"
//	  sha256sum "$a"
//	done
package iperf3

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Version is the pinned upstream release.
const Version = "3.21"

// releaseBase is where the pinned assets live.
const releaseBase = "https://github.com/userdocs/iperf3-static/releases/download/" + Version

// maxAssetBytes bounds a download: the largest pinned asset is under 8 MiB.
const maxAssetBytes = 64 << 20

// Doer is the subset of http.Client this package needs, so tests can serve
// assets locally.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

type asset struct {
	name   string
	sha256 string
	// binary is the file to return, relative to the install directory. For a
	// plain download that is the installed name; for an archive it is a member.
	binary  string
	archive bool
	// members are the files to extract from an archive, in order. The Windows
	// build is a Cygwin binary that needs cygwin1.dll next to it, so extracting
	// only the executable would produce something that cannot start.
	members []string
}

// assets is the pin table. Hashes were computed from the release assets named
// above; see the package comment for how to refresh them.
var assets = map[string]asset{
	"linux/amd64": {
		binary: "iperf3",
		name:   "iperf3-amd64",
		sha256: "201cbaed73d4e4da72c44c9aee895a2d58c75f1d1a4d721137f4888f6b7f5016",
	},
	"linux/arm64": {
		binary: "iperf3",
		name:   "iperf3-arm64v8",
		sha256: "2ce83dceb64fe08cea59926647f7ca20a7cb7baa8320e9648823d9b7de562c8e",
	},
	"darwin/amd64": {
		binary: "iperf3",
		name:   "iperf3-amd64-osx-15",
		sha256: "9168916504291356a0779b401052db14c7f4fd93fa657154ea8e60e80cbaf279",
	},
	"darwin/arm64": {
		binary: "iperf3",
		name:   "iperf3-arm64-osx-14",
		sha256: "c7c8945847f5228c428b7d1975243c185e4710fbdeb66ce69e87169d76e14d38",
	},
	"windows/amd64": {
		binary:  "iperf3.exe",
		name:    "iperf3-amd64-win.zip",
		sha256:  "913d9aac883f53c2f8c63ab3adcd7c8b00ceceae768e8d03a5a93c75d09c42b4",
		archive: true,
		members: []string{"iperf3.exe", "cygwin1.dll"},
	},
}

// ErrUnsupported is returned on platforms with no pinned build. That is a real
// gap rather than a bug: windows/arm64 has no upstream asset.
var ErrUnsupported = errors.New("no pinned iperf3 build for this platform")

// AssetURL returns the download URL for a platform, and whether one exists.
func AssetURL(goos, goarch string) (string, bool) {
	a, ok := assets[goos+"/"+goarch]
	if !ok {
		return "", false
	}
	return releaseBase + "/" + a.name, true
}

// Ensure returns the path to a verified iperf3 executable, downloading it into
// cacheDir on first use.
//
// The cached copy is re-hashed on every call rather than trusted after a
// one-time check, because the whole point of pinning a hash is that it is
// checked against the bytes actually about to be executed.
func Ensure(ctx context.Context, cacheDir string, doer Doer) (string, error) {
	a, ok := assets[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("%w: %s/%s", ErrUnsupported, runtime.GOOS, runtime.GOARCH)
	}
	return ensureAsset(ctx, filepath.Join(cacheDir, "iperf3", Version), a, doer)
}

// ensureAsset installs one asset into dir and returns the executable's path.
func ensureAsset(ctx context.Context, dir string, a asset, doer Doer) (string, error) {
	binPath := filepath.Join(dir, a.binary)

	if info, err := os.Stat(binPath); err == nil && info.Mode().IsRegular() {
		if err := verifyFile(binPath, a.sha256); err == nil {
			return binPath, nil
		}
		// A cached binary that no longer matches the pin is removed rather than
		// left to be picked up by something else.
		_ = os.Remove(binPath)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}

	body, err := fetch(ctx, doer, releaseBase+"/"+a.name)
	if err != nil {
		return "", err
	}
	defer func() { _ = body.Close() }()

	data, err := io.ReadAll(io.LimitReader(body, maxAssetBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", a.name, err)
	}
	if len(data) > maxAssetBytes {
		return "", fmt.Errorf("asset %s exceeds %d bytes", a.name, maxAssetBytes)
	}

	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != a.sha256 {
		return "", fmt.Errorf("checksum mismatch for %s: expected %s, got %s", a.name, a.sha256, got)
	}

	if a.archive {
		if err := extract(data, dir, a.members); err != nil {
			return "", err
		}
	} else {
		if err := writeExecutable(binPath, data); err != nil {
			return "", err
		}
	}

	return binPath, nil
}

func fetch(ctx context.Context, doer Doer, url string) (io.ReadCloser, error) {
	if doer == nil {
		doer = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("downloading %s: unexpected status %s", url, resp.Status)
	}
	return resp.Body, nil
}

func verifyFile(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("cached %s has hash %s, want %s", filepath.Base(path), got, want)
	}
	return nil
}

func writeExecutable(path string, data []byte) error {
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o700); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("installing %s: %w", path, err)
	}
	return nil
}

// extract unpacks the named members of a zip into dir.
func extract(data []byte, dir string, members []string) error {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("reading archive: %w", err)
	}

	available := make(map[string]*zip.File, len(reader.File))
	for _, f := range reader.File {
		available[filepath.Base(f.Name)] = f
	}

	for _, name := range members {
		f, ok := available[name]
		if !ok {
			return fmt.Errorf("archive does not contain %s", name)
		}

		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("opening %s in archive: %w", name, err)
		}

		content, err := io.ReadAll(io.LimitReader(rc, maxAssetBytes))
		_ = rc.Close()
		if err != nil {
			return fmt.Errorf("reading %s from archive: %w", name, err)
		}

		mode := os.FileMode(0o600)
		if strings.HasSuffix(name, ".exe") || strings.HasSuffix(name, ".dll") {
			mode = 0o700
		}
		if err := os.WriteFile(filepath.Join(dir, name), content, mode); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
	}

	return nil
}

// Run executes the pinned iperf3 and returns its stdout.
//
// stderr is captured separately and included in the error, because iperf3
// reports "the server is busy" and similar conditions there rather than through
// a non-zero exit status.
func Run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return stdout.Bytes(), fmt.Errorf("iperf3 failed: %w: %s", err, firstLine(msg))
		}
		return stdout.Bytes(), fmt.Errorf("iperf3 failed: %w", err)
	}

	return stdout.Bytes(), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Result is the subset of iperf3's -J output this tool relies on.
type Result struct {
	Start struct {
		Connected []struct {
			RemoteHost string `json:"remote_host"`
			RemotePort int    `json:"remote_port"`
		} `json:"connected"`
		TestStart struct {
			Timestamp struct {
				Timesecs float64 `json:"timesecs"`
			} `json:"timestamp"`
		} `json:"test_start"`
	} `json:"start"`

	End struct {
		SumSent     Sum `json:"sum_sent"`
		SumReceived Sum `json:"sum_received"`
	} `json:"end"`

	Error string `json:"error"`
}

// Sum is one direction's summary block.
type Sum struct {
	Bytes         int64   `json:"bytes"`
	Seconds       float64 `json:"seconds"`
	BitsPerSecond float64 `json:"bits_per_second"`
}

// RemoteHost returns the address of the server actually contacted, so a route
// or anycast change between phases is visible.
func (r Result) RemoteHost() string {
	if len(r.Start.Connected) == 0 {
		return ""
	}
	return r.Start.Connected[0].RemoteHost
}

// Download returns the server-to-client summary, which is what -R measures.
func (r Result) Download() Sum { return r.End.SumReceived }

// Upload returns the client-to-server summary, which is the default direction.
func (r Result) Upload() Sum { return r.End.SumSent }
