package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/rpratama123/warpbench/internal/config"
	"github.com/rpratama123/warpbench/internal/iperf3"
	"github.com/rpratama123/warpbench/internal/results"
	"github.com/rpratama123/warpbench/internal/runner"
	"github.com/rpratama123/warpbench/internal/serverlist"
	"github.com/rpratama123/warpbench/internal/throughput"
	"github.com/rpratama123/warpbench/internal/trace"
	"github.com/rpratama123/warpbench/internal/version"
)

// Exit codes. 2 is reserved for usage errors so a wrapper script can tell a
// mistyped flag apart from a measurement failure.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

const repoURL = "https://github.com/rpratama123/warpbench"

// options holds the parsed flags.
type options struct {
	showVersion bool
	doctor      bool
	noTTY       bool
	offline     bool
	servers     string

	quick    bool
	extended bool
	groups   string
	phase    string
	out      string
	report   string
	compare  bool
	force    bool
	parallel int
	ipv6     bool
	noMask   bool
	jsonOut  bool
	yes      bool
	noColour bool

	// positional holds arguments after the flags, used by --compare.
	positional []string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, interactiveConsole))
}

// interactiveConsole reports whether we own a usable terminal on both ends.
//
// The launcher may exec us with stdin redirected from /dev/tty, may pass
// --no-tty when it could not open /dev/tty at all, and stdout may be a pipe
// (curl | bash, irm | iex, CI). The TUI must never be drawn unless this is true.
func interactiveConsole() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// writef prints to w and discards the error by design: these are diagnostics
// bound for stdout/stderr, where a failed write is not actionable.
//
// Code that writes a *report* must not use this. A report that silently fails
// to flush is a data-loss bug, so report writers check their errors.
func writef(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// userAgent identifies us to every endpoint we contact.
func userAgent() string {
	return "warpbench/" + version.Short() + " (+" + repoURL + ")"
}

func run(args []string, stdout, stderr io.Writer, isTTY func() bool) int {
	opts, err := parseArgs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(stdout)
			return exitOK
		}
		return exitUsage
	}

	switch {
	case opts.showVersion:
		writef(stdout, "%s\n", version.String())
		return exitOK
	case opts.doctor:
		return doctor(stdout, opts, isTTY)
	case opts.compare:
		return runCompare(opts, args, stdout, stderr)
	default:
		return runPhase(opts, args, stdout, stderr, isTTY)
	}
}

func parseArgs(args []string, stderr io.Writer) (options, error) {
	var opts options

	fs := flag.NewFlagSet("warpbench", flag.ContinueOnError)
	fs.SetOutput(stderr)
	// Suppress the flag package's automatic usage dump; run() decides what to
	// print so that -h goes to stdout and real errors stay on stderr.
	fs.Usage = func() {}

	fs.BoolVar(&opts.showVersion, "version", false, "print version information and exit")
	fs.BoolVar(&opts.doctor, "doctor", false, "report the local environment and exit")
	fs.BoolVar(&opts.noTTY, "no-tty", false, "never draw a TUI; print plain, non-interactive output")
	fs.BoolVar(&opts.offline, "offline", false, "do not touch the network; use the cached or embedded server list")
	fs.StringVar(&opts.servers, "servers", "", "use a server list from `path` or URL instead of the built-in one")

	fs.BoolVar(&opts.quick, "quick", false, "measure the quick tier (default)")
	fs.BoolVar(&opts.extended, "extended", false, "measure every server in the list")
	fs.StringVar(&opts.groups, "groups", "", "comma-separated group ids to measure, e.g. id,sg")
	fs.StringVar(&opts.phase, "phase", "", "phase to measure: baseline or warp")
	fs.StringVar(&opts.out, "out", "", "write the result JSON to `path`")
	fs.BoolVar(&opts.compare, "compare", false, "compare two result files given as arguments")
	fs.StringVar(&opts.report, "report", "", "write a Markdown report to `path`, or - for stdout")
	fs.BoolVar(&opts.force, "force", false, "proceed despite a WARP-state or server-list-revision mismatch")
	fs.IntVar(&opts.parallel, "parallel", 1, "concurrent streams for throughput samples")
	fs.BoolVar(&opts.ipv6, "ipv6", false, "measure over IPv6 instead of IPv4")
	fs.BoolVar(&opts.noMask, "no-mask", false, "do not mask the public IP in saved results")
	fs.BoolVar(&opts.jsonOut, "json", false, "print the result JSON to stdout")
	fs.BoolVar(&opts.yes, "yes", false, "assume yes to prompts")
	fs.BoolVar(&opts.noColour, "no-color", false, "never use colour")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options{}, err
		}
		writef(stderr, "Run 'warpbench --help' for usage.\n")
		return options{}, err
	}

	opts.positional = fs.Args()

	if opts.compare {
		if len(opts.positional) != 2 {
			writef(stderr, "warpbench: --compare needs exactly two result files, got %d\n", len(opts.positional))
			writef(stderr, "Run 'warpbench --help' for usage.\n")
			return options{}, errors.New("--compare needs two files")
		}
	} else if len(opts.positional) > 0 {
		writef(stderr, "warpbench: unexpected argument %q\n", opts.positional[0])
		writef(stderr, "Run 'warpbench --help' for usage.\n")
		return options{}, errors.New("unexpected positional argument")
	}

	if opts.quick && opts.extended {
		writef(stderr, "warpbench: --quick and --extended are mutually exclusive\n")
		return options{}, errors.New("conflicting modes")
	}
	if opts.parallel < 1 {
		writef(stderr, "warpbench: --parallel must be at least 1\n")
		return options{}, errors.New("bad parallel value")
	}

	return opts, nil
}

