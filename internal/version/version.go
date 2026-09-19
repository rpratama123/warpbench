// Package version carries build identity for the warpbench binary.
//
// The variables are overridden at build time with -ldflags, e.g.
//
//	go build -ldflags "-X github.com/rpratama123/warpbench/internal/version.Version=v0.1.0"
//
// The defaults describe an unstamped local build, which is what `go run` and
// `go test` produce.
package version

import (
	"fmt"
	"runtime"
)

// Build identity. Set via -ldflags at release time; see .goreleaser.yaml.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns a single-line, human-readable build identity.
func String() string {
	return fmt.Sprintf("warpbench %s (commit %s, built %s, %s)", Version, Commit, Date, runtime.Version())
}

// Short returns just the version, for embedding in report headers.
func Short() string { return Version }
