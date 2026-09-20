package results

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// printer renders schema messages in plain English.
var printer = message.NewPrinter(language.English)

const schemaResourceURL = "https://raw.githubusercontent.com/rpratama123/warpbench/main/schema/results.schema.json"

var (
	schemaOnce sync.Once
	schemaVal  *jsonschema.Schema
	schemaErr  error
)

func compiledSchema() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
		if err != nil {
			schemaErr = fmt.Errorf("parsing embedded results schema: %w", err)
			return
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource(schemaResourceURL, doc); err != nil {
			schemaErr = fmt.Errorf("registering embedded results schema: %w", err)
			return
		}
		schemaVal, schemaErr = c.Compile(schemaResourceURL)
	})
	return schemaVal, schemaErr
}

// Validate checks a serialised result file against the results schema.
//
// Load enforces this, which is what makes --compare safe to point at a file
// that warpbench did not write: a hand-edited or truncated result is rejected
// with a reason rather than silently producing a wrong comparison.
func Validate(data []byte) error {
	schema, err := compiledSchema()
	if err != nil {
		return err
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("not valid JSON: %w", err)
	}

	if err := schema.Validate(doc); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			return fmt.Errorf("does not match schema/results.schema.json: %s", firstCause(ve))
		}
		return err
	}
	return nil
}

// firstCause renders the leaf failures, which is far more useful than the
// schema library's full nested dump.
//
// Every leaf is walked rather than just the first: an anyOf reports one cause
// per branch, and following only the first branch hides the branch that
// actually failed.
func firstCause(ve *jsonschema.ValidationError) string {
	var leaves []string

	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			leaves = append(leaves, renderCause(e))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)

	if len(leaves) == 0 {
		leaves = []string{renderCause(ve)}
	}
	// A handful is plenty to diagnose; the full dump is unreadable.
	const maxLeaves = 4
	if len(leaves) > maxLeaves {
		leaves = append(leaves[:maxLeaves], fmt.Sprintf("and %d more", len(leaves)-maxLeaves))
	}
	return strings.Join(leaves, "; ")
}

func renderCause(ve *jsonschema.ValidationError) string {
	path := "(document)"
	if len(ve.InstanceLocation) > 0 {
		path = "/" + strings.Join(ve.InstanceLocation, "/")
	}
	if ve.ErrorKind != nil {
		return fmt.Sprintf("%s: %s", path, ve.ErrorKind.LocalizedString(printer))
	}
	return path
}

// Write serialises a result file, indented for reviewability.
//
// The write is atomic: a report that is half-written because the process was
// interrupted would be worse than no file, since it would look like data.
func Write(path string, f *File) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding results: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("installing %s: %w", path, err)
	}
	return nil
}

// Load reads and validates a result file.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	if err := Validate(data); err != nil {
		return nil, fmt.Errorf("%s %w", path, err)
	}

	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", path, err)
	}

	if f.Schema != SchemaVersion {
		return nil, fmt.Errorf("%s has schema %d, but this build reads schema %d", path, f.Schema, SchemaVersion)
	}
	return &f, nil
}
