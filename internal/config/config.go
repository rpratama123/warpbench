// Package config resolves the per-user cache directory.
//
// This MUST stay byte-for-byte consistent with warpbench.sh and warpbench.ps1:
// the launchers place the platform binary there and the binary places the
// server-list cache and the iperf3 binary there. If the two disagree, the
// launcher downloads a binary the binary never finds, or vice versa.
//
// Keep the table in the doc comment below in sync with both launchers.
//
//	WARPBENCH_CACHE_DIR   explicit override (used by tests and self-hosting)
//	windows               %LOCALAPPDATA%\warpbench
//	otherwise             ${XDG_CACHE_HOME:-$HOME/.cache}/warpbench
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// EnvCacheDir overrides the cache location on every platform.
const EnvCacheDir = "WARPBENCH_CACHE_DIR"

// CacheDir returns the per-user warpbench cache directory. It does not create
// the directory; callers that write must do so themselves.
func CacheDir() (string, error) {
	if dir := os.Getenv(EnvCacheDir); dir != "" {
		return dir, nil
	}

	if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			return "", errors.New("LOCALAPPDATA is not set and WARPBENCH_CACHE_DIR is empty")
		}
		return filepath.Join(base, "warpbench"), nil
	}

	if base := os.Getenv("XDG_CACHE_HOME"); base != "" {
		return filepath.Join(base, "warpbench"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	if home == "" {
		return "", errors.New("home directory is empty and neither XDG_CACHE_HOME nor WARPBENCH_CACHE_DIR is set")
	}
	return filepath.Join(home, ".cache", "warpbench"), nil
}

// EnsureCacheDir returns CacheDir, creating it (0700) if necessary, and reports
// whether it is writable. The launchers create the same directory with the same
// permissions, so a non-writable result means something is genuinely wrong.
func EnsureCacheDir() (string, error) {
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return dir, fmt.Errorf("creating cache directory %s: %w", dir, err)
	}
	return dir, nil
}
