package throughput

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rpratama123/warpbench/internal/iperf3"
	"github.com/rpratama123/warpbench/internal/serverlist"
)

// These exercise the real adapters against the real servers in servers.json,
// including downloading and running the pinned iperf3 binary. They are opt-in
// because they need network access and take tens of seconds.
//
// Run with:
//
//	WARPBENCH_INTEGRATION=1 go test ./internal/throughput/ -run Integration -v
func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("WARPBENCH_INTEGRATION") == "" {
		t.Skip("set WARPBENCH_INTEGRATION=1 to run network integration tests")
	}
}

func loadShippedList(t *testing.T) *serverlist.List {
	t.Helper()

	res, err := serverlist.Load(context.Background(), serverlist.Options{
		Override: filepath.Join("..", "..", "servers.json"),
		Offline:  true,
	})
	if err != nil {
		t.Fatalf("loading servers.json: %v", err)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("servers.json produced warnings: %v", res.Warnings)
	}
	return res.List
}

func findServer(t *testing.T, list *serverlist.List, id string) serverlist.Server {
	t.Helper()
	for _, s := range list.Servers {
		if s.ID == id {
			return s
		}
	}
	t.Skipf("server %s is not in servers.json", id)
	return serverlist.Server{}
}

// smokeDeps builds the dependencies the adapters need, including a real iperf3
// binary resolved (and verified) into a temporary cache.
func smokeDeps(t *testing.T) Deps {
	t.Helper()
	cache := t.TempDir()

	return Deps{
		Client:    NewHTTPClient(0),
		UserAgent: "warpbench-smoke/1.0 (+https://github.com/rpratama123/warpbench)",
		IPerf3Bin: func(ctx context.Context) (string, error) {
			return iperf3.Ensure(ctx, cache, http.DefaultClient)
		},
	}
}

func smokeOpts() Opts {
	return Opts{
		Duration: 4 * time.Second,
		Warmup:   1 * time.Second,
		MaxBytes: 200 << 20,
		MinBytes: 1 << 20,
		Parallel: 1,
	}
}

func logSample(t *testing.T, label string, s Sample) {
	t.Helper()
	t.Logf("%-28s %-8s steady=%7.2f Mbps overall=%7.2f Mbps bytes=%9d elapsed=%v proto=%s ip=%s undersized=%v",
		label, s.Metric, s.SteadyMbps, s.OverallMbps, s.Bytes, s.Elapsed.Round(time.Millisecond), s.Proto, s.RemoteIP, s.Undersized)
}

func TestIntegrationSmokeDownloadEveryProtocol(t *testing.T) {
	requireIntegration(t)

	list := loadShippedList(t)
	deps := smokeDeps(t)
	ctx := context.Background()

	// One target per protocol, chosen to cover all four adapters.
	for _, id := range []string{"sg-linode", "id-cf-cgk", "eu-ls-ams-clouvider", "id-myrepublic-iperf3"} {
		srv := findServer(t, list, id)
		t.Run(id, func(t *testing.T) {
			adapter, err := New(srv, deps)
			if err != nil {
				t.Fatalf("New(%s): %v", id, err)
			}

			got, err := adapter.Download(ctx, smokeOpts())
			if err != nil {
				t.Fatalf("Download(%s): %v", id, err)
			}
			logSample(t, id, got)

			if got.Bytes <= 0 {
				t.Error("no bytes were transferred")
			}
			if got.SteadyMbps <= 0 {
				t.Error("steady rate is zero")
			}
			if got.Truncated {
				t.Error("the byte guard ended the sample; the window should have")
			}
		})
	}
}

func TestIntegrationSmokeUploadWhereSupported(t *testing.T) {
	requireIntegration(t)

	list := loadShippedList(t)
	deps := smokeDeps(t)
	ctx := context.Background()

	// Upload is the metric the whole iperf3 decision was made for, so the
	// Indonesian iperf3 target is the important case here.
	for _, id := range []string{"id-myrepublic-iperf3", "sg-leaseweb-iperf3", "eu-ls-prg-cesnet", "id-cf-cgk"} {
		srv := findServer(t, list, id)
		t.Run(id, func(t *testing.T) {
			adapter, err := New(srv, deps)
			if err != nil {
				t.Fatalf("New(%s): %v", id, err)
			}
			if !adapter.Caps().Upload {
				t.Fatalf("%s does not advertise upload", id)
			}

			got, err := adapter.Upload(ctx, smokeOpts())
			if err != nil {
				t.Fatalf("Upload(%s): %v", id, err)
			}
			logSample(t, id, got)

			if got.Bytes <= 0 {
				t.Error("no bytes were uploaded")
			}
			if got.SteadyMbps <= 0 {
				t.Error("steady rate is zero")
			}
		})
	}
}

// The iperf3 binary is downloaded, hash-verified and executed for real.
func TestIntegrationIPerf3BinaryResolves(t *testing.T) {
	requireIntegration(t)

	cache := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	path, err := iperf3.Ensure(ctx, cache, http.DefaultClient)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	t.Logf("pinned iperf3 %s installed at %s (%d bytes)", iperf3.Version, path, info.Size())

	// A second call must reuse the verified copy.
	callsBefore := info.ModTime()
	again, err := iperf3.Ensure(ctx, cache, http.DefaultClient)
	if err != nil {
		t.Fatalf("second Ensure() error = %v", err)
	}
	if again != path {
		t.Errorf("second Ensure() returned %q, want %q", again, path)
	}
	after, _ := os.Stat(again)
	if !after.ModTime().Equal(callsBefore) {
		t.Error("the cached binary was rewritten instead of reused")
	}

	out, err := iperf3.Run(ctx, path, "--version")
	if err != nil {
		t.Fatalf("running the pinned binary: %v", err)
	}
	t.Logf("binary reports: %s", firstLineOf(string(out)))
}

func firstLineOf(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