const usageText = `warpbench - measure what Cloudflare WARP changes on your ISP path

Usage:
  warpbench [flags]                  measure one phase
  warpbench --compare A.json B.json  compare a baseline against a warp result

Flags:
  --quick            measure the quick tier (default)
  --extended         measure every server in the list
  --groups LIST      comma-separated group ids, e.g. id,sg
  --phase PHASE      baseline or warp (required to measure)
  --out PATH         write the result JSON to PATH
  --compare          compare two result files given as arguments
  --report PATH      write a Markdown report to PATH, or - for stdout
  --force            proceed despite a state or revision mismatch
  --parallel N       concurrent streams for throughput samples (default 1)
  --ipv6             measure over IPv6 instead of IPv4
  --no-mask          do not mask the public IP in saved results
  --json             print the result JSON to stdout
  --yes              assume yes to prompts
  --no-color         never use colour
  --servers PATH     use a server list from PATH or a URL
  --offline          do not touch the network; use the cached or embedded list
  --doctor           report the local environment and exit
  --no-tty           never draw a TUI; print plain, non-interactive output
  --version          print version information and exit
  -h, --help         show this help and exit

Typical use:
  warpbench --quick --phase baseline --out baseline.json
  # turn WARP on
  warpbench --quick --phase warp --out warp.json
  warpbench --compare baseline.json warp.json
  warpbench --compare --report report.md baseline.json warp.json

Plan and progress:
  ` + repoURL + `
`

func usage(w io.Writer) {
	_, _ = io.WriteString(w, usageText)
}

// doctor reports the facts the launchers and the binary must agree on, plus the
// provenance of the server list. A user debugging a surprising result needs to
// know whether they measured against a fresh remote revision, a stale cache, or
// the copy compiled into the binary.
func doctor(w io.Writer, opts options, isTTY func() bool) int {
	writef(w, "warpbench doctor\n")
	writef(w, "  version          %s\n", version.String())
	writef(w, "  platform         %s/%s\n", runtime.GOOS, runtime.GOARCH)

	interactive := isTTY() && !opts.noTTY
	var reason string
	switch {
	case opts.noTTY:
		reason = " (--no-tty)"
	case !isTTY():
		reason = " (stdin or stdout is not a terminal)"
	}
	writef(w, "  interactive      %v%s\n", interactive, reason)

	if override := os.Getenv(config.EnvCacheDir); override != "" {
		writef(w, "  cache override   %s=%s\n", config.EnvCacheDir, override)
	}

	dir, err := config.EnsureCacheDir()
	if err != nil {
		writef(w, "  cache directory  ERROR: %v\n", err)
		return exitError
	}
	if err := checkWritable(dir); err != nil {
		writef(w, "  cache directory  %s (NOT writable: %v)\n", dir, err)
		return exitError
	}
	writef(w, "  cache directory  %s (writable)\n", dir)

	res, err := serverlist.Load(context.Background(), serverlist.Options{
		Override:  opts.servers,
		CacheDir:  dir,
		Offline:   opts.offline,
		UserAgent: userAgent(),
	})
	if err != nil {
		writef(w, "  server list      ERROR: %v\n", err)
		return exitError
	}

	writef(w, "  server list      %s\n", res.Origin)
	writef(w, "  list source      %s (revision %s)\n", res.Source, res.Revision)
	writef(w, "  servers          %d across %d groups\n", len(res.List.Servers), len(res.List.Groups))
	for _, warn := range res.Warnings {
		writef(w, "  warning          %s\n", warn)
	}

	return exitOK
}

// checkWritable verifies we can actually create files in dir, rather than
// trusting the permission bits.
func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".warpbench-doctor-*")
	if err != nil {
		return err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		return err
	}
	return os.Remove(filepath.Clean(name))
}

// --- the measure path ------------------------------------------------------

