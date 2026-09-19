package serverlist

import _ "embed"

// The embedded copies are generated from the repository-root originals by
// tools/gen-embedded, because go:embed cannot reach outside this package.
// Regenerate with `go generate ./...`; embed_drift_test.go fails if they differ.

//go:generate go run github.com/rpratama123/warpbench/tools/gen-embedded

//go:embed embedded/servers.json
var embeddedList []byte

//go:embed embedded/servers.schema.json
var embeddedSchema []byte
