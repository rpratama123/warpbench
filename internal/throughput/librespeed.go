package throughput

import (
	"context"
	"strconv"

	"github.com/rpratama123/warpbench/internal/serverlist"
)

// LibreSpeed's protocol is a small set of PHP endpoints:
//
//	GET  garbage.php?ckSize=N   streams N megabytes of pseudo-random data
//	POST empty.php              accepts and discards a body
//	GET  empty.php              returns an empty 200, used for latency
//
// ckSize is in megabytes, so the request below is deliberately far larger than
// the byte guard: the clock ends the sample, not the server.
const (
	libreSpeedChunkMB    = 512
	libreSpeedProbeChunk = 1
)

// libreSpeed measures servers running the open-source LibreSpeed backend.
//
// These are the preferred targets where they exist, because they support upload
// and the protocol is trivial to implement correctly. They do not exist in
// APAC: the canonical community list has one APAC server and it returns 403,
// which is why iperf3 carries APAC upload instead.
type libreSpeed struct {
	server serverlist.Server
	deps   Deps
}

func newLibreSpeed(server serverlist.Server, deps Deps) *libreSpeed {
	return &libreSpeed{server: server, deps: deps}
}

func (a *libreSpeed) ID() string { return "librespeed" }

func (a *libreSpeed) Caps() Caps {
	return Caps{
		Ping:     true,
		Download: derefOrEmpty(a.server.DownloadURL) != "",
		Upload:   derefOrEmpty(a.server.UploadURL) != "",
		Timings:  derefOrEmpty(a.server.DownloadURL) != "",
	}
}

func (a *libreSpeed) TimingsURL() string {
	url, err := withQueryParam(derefOrEmpty(a.server.DownloadURL), "ckSize", strconv.Itoa(libreSpeedProbeChunk))
	if err != nil {
		return ""
	}
	return url
}

func (a *libreSpeed) Download(ctx context.Context, o Opts) (Sample, error) {
	base := derefOrEmpty(a.server.DownloadURL)
	if base == "" {
		return Sample{}, notSupported(a.ID(), "download")
	}

	url, err := withQueryParam(base, "ckSize", strconv.Itoa(libreSpeedChunkMB))
	if err != nil {
		return Sample{}, err
	}

	o = o.withDefaults()
	client := a.deps.client()

	return measure("download", o, func() (counters, string, string, error) {
		return downloadSample(ctx, client, url, o)
	})
}

func (a *libreSpeed) Upload(ctx context.Context, o Opts) (Sample, error) {
	url := derefOrEmpty(a.server.UploadURL)
	if url == "" {
		return Sample{}, notSupported(a.ID(), "upload")
	}

	o = o.withDefaults()
	client := a.deps.client()

	return measure("upload", o, func() (counters, string, string, error) {
		return uploadOnce(ctx, client, url, o)
	})
}
