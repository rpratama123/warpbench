package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// alwaysTTY / neverTTY let the console-detection branch be tested without a
// real terminal, which CI does not have.
func alwaysTTY() bool { return true }
func neverTTY() bool  { return false }

func runCapture(t *testing.T, args []string, isTTY func() bool) (code int, stdout, stderr string) {
	t.Helper()

	var out, errBuf bytes.Buffer
	code = run(args, &out, &errBuf, isTTY)
	return code, out.String(), errBuf.String()
}

func TestVersionFlag(t *testing.T) {
	for _, arg := range []string{"--version", "-version"} {
		t.Run(arg, func(t *testing.T) {
			code, stdout, stderr := runCapture(t, []string{arg}, neverTTY)

			if code != exitOK {
				t.Errorf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
			}
			if !strings.Contains(stdout, "warpbench") {
				t.Errorf("stdout = %q, want it to mention warpbench", stdout)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
			}
		})
	}
}

func TestHelpGoesToStdoutAndExitsZero(t *testing.T) {
	for _, arg := range []string{"-h", "--help"} {
		t.Run(arg, func(t *testing.T) {
			code, stdout, _ := runCapture(t, []string{arg}, neverTTY)

			if code != exitOK {
				t.Errorf("exit code = %d, want %d", code, exitOK)
			}
			if !strings.Contains(stdout, "Usage:") {
				t.Errorf("stdout = %q, want usage text", stdout)
			}
		})
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	tests := map[string][]string{
		"unknown flag":       {"--definitely-not-a-flag"},
		"positional arg":     {"baseline.json"},
		"flag missing value": {"--out"},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := runCapture(t, args, neverTTY)

			if code != exitUsage {
				t.Errorf("exit code = %d, want %d", code, exitUsage)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty (errors belong on stderr)", stdout)
			}
			if !strings.Contains(stderr, "warpbench") {
				t.Errorf("stderr = %q, want a diagnostic", stderr)
			}
		})
	}
}

// Until the measurement engine lands, a bare invocation must fail loudly rather
// than exit 0 and look like a successful run to a script.
func TestBareInvocationReportsUnimplemented(t *testing.T) {
	code, stdout, stderr := runCapture(t, nil, alwaysTTY)

	if code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "scaffold") {
		t.Errorf("stderr = %q, want it to explain this is a scaffold", stderr)
	}
}

func TestDoctorReportsCacheDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "warpbench")
	t.Setenv("WARPBENCH_CACHE_DIR", dir)

	code, stdout, stderr := runCapture(t, []string{"--doctor"}, alwaysTTY)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitOK, stderr)
	}
	for _, want := range []string{"warpbench doctor", "platform", "interactive      true", dir, "writable"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q\n--- got ---\n%s", want, stdout)
		}
	}
}

// --no-tty must be visible in the report, because the launcher passes it when
// it could not open /dev/tty and the user needs to know why there is no TUI.
func TestDoctorHonoursNoTTY(t *testing.T) {
	t.Setenv("WARPBENCH_CACHE_DIR", filepath.Join(t.TempDir(), "warpbench"))

	code, stdout, _ := runCapture(t, []string{"--doctor", "--no-tty"}, alwaysTTY)

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if !strings.Contains(stdout, "interactive      false") {
		t.Errorf("stdout = %q, want interactive false", stdout)
	}
	if !strings.Contains(stdout, "--no-tty") {
		t.Errorf("stdout = %q, want it to name --no-tty as the reason", stdout)
	}
}

func TestDoctorFailsOnUnwritableCacheDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions are not enforced the same way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WARPBENCH_CACHE_DIR", filepath.Join(blocker, "warpbench"))

	code, stdout, _ := runCapture(t, []string{"--doctor"}, neverTTY)

	if code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
	if !strings.Contains(stdout, "ERROR") {
		t.Errorf("stdout = %q, want an ERROR line", stdout)
	}
}

func TestInteractiveConsoleIsFalseWithoutTerminal(t *testing.T) {
	// Under `go test`, stdin/stdout are pipes, so the real detector must say no.
	// This is the guard that keeps the TUI from being drawn into a pipeline.
	if interactiveConsole() {
		t.Error("interactiveConsole() = true under go test; want false")
	}
}
