package serverlist

import (
	"fmt"
	"strings"
	"testing"
)

// goodServer is a minimal valid entry used as the control in mutation tests.
const goodServer = `{
	"id": "sg-ok",
	"group": "sg",
	"name": "OK",
	"protocol": "http-file",
	"ping_host": "example.com",
	"download_url": "https://example.com/100MB.bin",
	"capabilities": ["ping", "download"],
	"tier": "quick"
}`

func listWith(servers ...string) []byte {
	return []byte(`{
		"schema": 2,
		"revision": "2026-09-20",
		"groups": [{"id": "sg", "name": "Singapore"}],
		"servers": [` + strings.Join(servers, ",") + `]
	}`)
}

func mustParse(t *testing.T, data []byte) *Result {
	t.Helper()
	res, err := parse(data, SourceEmbedded, "test")
	if err != nil {
		t.Fatalf("parse() error = %v", err)
	}
	return res
}

// The list we actually ship must validate cleanly and produce no warnings. If
// this fails, servers.json is wrong, not the test.
func TestEmbeddedListIsValidAndWarningFree(t *testing.T) {
	res := mustParse(t, embeddedList)

	if got, want := res.List.Schema, 2; got != want {
		t.Errorf("schema = %d, want %d", got, want)
	}
	if res.List.Revision == "" {
		t.Error("revision is empty; every report records it")
	}
	if len(res.Warnings) != 0 {
		t.Errorf("shipped list produced warnings: %v", res.Warnings)
	}
	if len(res.List.Servers) < 20 {
		t.Errorf("only %d servers; the curated list should be broader", len(res.List.Servers))
	}
}

// The plan promises upload coverage in every group, which is the whole reason
// iperf3 was promoted to a first-class protocol. Enforce it.
func TestQuickTierCoversEveryGroupIncludingUpload(t *testing.T) {
	res := mustParse(t, embeddedList)

	quickByGroup := map[string][]Server{}
	for _, s := range res.List.ByTier("quick") {
		quickByGroup[s.Group] = append(quickByGroup[s.Group], s)
	}

	for _, g := range res.List.Groups {
		servers := quickByGroup[g.ID]
		if len(servers) == 0 {
			t.Errorf("group %s has no quick-tier server, so quick mode skips it entirely", g.ID)
			continue
		}

		var withUpload bool
		for _, s := range servers {
			if s.Has("upload") {
				withUpload = true
			}
		}
		if !withUpload {
			t.Errorf("group %s has no quick-tier upload target; upload is a first-class metric", g.ID)
		}
	}
}

func TestEveryServerHasADeclaredGroup(t *testing.T) {
	res := mustParse(t, embeddedList)
	groups := res.List.GroupByID()

	for _, s := range res.List.Servers {
		if _, ok := groups[s.Group]; !ok {
			t.Errorf("server %s references undeclared group %q", s.ID, s.Group)
		}
	}
}

func TestServerIDsAreUnique(t *testing.T) {
	res := mustParse(t, embeddedList)
	seen := map[string]bool{}
	for _, s := range res.List.Servers {
		if seen[s.ID] {
			t.Errorf("duplicate server id %q", s.ID)
		}
		seen[s.ID] = true
	}
}

