package throughput

import (
	"context"
	"strconv"

	"github.com/rpratama123/warpbench/internal/serverlist"
)

// cloudflarePayloadBytes is what we ask the endpoint for.
//
// The endpoint takes an explicit size and rejects anything at or above
// 100,000,000 bytes with a bare 403 (found by bisection; there is no documented
// limit). 90 MB sits safely under that ceiling and still exceeds what most
// links move in one window, so the sample is filled by repeating the request
// rather than by asking for a larger payload.
const cloudflarePayloadBytes = 90_000_000

// cloudflareProbeBytes is the size of the timings probe: enough to get a
// response, small enough not to measure throughput by accident.
const cloudflareProbeBytes = 1024

// cloudflare measures against Cloudflare's own speed endpoints.
//
// This path is special and the report must footnote it: with WARP enabled, a
// connection to speed.cloudflare.com never leaves Cloudflare's network. It
// therefore measures ISP-to-nearest-edge and must never be read as an
// end-to-end international result. It is kept because it is the only upload
// target available in some groups, and because the contrast between it and the
// other targets is itself informative.
type cloudflare struct {
	server serverlist.Server
	deps   Deps
}

func newCloudflare(server serverlist.Server, deps Deps) *cloudflare {
	return &cloudflare{server: server, deps: deps}
}

func (a *cloudflare) ID() string { return "cloudflare" }

func (a *cloudflare) Caps() Caps {
	return Caps{
		Ping:     true,
		Download: derefOrEmpty(a.server.DownloadURL) != "",
		Upload:   derefOrEmpty(a.server.UploadURL) != "",
		Timings:  derefOrEmpty(a.server.DownloadURL) != "",
	}
}

func (a *cloudflare) TimingsURL() string {
	url, err := withQueryParam(derefOrEmpty(a.server.DownloadURL), "bytes", strconv.Itoa(cloudflareProbeBytes))
	if err != nil {
		return ""
	}
	return url
}

func (a *cloudflare) Download(ctx context.Context, o Opts) (Sample, error) {
	base := derefOrEmpty(a.server.DownloadURL)
	if base == "" {
		return Sample{}, notSupported(a.ID(), "download")
	}

	url, err := withQueryParam(base, "bytes", strconv.Itoa(cloudflarePayloadBytes))
	if err != nil {
		return Sample{}, err
	}

	o = o.withDefaults()
	client := a.deps.client()

	return measure("download", o, func() (counters, string, string, error) {
		return downloadSample(ctx, client, url, o)
	})
}

func (a *cloudflare) Upload(ctx context.Context, o Opts) (Sample, error) {
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
