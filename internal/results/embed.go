package results

import _ "embed"

// The embedded copy is generated from schema/results.schema.json by
// tools/gen-embedded, because go:embed cannot reach outside this package.
// Regenerate with `go generate ./...`; results_test.go fails if it drifts.

//go:generate go run github.com/rpratama123/warpbench/tools/gen-embedded

//go:embed embedded/results.schema.json
var schemaJSON []byte
