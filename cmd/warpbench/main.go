// Command warpbench measures what Cloudflare WARP actually changes on a
// congested ISP uplink: latency, jitter, packet loss, connection-setup timings,
// and download/upload throughput, first on the raw ISP path and then over WARP.
//
// This is the Phase 2 scaffold. Argument parsing, version reporting,
// interactive-console detection and cache-directory resolution are real and
// tested; the measurement engine lands in later phases.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/term"

	"github.com/rpratama123/warpbench/internal/config"
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

// options holds the flags this scaffold understands. The full documented flag
// set (--quick/--extended/--phase/--compare/...) arrives with the runner.
type options struct {
	showVersion bool
	doctor      bool
	noTTY       bool
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
	}

	writef(stderr, "%s\n\n", version.String())
	writef(stderr, "This build is the Phase 2 scaffold: the launcher, version and\n")
	writef(stderr, "console-detection paths work, but the measurement engine is not\n")
	writef(stderr, "implemented yet, so there is nothing to measure.\n")
	writef(stderr, "\nPlan and progress: %s\n", repoURL)
	return exitError
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

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options{}, err
		}
		writef(stderr, "Run 'warpbench --help' for usage.\n")
		return options{}, err
	}

	if fs.NArg() > 0 {
		writef(stderr, "warpbench: unexpected argument %q\n", fs.Arg(0))
		writef(stderr, "Run 'warpbench --help' for usage.\n")
		return options{}, errors.New("unexpected positional argument")
	}

	return opts, nil
}

const usageText = `warpbench - measure what Cloudflare WARP changes on your ISP path

Usage:
  warpbench [flags]

Flags:
  --version   print version information and exit
  --doctor    report the local environment and exit
  --no-tty    never draw a TUI; print plain, non-interactive output
  -h, --help  show this help and exit

Status:
  This is the Phase 2 scaffold. The measurement engine is not implemented yet.

Plan and progress:
  ` + repoURL + `
`

func usage(w io.Writer) {
	_, _ = io.WriteString(w, usageText)
}

// doctor reports the facts the launchers and the binary must agree on. It exits
// non-zero when the cache directory cannot be created, which is the one failure
// that would otherwise surface later as a confusing download error.
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
