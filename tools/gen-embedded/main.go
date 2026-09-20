// Command gen-embedded copies the canonical data files from the repository root
// into internal/serverlist/embedded/, where go:embed can reach them.
//
// The go:embed directive cannot reference files outside the embedding package's
// directory, so these copies are generated. internal/serverlist has a test that
// fails when they drift from the originals, so run `go generate ./...` after
// editing servers.json or the schema.
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// pairs maps each canonical source, relative to the repository root, to its
// generated destination. A slice rather than a map so output order is stable.
var pairs = []struct{ src, dst string }{
	{"servers.json", "internal/serverlist/embedded/servers.json"},
	{"schema/servers.schema.json", "internal/serverlist/embedded/servers.schema.json"},
	{"schema/results.schema.json", "internal/results/embedded/results.schema.json"},
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "gen-embedded: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	root, err := moduleRoot()
	if err != nil {
		return err
	}

	for _, p := range pairs {
		srcPath := filepath.Join(root, filepath.FromSlash(p.src))
		dstPath := filepath.Join(root, filepath.FromSlash(p.dst))

		data, err := os.ReadFile(srcPath)
		if err != nil {
			return fmt.Errorf("reading %s: %w", p.src, err)
		}
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", filepath.Dir(p.dst), err)
		}
		if err := os.WriteFile(dstPath, data, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", p.dst, err)
		}
		fmt.Printf("gen-embedded: %s -> %s (%d bytes)\n", p.src, p.dst, len(data))
	}
	return nil
}

// moduleRoot walks up from the working directory to the directory holding go.mod.
// `go generate` runs with the working directory set to the package directory, so
// this always finds the repository root regardless of where it is invoked from.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found in or above the working directory")
		}
		dir = parent
	}
}
