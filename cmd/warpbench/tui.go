package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rpratama123/warpbench/internal/config"
	"github.com/rpratama123/warpbench/internal/iperf3"
	"github.com/rpratama123/warpbench/internal/report"
	"github.com/rpratama123/warpbench/internal/results"
	"github.com/rpratama123/warpbench/internal/runner"
	"github.com/rpratama123/warpbench/internal/serverlist"
	"github.com/rpratama123/warpbench/internal/throughput"
	"github.com/rpratama123/warpbench/internal/trace"
	"github.com/rpratama123/warpbench/internal/tui"
	"github.com/rpratama123/warpbench/internal/version"
)

// runTUI drives the interactive flow.
//
// It wires the same runner, the same result format and the same trace endpoint
// the plain path uses; only the presentation differs.
func runTUI(opts options, args []string, stderr io.Writer) int {
	ctx := context.Background()

	cacheDir, err := config.EnsureCacheDir()
	if err != nil {
		writef(stderr, "warpbench: %v\n", err)
		return exitError
	}

	list, err := serverlist.Load(ctx, serverlist.Options{
		Override:  opts.servers,
		CacheDir:  cacheDir,
		Offline:   opts.offline,
		UserAgent: userAgent(),
	})
	if err != nil {
		writef(stderr, "warpbench: loading the server list: %v\n", err)
		return exitError
	}
	for _, warn := range list.Warnings {
		writef(stderr, "warpbench: warning: %s\n", warn)
	}

	client := throughput.NewHTTPClient(0)
	// A redirect-following client for the iperf3 asset; the measurement client
	// refuses redirects on purpose.
	assetClient := newAssetClient()

	mode := runner.ModeQuick
	if opts.extended {
		mode = runner.ModeExtended
	}

	app := tui.NewApp(tui.AppConfig{
		List:   list.List,
		Mode:   mode,
		Theme:  tui.NewTheme(tui.ColourEnabled(opts.noColour)),
		Groups: splitList(opts.groups),

		Run: func(ctx context.Context, phase string, servers []serverlist.Server, budget runner.Budget, progress runner.Progress) (*results.File, error) {
			return runner.Run(ctx, runner.Config{
				Phase:            phase,
				Mode:             mode,
				List:             list.List,
				Servers:          servers,
				Groups:           splitList(opts.groups),
				Budget:           budget,
				IPv6:             opts.ipv6,
				Masked:           !opts.noMask,
				Offline:          opts.offline,
				ServerListSource: list.Source,
				ServerListOrigin: list.Origin,
				Progress:         progress,
				Tool: results.Tool{
					Version: version.Short(),
					Commit:  version.Commit,
					Go:      runtime.Version(),
				},
			}, runner.Deps{
				TraceDoer: client.Client(),
				Client:    client,
				UserAgent: userAgent(),
				IPerf3Bin: func(ctx context.Context) (string, error) {
					return iperf3.Ensure(ctx, cacheDir, assetClient)
				},
			})
		},

		CheckTrace: func() tea.Cmd {
			return func() tea.Msg {
				result, err := trace.Fetch(ctx, client.Client(), trace.DefaultURL, "warp-check")
				return tui.TracePoll(result, err)
			}
		},

		Save: func(cmp *results.Comparison) ([]string, error) {
			paths := []string{
				defaultOutPath("baseline", cmp.Baseline.StartedAt),
				defaultOutPath("warp", cmp.Warp.StartedAt),
				defaultReportPath(cmp.Warp.StartedAt),
			}
			if err := results.Write(paths[0], cmp.Baseline); err != nil {
				return nil, err
			}
			if err := results.Write(paths[1], cmp.Warp); err != nil {
				return nil, err
			}

			md := report.Markdown(cmp, report.Options{CommandLine: currentCommandLine(args)})
			if err := os.WriteFile(paths[2], md, 0o644); err != nil {
				return nil, err
			}
			return paths, nil
		},

		CompareOptions: results.CompareOptions{Force: opts.force},
		AutoConfirm:    opts.yes,
	})

	final, err := tea.NewProgram(app).Run()
	app.Cleanup()

	m, ok := final.(tui.AppModel)
	if !ok {
		if err != nil {
			writef(stderr, "warpbench: %v\n", err)
			return exitError
		}
		return exitOK
	}
	m.Cleanup()

	if err != nil {
		writef(stderr, "warpbench: %v\n", err)
		return exitError
	}
	if runErr := m.Err(); runErr != nil {
		writef(stderr, "warpbench: %v\n", runErr)
		return exitError
	}

	for _, path := range m.SavedPaths() {
		writef(stderr, "wrote %s\n", path)
	}
	if len(m.SavedPaths()) >= 2 {
		writef(stderr, "compare them with:\n  warpbench --compare %s %s\n", m.SavedPaths()[0], m.SavedPaths()[1])
	}
	return exitOK
}

// defaultReportPath names the Markdown report written alongside the results.
func defaultReportPath(at time.Time) string {
	return fmt.Sprintf("warpbench-%s-report.md", at.Format("20060102-1504"))
}

// newAssetClient returns the client used to fetch release assets.
//
// It follows redirects, unlike the measurement client, because GitHub serves
// release assets through one.
func newAssetClient() *http.Client {
	return &http.Client{Timeout: 3 * time.Minute}
}