func runPhase(opts options, args []string, stdout, stderr io.Writer, isTTY func() bool) int {
	if wantsTUI(opts, isTTY) {
		return runTUI(opts, args, stderr)
	}

	phase := strings.ToLower(strings.TrimSpace(opts.phase))
	if phase != "baseline" && phase != "warp" {
		writef(stderr, "warpbench: --phase must be baseline or warp (got %q)\n", opts.phase)
		writef(stderr, "Run 'warpbench --help' for usage.\n")
		return exitUsage
	}

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

	mode := runner.ModeQuick
	if opts.extended {
		mode = runner.ModeExtended
	}

	groups := splitList(opts.groups)
	selected := runner.Select(list.List, mode, groups, nil)
	if len(selected) == 0 {
		writef(stderr, "warpbench: no servers selected for mode %s and groups %v\n", mode, groups)
		return exitUsage
	}

	budget := runner.BudgetFor(mode).WithParallel(opts.parallel)
	estimate := runner.Estimate(selected, budget)

	client := throughput.NewHTTPClient(0)

	// A separate client for downloading the pinned iperf3 binary. The
	// measurement client refuses redirects on purpose, and GitHub serves release
	// assets through one, so sharing it would break the download.
	assetClient := &http.Client{Timeout: 3 * time.Minute}

	// The state check is what makes the comparison meaningful. Measuring a
	// "baseline" with WARP already on produces a file that looks fine and means
	// nothing.
	probe, _ := trace.Fetch(ctx, client.Client(), trace.DefaultURL, "preflight")
	if code := checkPhaseState(probe, phase, opts.force, stderr); code != exitOK {
		return code
	}

	writef(stderr, "measuring %d server(s) in %s mode; estimated %s for this phase\n",
		len(selected), mode, estimate.Round(time.Second))

	prog := &textProgress{w: stderr}

	file, err := runner.Run(ctx, runner.Config{
		Phase:            phase,
		Mode:             mode,
		List:             list.List,
		Servers:          selected,
		Groups:           groups,
		Budget:           budget,
		IPv6:             opts.ipv6,
		Masked:           !opts.noMask,
		Offline:          opts.offline,
		ServerListSource: list.Source,
		ServerListOrigin: list.Origin,
		Progress:         prog,
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
	if err != nil {
		writef(stderr, "warpbench: %v\n", err)
		return exitError
	}

	if opts.force {
		file.Warnings = append(file.Warnings, "run proceeded with --force despite a state or revision mismatch")
	}

	outPath := opts.out
	if outPath == "" {
		outPath = defaultOutPath(phase, file.StartedAt)
	}
	if err := results.Write(outPath, file); err != nil {
		writef(stderr, "warpbench: %v\n", err)
		return exitError
	}

	writef(stderr, "\nwrote %s\n", outPath)
	prog.Summary(file)

	if opts.jsonOut {
		data, err := json.MarshalIndent(file, "", "  ")
		if err != nil {
			writef(stderr, "warpbench: %v\n", err)
			return exitError
		}
		writef(stdout, "%s\n", data)
	}

	return exitOK
}

// checkPhaseState refuses to measure a phase the trace endpoint contradicts.
func checkPhaseState(probe trace.Result, phase string, force bool, stderr io.Writer) int {
	if probe.Err != "" {
		writef(stderr, "warpbench: could not read the WARP state (%s); continuing with the state unknown\n", probe.Err)
		return exitOK
	}

	wantOn := phase == "warp"
	if probe.WarpEnabled() == wantOn {
		return exitOK
	}

	detail := fmt.Sprintf("the %s phase expects WARP to be %s, but the trace endpoint reports warp=%s",
		phase, onOff(wantOn), probe.Warp)

	if force {
		writef(stderr, "warpbench: WARNING: %s; continuing because --force was given\n", detail)
		return exitOK
	}

	writef(stderr, "warpbench: %s\n", detail)
	if explain := probe.Explain(); explain != "" {
		writef(stderr, "warpbench: %s\n", explain)
	}
	writef(stderr, "warpbench: pass --force to measure anyway, and the override will be recorded\n")
	return exitError
}

// wantsTUI reports whether the interactive flow should run.
//
// The interactive flow is the default when we own a terminal and the user has
// not said what to measure, because choosing is exactly what the screens are
// for. Naming a phase, or asking for JSON, is a scriptable request and gets the
// plain path.
func wantsTUI(opts options, isTTY func() bool) bool {
	return isTTY() &&
		!opts.noTTY &&
		!opts.jsonOut &&
		strings.TrimSpace(opts.phase) == ""
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func defaultOutPath(phase string, at time.Time) string {
	return fmt.Sprintf("warpbench-%s-%s.json", at.Format("20060102-1504"), phase)
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
