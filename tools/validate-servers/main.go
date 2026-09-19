// Command validate-servers checks that every entry in servers.json is alive.
//
// It loads the list through internal/serverlist -- the same code path the
// shipped binary uses -- so a schema violation, or any entry the loader would
// silently drop, fails the run. It then probes every target: DNS for ping_host,
// an HTTP request for download and upload endpoints, and a TCP connect for
// iperf3 ports.
//
// Exit status 0 means every entry passed. Any failure exits 1, which is what
// makes the weekly GitHub Action able to open an issue.
//
// Usage:
//
//	go run ./tools/validate-servers [-list servers.json] [-timeout 20s] [-concurrency 6]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rpratama123/warpbench/internal/serverlist"
)

const (
	// userAgent identifies us to the servers we probe. Public mirrors and
	// community speedtest hosts deserve to know who is knocking.
	userAgent = "warpbench-validate/1.0 (+https://github.com/rpratama123/warpbench)"

	// uploadProbeBytes is deliberately tiny: enough to prove the endpoint
	// accepts a body, not enough to burden a volunteer-run server.
	uploadProbeBytes = 1024
)

// writef prints to w and discards the error by design: these are diagnostics
// bound for stdout/stderr, where a failed write is not actionable.
func writef(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

type check struct {
	server string
	kind   string
	target string
	run    func(context.Context, *http.Client) error
}

type outcome struct {
	check check
	err   error
	note  string
}

func main() {
	listPath := flag.String("list", "servers.json", "path to the server list to validate")
	timeout := flag.Duration("timeout", 20*time.Second, "per-check timeout")
	concurrency := flag.Int("concurrency", 6, "how many checks to run at once")
	verbose := flag.Bool("v", false, "print every check, not only failures")
	flag.Parse()

	if err := run(*listPath, *timeout, *concurrency, *verbose, os.Stdout); err != nil {
		writef(os.Stderr, "validate-servers: %v\n", err)
		os.Exit(1)
	}
}

func run(listPath string, timeout time.Duration, concurrency int, verbose bool, out io.Writer) error {
	// Offline + Override: read exactly the file we were given, never the network.
	res, err := serverlist.Load(context.Background(), serverlist.Options{
		Override: listPath,
		Offline:  true,
	})
	if err != nil {
		return fmt.Errorf("loading %s: %w", listPath, err)
	}

	// A warning means the loader dropped an entry. For the curated list that is
	// a defect, not something to tolerate.
	if len(res.Warnings) > 0 {
		for _, w := range res.Warnings {
			writef(out, "  WARNING %s\n", w)
		}
		return fmt.Errorf("%s has %d unusable entr(ies); fix them before shipping", listPath, len(res.Warnings))
	}

	writef(out, "%s: revision %s, %d servers across %d groups\n\n",
		listPath, res.Revision, len(res.List.Servers), len(res.List.Groups))

	client := &http.Client{Timeout: timeout}
	checks := buildChecks(res.List.Servers)
	results := execute(checks, concurrency, timeout, client)

	var failed int
	for _, r := range results {
		if r.err != nil {
			failed++
			writef(out, "  FAIL %-22s %-9s %s\n       %v\n", r.check.server, r.check.kind, r.check.target, r.err)
			continue
		}
		if verbose {
			note := r.note
			if note != "" {
				note = " (" + note + ")"
			}
			writef(out, "  ok   %-22s %-9s %s%s\n", r.check.server, r.check.kind, r.check.target, note)
		}
	}

	writef(out, "\n%d checks: %d passed, %d failed\n", len(results), len(results)-failed, failed)
	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	return nil
}

func buildChecks(servers []serverlist.Server) []check {
	var checks []check

	for _, s := range servers {
		pingHost := s.PingHost

		checks = append(checks, check{
			server: s.ID,
			kind:   "dns",
			target: pingHost,
			run: func(ctx context.Context, _ *http.Client) error {
				var r net.Resolver
				addrs, err := r.LookupHost(ctx, pingHost)
				if err != nil {
					return err
				}
				if len(addrs) == 0 {
					return errors.New("resolved to no addresses")
				}
				return nil
			},
		})

		if s.Has("download") && s.Protocol != "iperf3" {
			url := downloadProbeURL(s)
			checks = append(checks, check{
				server: s.ID,
				kind:   "download",
				target: url,
				run:    probeDownload(url),
			})
		}

		// iperf3 has no upload URL; its upload leg is exercised by the TCP
		// check below, so only add an HTTP upload check when there is a URL.
		if s.Has("upload") {
			if url := deref(s.UploadURL); url != "" {
				checks = append(checks, check{
					server: s.ID,
					kind:   "upload",
					target: url,
					run:    probeUpload(url),
				})
			}
		}

		if s.IPerf3 != nil {
			host, port := s.IPerf3.Host, firstPort(s.IPerf3.PortRange)
			target := fmt.Sprintf("%s:%d", host, port)
			checks = append(checks, check{
				server: s.ID,
				kind:   "iperf3",
				target: target,
				run: func(ctx context.Context, _ *http.Client) error {
					var d net.Dialer
					conn, err := d.DialContext(ctx, "tcp", target)
					if err != nil {
						return err
					}
					return conn.Close()
				},
			})
		}
	}

	return checks
}

// downloadProbeURL keeps the probe cheap. LibreSpeed and Cloudflare endpoints
// need a size parameter that the stored base URL deliberately omits.
func downloadProbeURL(s serverlist.Server) string {
	url := deref(s.DownloadURL)
	switch s.Protocol {
	case "librespeed":
		if !strings.Contains(url, "?") {
			url += "?ckSize=1"
		}
	case "cloudflare":
		if !strings.Contains(url, "?") {
			url += "?bytes=1000"
		}
	}
	return url
}

func probeDownload(url string) func(context.Context, *http.Client) error {
	return func(ctx context.Context, c *http.Client) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", userAgent)
		// Ask for a sliver so a 100 MB file never comes down the wire.
		req.Header.Set("Range", "bytes=0-1023")

		resp, err := c.Do(req)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			return fmt.Errorf("HTTP %s", resp.Status)
		}
		return nil
	}
}

func probeUpload(url string) func(context.Context, *http.Client) error {
	return func(ctx context.Context, c *http.Client) error {
		// A finite reader. An infinite one would upload until the timeout.
		body := io.LimitReader(zeroReader{}, uploadProbeBytes)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.ContentLength = uploadProbeBytes

		resp, err := c.Do(req)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return fmt.Errorf("HTTP %s", resp.Status)
		}
		return nil
	}
}

// zeroReader yields an endless stream of zero bytes, bounded by the caller.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// execute runs checks with a bounded worker pool, preserving input order in the
// result so output is stable and diffable.
func execute(checks []check, concurrency int, timeout time.Duration, client *http.Client) []outcome {
	if concurrency < 1 {
		concurrency = 1
	}

	results := make([]outcome, len(checks))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, c := range checks {
		wg.Add(1)
		go func(i int, c check) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			results[i] = outcome{check: c, err: c.run(ctx, client)}
		}(i, c)
	}
	wg.Wait()

	return results
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func firstPort(r []int) int {
	if len(r) == 0 {
		return 0
	}
	// The lower bound is what the ranges are normally expressed against.
	return min(r[0], r[len(r)-1])
}

// sortedIDs is used by the tests to compare sets deterministically.
func sortedIDs(servers []serverlist.Server) []string {
	ids := make([]string, 0, len(servers))
	for _, s := range servers {
		ids = append(ids, s.ID)
	}
	sort.Strings(ids)
	return ids
}
