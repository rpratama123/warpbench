package throughput

import (
	"context"

	"github.com/rpratama123/warpbench/internal/serverlist"
)

// httpFile measures static files from provider speedtest hosts.
//
// Download only: these endpoints serve a fixed payload and accept no upload.
// They are the most stable targets in the list, which is why they are used to
// cross-check the protocols that do support upload.
type httpFile struct {
	server serverlist.Server
	deps   Deps
}

func newHTTPFile(server serverlist.Server, deps Deps) *httpFile {
	return &httpFile{server: server, deps: deps}
}

func (a *httpFile) ID() string { return "http-file" }

func (a *httpFile) Caps() Caps {
	return Caps{
		Ping:     true,
		Download: derefOrEmpty(a.server.DownloadURL) != "",
		Upload:   false,
		Timings:  derefOrEmpty(a.server.DownloadURL) != "",
	}
}

func (a *httpFile) TimingsURL() string { return derefOrEmpty(a.server.DownloadURL) }

func (a *httpFile) Download(ctx context.Context, o Opts) (Sample, error) {
	url := derefOrEmpty(a.server.DownloadURL)
	if url == "" {
		return Sample{}, notSupported(a.ID(), "download")
	}

	o = o.withDefaults()
	client := a.deps.client()

	return measure("download", o, func() (counters, string, string, error) {
		return downloadSample(ctx, client, url, o)
	})
}

func (a *httpFile) Upload(context.Context, Opts) (Sample, error) {
	return Sample{}, notSupported(a.ID(), "upload")
}
