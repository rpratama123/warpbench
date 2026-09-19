package serverlist

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// schemaResourceURL is the schema's canonical $id. The embedded copy is
// registered under it so that internal $refs resolve without any network access.
const schemaResourceURL = "https://raw.githubusercontent.com/rpratama123/warpbench/main/schema/servers.schema.json"

var (
	schemaOnce sync.Once
	schemaVal  *jsonschema.Schema
	schemaErr  error
)

// compiledSchema compiles the embedded schema once per process.
func compiledSchema() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(embeddedSchema))
		if err != nil {
			schemaErr = fmt.Errorf("parsing embedded schema: %w", err)
			return
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource(schemaResourceURL, doc); err != nil {
			schemaErr = fmt.Errorf("registering embedded schema: %w", err)
			return
		}
		schemaVal, schemaErr = c.Compile(schemaResourceURL)
	})
	return schemaVal, schemaErr
}

var msgPrinter = message.NewPrinter(language.English)

// parse validates and decodes a server list.
//
// A structural problem that is not confined to a single server entry is fatal:
// the document cannot be trusted. A problem confined to one entry drops only
// that entry, with a warning naming it, so one stale host cannot take the whole
// list down.
func parse(data []byte, source Source, origin string) (*Result, error) {
	sch, err := compiledSchema()
	if err != nil {
		return nil, err
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}

	var list List
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("decoding server list: %w", err)
	}

	badEntries := map[int][]string{}
	if err := sch.Validate(doc); err != nil {
		var ve *jsonschema.ValidationError
		if !errors.As(err, &ve) {
			return nil, err
		}
		var docErrs []string
		badEntries, docErrs = splitValidationErrors(ve)
		if len(docErrs) > 0 {
			return nil, fmt.Errorf("does not match schema/servers.schema.json: %s", strings.Join(docErrs, "; "))
		}
	}

	kept, warnings := filterServers(list, badEntries)
	if len(kept) == 0 {
		return nil, errors.New("no usable server entries")
	}
	list.Servers = kept

	return &Result{
		List:     &list,
		Source:   source,
		Origin:   origin,
		Revision: list.Revision,
		Warnings: warnings,
	}, nil
}

// splitValidationErrors separates leaf failures that point at one server entry
// from failures that concern the document as a whole.
func splitValidationErrors(ve *jsonschema.ValidationError) (perEntry map[int][]string, doc []string) {
	perEntry = map[int][]string{}

	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) > 0 {
			for _, c := range e.Causes {
				walk(c)
			}
			return
		}

		loc := e.InstanceLocation
		msg := describe(e)

		if len(loc) >= 2 && loc[0] == "servers" {
			if idx, err := strconv.Atoi(loc[1]); err == nil {
				perEntry[idx] = append(perEntry[idx], fmt.Sprintf("%s %s", pathString(loc), msg))
				return
			}
		}
		doc = append(doc, fmt.Sprintf("%s %s", pathString(loc), msg))
	}
	walk(ve)

	return perEntry, doc
}

func pathString(loc []string) string {
	if len(loc) == 0 {
		return "(document)"
	}
	return "/" + strings.Join(loc, "/")
}

// describe renders a leaf failure in plain English.
func describe(e *jsonschema.ValidationError) string {
	if e.ErrorKind != nil {
		if s := e.ErrorKind.LocalizedString(msgPrinter); s != "" {
			return s
		}
	}
	return "failed validation"
}

// filterServers drops entries that failed schema validation or one of the
// cross-field rules JSON Schema cannot express, and reports each drop.
func filterServers(list List, badEntries map[int][]string) (kept []Server, warnings []string) {
	groups := make(map[string]bool, len(list.Groups))
	for _, g := range list.Groups {
		groups[g.ID] = true
	}

	seen := make(map[string]bool, len(list.Servers))

	for i, s := range list.Servers {
		label := s.ID
		if label == "" {
			label = fmt.Sprintf("#%d", i)
		}

		if reasons := badEntries[i]; len(reasons) > 0 {
			warnings = append(warnings, fmt.Sprintf("skipping server %s: %s", label, strings.Join(reasons, "; ")))
			continue
		}

		if reason := coherenceProblem(s, groups, seen); reason != "" {
			warnings = append(warnings, fmt.Sprintf("skipping server %s: %s", label, reason))
			continue
		}

		seen[s.ID] = true
		kept = append(kept, s)
	}

	return kept, warnings
}

// coherenceProblem enforces the rules that JSON Schema either cannot express or
// expresses too obscurely to be trustworthy.
func coherenceProblem(s Server, groups map[string]bool, seen map[string]bool) string {
	if seen[s.ID] {
		return "duplicate id"
	}
	if !groups[s.Group] {
		return fmt.Sprintf("group %q is not declared in the groups array", s.Group)
	}

	switch s.Protocol {
	case "iperf3":
		if s.Has("timings") {
			// Timings come from httptrace over HTTP; iperf3 has no HTTP surface.
			return "protocol iperf3 cannot provide the timings capability"
		}
		if s.IPerf3 == nil {
			return "protocol iperf3 requires an iperf3 block"
		}
		if len(s.IPerf3.PortRange) == 2 && s.IPerf3.PortRange[0] > s.IPerf3.PortRange[1] {
			return fmt.Sprintf("iperf3 port_range is reversed: %v", s.IPerf3.PortRange)
		}
	default:
		if s.IPerf3 != nil {
			return fmt.Sprintf("protocol %s must not carry an iperf3 block", s.Protocol)
		}
	}

	// A declared capability must have something to exercise it.
	if s.Has("download") && s.Protocol != "iperf3" && derefOrEmpty(s.DownloadURL) == "" {
		return "declares download but has no download_url"
	}
	if s.Has("upload") && s.Protocol != "iperf3" && derefOrEmpty(s.UploadURL) == "" {
		return "declares upload but has no upload_url"
	}

	return ""
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