// A single bad entry must be dropped with a warning naming it, and must not
// take the rest of the list down with it.
func TestBadEntriesAreSkippedIndividually(t *testing.T) {
	tests := map[string]struct {
		server string
		expect string
	}{
		"unknown protocol": {
			server: `{"id":"bad-x","group":"sg","name":"X","protocol":"ftp","ping_host":"e.com","download_url":"https://e.com/f","capabilities":["ping","download"],"tier":"quick"}`,
			expect: "skipping server bad-x",
		},
		"declares download without a url": {
			server: `{"id":"bad-x","group":"sg","name":"X","protocol":"http-file","ping_host":"e.com","capabilities":["ping","download"],"tier":"quick"}`,
			expect: "skipping server bad-x",
		},
		"declares upload without a url": {
			server: `{"id":"bad-x","group":"sg","name":"X","protocol":"http-file","ping_host":"e.com","download_url":"https://e.com/f","capabilities":["ping","download","upload"],"tier":"quick"}`,
			expect: "skipping server bad-x",
		},
		"undeclared group": {
			server: `{"id":"bad-x","group":"nowhere","name":"X","protocol":"http-file","ping_host":"e.com","download_url":"https://e.com/f","capabilities":["ping","download"],"tier":"quick"}`,
			expect: `group "nowhere" is not declared`,
		},
		"duplicate id": {
			server: `{"id":"sg-ok","group":"sg","name":"Dup","protocol":"http-file","ping_host":"e.com","download_url":"https://e.com/f","capabilities":["ping","download"],"tier":"quick"}`,
			expect: "duplicate id",
		},
		"unknown tier": {
			server: `{"id":"bad-x","group":"sg","name":"X","protocol":"http-file","ping_host":"e.com","download_url":"https://e.com/f","capabilities":["ping","download"],"tier":"medium"}`,
			expect: "skipping server bad-x",
		},
		"unknown capability": {
			server: `{"id":"bad-x","group":"sg","name":"X","protocol":"http-file","ping_host":"e.com","download_url":"https://e.com/f","capabilities":["ping","teleport"],"tier":"quick"}`,
			expect: "skipping server bad-x",
		},
		"iperf3 without an iperf3 block": {
			server: `{"id":"bad-x","group":"sg","name":"X","protocol":"iperf3","ping_host":"e.com","capabilities":["ping","download"],"tier":"quick"}`,
			expect: "skipping server bad-x",
		},
		"iperf3 claiming timings": {
			server: `{"id":"bad-x","group":"sg","name":"X","protocol":"iperf3","ping_host":"e.com","capabilities":["ping","download","timings"],"tier":"quick","iperf3":{"host":"e.com","port_range":[5201,5210],"reverse_download":true}}`,
			expect: "cannot provide the timings capability",
		},
		"reversed iperf3 port range": {
			server: `{"id":"bad-x","group":"sg","name":"X","protocol":"iperf3","ping_host":"e.com","capabilities":["ping","download"],"tier":"quick","iperf3":{"host":"e.com","port_range":[5210,5201],"reverse_download":true}}`,
			expect: "port_range is reversed",
		},
		"non-iperf3 carrying an iperf3 block": {
			server: `{"id":"bad-x","group":"sg","name":"X","protocol":"http-file","ping_host":"e.com","download_url":"https://e.com/f","capabilities":["ping","download"],"tier":"quick","iperf3":{"host":"e.com","port_range":[5201,5201],"reverse_download":true}}`,
			expect: "skipping server bad-x",
		},
		"unknown property": {
			server: `{"id":"bad-x","group":"sg","name":"X","protocol":"http-file","ping_host":"e.com","download_url":"https://e.com/f","capabilities":["ping","download"],"tier":"quick","wat":1}`,
			expect: "skipping server bad-x",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			res := mustParse(t, listWith(goodServer, tc.server))

			if len(res.List.Servers) != 1 {
				t.Fatalf("kept %d servers, want only the good one: %+v", len(res.List.Servers), res.List.Servers)
			}
			if res.List.Servers[0].ID != "sg-ok" {
				t.Errorf("kept %q, want the good entry sg-ok", res.List.Servers[0].ID)
			}

			joined := strings.Join(res.Warnings, " | ")
			if !strings.Contains(joined, tc.expect) {
				t.Errorf("warnings = %q, want one containing %q", joined, tc.expect)
			}
		})
	}
}

// Problems with the document itself must be fatal: a list whose shape is wrong
// cannot be partially trusted.
func TestDocumentLevelProblemsAreFatal(t *testing.T) {
	tests := map[string]string{
		"wrong schema version": `{"schema":1,"revision":"2026-09-20","groups":[{"id":"sg","name":"S"}],"servers":[` + goodServer + `]}`,
		"missing revision":     `{"schema":2,"groups":[{"id":"sg","name":"S"}],"servers":[` + goodServer + `]}`,
		"bad revision format":  `{"schema":2,"revision":"yesterday","groups":[{"id":"sg","name":"S"}],"servers":[` + goodServer + `]}`,
		"no groups":            `{"schema":2,"revision":"2026-09-20","groups":[],"servers":[` + goodServer + `]}`,
		"unknown top-level key": `{"schema":2,"revision":"2026-09-20","groups":[{"id":"sg","name":"S"}],
			"servers":[` + goodServer + `],"extra":true}`,
		"not json": `this is not json`,
	}

	for name, doc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parse([]byte(doc), SourceEmbedded, "test"); err == nil {
				t.Error("parse() succeeded, want a fatal error")
			}
		})
	}
}

