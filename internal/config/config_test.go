package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCacheDirOverrideWins(t *testing.T) {
	want := filepath.Join(t.TempDir(), "custom")
	t.Setenv(EnvCacheDir, want)

	got, err := CacheDir()
	if err != nil {
		t.Fatalf("CacheDir() error = %v", err)
	}
	if got != want {
		t.Errorf("CacheDir() = %q, want %q", got, want)
	}
}

// The override must win even when the platform-specific variables are also set,
// because tests and self-hosted launchers rely on it.
func TestCacheDirOverrideBeatsPlatformVars(t *testing.T) {
	want := filepath.Join(t.TempDir(), "custom")
	t.Setenv(EnvCacheDir, want)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "xdg"))
	t.Setenv("LOCALAPPDATA", filepath.Join(t.TempDir(), "local"))

	got, err := CacheDir()
	if err != nil {
		t.Fatalf("CacheDir() error = %v", err)
	}
	if got != want {
		t.Errorf("CacheDir() = %q, want %q", got, want)
	}
}

func TestCacheDirPerPlatform(t *testing.T) {
	t.Setenv(EnvCacheDir, "")

	switch runtime.GOOS {
	case "windows":
		base := filepath.Join(t.TempDir(), "AppData", "Local")
		t.Setenv("LOCALAPPDATA", base)

		got, err := CacheDir()
		if err != nil {
			t.Fatalf("CacheDir() error = %v", err)
		}
		if want := filepath.Join(base, "warpbench"); got != want {
			t.Errorf("CacheDir() = %q, want %q", got, want)
		}

		t.Setenv("LOCALAPPDATA", "")
		if _, err := CacheDir(); err == nil {
			t.Error("CacheDir() with empty LOCALAPPDATA: want error, got nil")
		}
	default:
		base := filepath.Join(t.TempDir(), "cache")
		t.Setenv("XDG_CACHE_HOME", base)

		got, err := CacheDir()
		if err != nil {
			t.Fatalf("CacheDir() error = %v", err)
		}
		if want := filepath.Join(base, "warpbench"); got != want {
			t.Errorf("CacheDir() = %q, want %q", got, want)
		}

		// With XDG unset it must fall back to $HOME/.cache, not error out.
		t.Setenv("XDG_CACHE_HOME", "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		if runtime.GOOS == "windows" {
			t.Skip("HOME fallback is not used on Windows")
		}

		got, err = CacheDir()
		if err != nil {
			t.Fatalf("CacheDir() with no XDG: error = %v", err)
		}
		if want := filepath.Join(home, ".cache", "warpbench"); got != want {
			t.Errorf("CacheDir() with no XDG = %q, want %q", got, want)
		}
	}
}

func TestEnsureCacheDirCreates(t *testing.T) {
	want := filepath.Join(t.TempDir(), "nested", "warpbench")
	t.Setenv(EnvCacheDir, want)

	got, err := EnsureCacheDir()
	if err != nil {
		t.Fatalf("EnsureCacheDir() error = %v", err)
	}
	if got != want {
		t.Errorf("EnsureCacheDir() = %q, want %q", got, want)
	}

	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat %s: %v", got, err)
	}
	if !info.IsDir() {
		t.Errorf("%s is not a directory", got)
	}
	// Windows does not represent Unix permission bits, so only assert them
	// where they are meaningful.
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("permissions = %o, want 700", perm)
		}
	}
}

func TestEnsureCacheDirUnwritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions are not enforced the same way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}

	// A file where the directory should be makes MkdirAll fail.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvCacheDir, filepath.Join(blocker, "warpbench"))

	if _, err := EnsureCacheDir(); err == nil {
		t.Error("EnsureCacheDir() over a regular file: want error, got nil")
	}
}
