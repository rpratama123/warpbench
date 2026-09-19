package serverlist

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The embedded copies exist because go:embed cannot reach outside its own
// package. That indirection is only safe if it is checked: if these fail, run
// `go generate ./...` and commit the result.
func TestEmbeddedServersMatchesCanonical(t *testing.T) {
	assertSameFile(t, filepath.Join("..", "..", "servers.json"), embeddedList)
}

func TestEmbeddedSchemaMatchesCanonical(t *testing.T) {
	assertSameFile(t, filepath.Join("..", "..", "schema", "servers.schema.json"), embeddedSchema)
}

func assertSameFile(t *testing.T, path string, embedded []byte) {
	t.Helper()

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	if bytes.Equal(onDisk, embedded) {
		return
	}

	t.Errorf(`%s and its embedded copy have drifted.

Fix: run 'go generate ./...' from the repository root and commit the result.

  on disk:  %d bytes
  embedded: %d bytes`, path, len(onDisk), len(embedded))
}