func TestAllEntriesBadIsFatal(t *testing.T) {
	bad := `{"id":"bad-x","group":"sg","name":"X","protocol":"ftp","ping_host":"e.com","capabilities":["ping"],"tier":"quick"}`

	_, err := parse(listWith(bad), SourceEmbedded, "test")
	if err == nil {
		t.Fatal("parse() succeeded with no usable entries")
	}
	if !strings.Contains(err.Error(), "no usable server entries") {
		t.Errorf("error = %v, want it to mention no usable entries", err)
	}
}

// Every drop must be reported; silently shrinking the list would quietly change
// what a published result means.
func TestEveryDroppedEntryIsWarnedAbout(t *testing.T) {
	bad := `{"id":"bad-1","group":"sg","name":"X","protocol":"ftp","ping_host":"e.com","capabilities":["ping"],"tier":"quick"}`
	res := mustParse(t, listWith(goodServer, bad))

	if len(res.Warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", res.Warnings)
	}
	if !strings.Contains(res.Warnings[0], "bad-1") {
		t.Errorf("warning %q does not name the dropped entry", res.Warnings[0])
	}
}

func TestAdapterOptsRoundTrip(t *testing.T) {
	res := mustParse(t, embeddedList)

	var found bool
	for _, s := range res.List.Servers {
		if s.AdapterOpts != nil {
			found = true
			if s.AdapterOpts.MaxBytes <= 0 {
				t.Errorf("server %s has adapter_opts with a non-positive max_bytes", s.ID)
			}
		}
	}
	if !found {
		t.Error("no server carries adapter_opts; the domestic mirror should cap its payload")
	}
}

func TestSelectedAndByTier(t *testing.T) {
	res := mustParse(t, embeddedList)
	list := res.List

	if got := len(list.Selected(nil)); got != len(list.Servers) {
		t.Errorf("Selected(nil) = %d servers, want all %d", got, len(list.Servers))
	}

	got := list.Selected([]string{"sg"})
	if len(got) == 0 {
		t.Fatal("Selected(sg) returned nothing")
	}
	for _, s := range got {
		if s.Group != "sg" {
			t.Errorf("Selected(sg) included %s from group %s", s.ID, s.Group)
		}
	}

	quick := list.ByTier("quick")
	extended := list.ByTier("extended")
	if len(quick)+len(extended) != len(list.Servers) {
		t.Errorf("quick(%d) + extended(%d) != total(%d)", len(quick), len(extended), len(list.Servers))
	}
}

func TestDescribeAndPathHelpers(t *testing.T) {
	if got := pathString(nil); got != "(document)" {
		t.Errorf("pathString(nil) = %q", got)
	}
	if got := pathString([]string{"servers", "0", "id"}); got != "/servers/0/id" {
		t.Errorf("pathString = %q", got)
	}
}

func TestCoherenceProblemEmptyForGoodServer(t *testing.T) {
	res := mustParse(t, listWith(goodServer))
	if len(res.List.Servers) != 1 {
		t.Fatalf("expected the good server to survive, got %d", len(res.List.Servers))
	}
	s := res.List.Servers[0]
	if got := coherenceProblem(s, map[string]bool{"sg": true}, map[string]bool{}); got != "" {
		t.Errorf("coherenceProblem() = %q, want empty", got)
	}
	if !s.Has("download") || s.Has("upload") {
		t.Errorf("Has() misreports capabilities for %s", s.ID)
	}
	if want := fmt.Sprintf("%s (%s)", s.ID, s.Name); s.String() != want {
		t.Errorf("String() = %q, want %q", s.String(), want)
	}
}
