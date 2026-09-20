package results

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// validFile returns a file that satisfies the schema, used as the control in
// mutation tests.
func validFile() *File {
	return &File{
		Schema:      SchemaVersion,
		Tool:        Tool{Version: "test", Commit: "abc", Go: "go-test"},
		Phase:       "baseline",
		StartedAt:   time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC),
		EndedAt:     time.Date(2026, 9, 20, 8, 6, 0, 0, time.UTC),
		DurationSec: 360,
		Configuration: Configuration{
			Mode:      "quick",
			Groups:    []string{"sg"},
			ServerIDs: []string{"sg-1"},
			Parallel:  1,
		},
		ServerList:  ServerListRef{Schema: 2, Revision: "2026-09-20", Source: "remote", Origin: "https://example.invalid/servers.json"},
		Environment: Environment{OS: "linux", Arch: "amd64", Go: "go-test", LocalTime: "2026-09-20T15:00:00+07:00", Timezone: "WIB"},
		Traces: []Trace{{
			Stage: "before-baseline", At: "2026-09-20T08:00:00Z", Warp: "off", WarpRaw: "off",
			Colo: "SIN", IP: "203.0.113.0/24", Gateway: "off", Loc: "ID", HTTP: "http/2", TLS: "TLSv1.3",
		}},
		Servers: []Server{{
			ID: "sg-1", Name: "SG One", Group: "sg", Protocol: "http-file",
			Provider: "Test", City: "Singapore", Country: "SG", ResolvedIP: "203.0.113.9",
			Ping:    &Ping{Method: "icmp", Sent: 10, Received: 10, AvgMs: 42, JitterMs: 3, P95Ms: 50},
			Timings: &Timings{Samples: 3, ConnectMs: 20, TTFBMs: 48, ConnectRan: true, FirstByteRan: true},
			Download: &Series{
				Metric: "download", Parallel: 1, MedianSteadyMbps: 41.2, MedianOverallMbps: 38,
				Samples: []Sample{{Bytes: 1000, ElapsedMs: 12000, SteadyMbps: 41.2, OverallMbps: 38, Parallel: 1, Proto: "HTTP/1.1"}},
			},
			Warnings: []string{},
		}},
		Warnings: []string{},
	}
}

// --- schema conformance ----------------------------------------------------

func TestValidFilePassesSchema(t *testing.T) {
	data, err := json.Marshal(validFile())
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(data); err != nil {
		t.Fatalf("the control fixture does not satisfy the schema: %v", err)
	}
}

func TestValidateRejectsBadDocuments(t *testing.T) {
	base := func(mutate func(*File)) []byte {
		f := validFile()
		mutate(f)
		data, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	tests := map[string][]byte{
		"wrong schema version": base(func(f *File) { f.Schema = 99 }),
		"missing phase":        base(func(f *File) { f.Phase = "" }),
		"unknown phase":        base(func(f *File) { f.Phase = "before" }),
		"unknown tool field":   []byte(`{"schema":1,"tool":{"version":"a","bogus":1}}`),
		"not json":             []byte("not json at all"),
		"null groups array": base(func(f *File) {
			f.Configuration.Groups = nil
		}),
		"loss above 100": base(func(f *File) { f.Servers[0].Ping.LossPct = 150 }),
		"negative duration": base(func(f *File) {
			f.Servers[0].Download.Samples[0].ElapsedMs = -1
		}),
		"unknown ping method": base(func(f *File) { f.Servers[0].Ping.Method = "udp" }),
		"bad warp state": base(func(f *File) {
			f.Traces[0].Warp = ""
		}),
		"unknown protocol":      base(func(f *File) { f.Servers[0].Protocol = "gopher" }),
		"unknown top-level key": base(func(f *File) { /* replaced below */ }),
	}

	// The unknown-key case needs a hand-built document.
	var raw map[string]any
	if err := json.Unmarshal(base(func(*File) {}), &raw); err != nil {
		t.Fatal(err)
	}
	raw["surprise"] = true
	encoded, _ := json.Marshal(raw)
	tests["unknown top-level key"] = encoded

	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if err := Validate(data); err == nil {
				t.Error("Validate() accepted an invalid document")
			}
		})
	}
}

// The embedded copy exists because go:embed cannot reach outside its package.
func TestEmbeddedSchemaMatchesCanonical(t *testing.T) {
	onDisk, err := os.ReadFile(filepath.Join("..", "..", "schema", "results.schema.json"))
	if err != nil {
		t.Fatalf("reading the canonical schema: %v", err)
	}
	if bytes.Equal(onDisk, schemaJSON) {
		return
	}
	t.Errorf("schema/results.schema.json and its embedded copy have drifted.\n" +
		"Fix: run 'go generate ./...' from the repository root and commit the result.")
}

// --- load and write --------------------------------------------------------

func TestWriteAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	original := validFile()

	if err := Write(path, original); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.Phase != original.Phase || loaded.Schema != original.Schema {
		t.Errorf("round trip changed the header: %+v", loaded)
	}
	if len(loaded.Servers) != 1 || loaded.Servers[0].ID != "sg-1" {
		t.Errorf("round trip changed the servers: %+v", loaded.Servers)
	}
	if loaded.Servers[0].Download.MedianSteadyMbps != 41.2 {
		t.Errorf("throughput did not survive the round trip: %v", loaded.Servers[0].Download.MedianSteadyMbps)
	}
	if !loaded.StartedAt.Equal(original.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", loaded.StartedAt, original.StartedAt)
	}
}

func TestWriteCreatesParentDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "out.json")

	if err := Write(path, validFile()); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("output was not written: %v", err)
	}
}

// A half-written result would look like data, so the write must be atomic.
func TestWriteLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")

	if err := Write(path, validFile()); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

func TestLoadRejectsUnusableFiles(t *testing.T) {
	dir := t.TempDir()

	invalid := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(invalid, []byte(`{"schema":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	future := filepath.Join(dir, "future.json")
	f := validFile()
	f.Schema = 99
	data, _ := json.Marshal(f)
	if err := os.WriteFile(future, data, 0o600); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"missing file": filepath.Join(dir, "nope.json"),
		"malformed":    invalid,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(path); err == nil {
				t.Error("Load() succeeded, want an error")
			}
		})
	}
}

// --- direction -------------------------------------------------------------

// A percentage change is meaningless without knowing which way is better:
// +138% download is an improvement, +138% latency is a disaster.
func TestDirection(t *testing.T) {
	if !HigherIsBetter.Better(10, 20) {
		t.Error("higher-is-better should call an increase an improvement")
	}
	if HigherIsBetter.Better(20, 10) {
		t.Error("higher-is-better should not call a decrease an improvement")
	}
	if !LowerIsBetter.Better(20, 10) {
		t.Error("lower-is-better should call a decrease an improvement")
	}
	if LowerIsBetter.Better(10, 20) {
		t.Error("lower-is-better should not call an increase an improvement")
	}
	if HigherIsBetter.String() != "higher-is-better" || LowerIsBetter.String() != "lower-is-better" {
		t.Error("Direction.String() is wrong")
	}
}

func TestComputeDelta(t *testing.T) {
	d := computeDelta("download", HigherIsBetter, 100, true, 200, true)

	if d.AbsDiff != 100 {
		t.Errorf("AbsDiff = %v, want 100", d.AbsDiff)
	}
	if d.PctChange != 100 {
		t.Errorf("PctChange = %v, want 100", d.PctChange)
	}
	if !d.Comparable() || !d.Improved() {
		t.Error("a doubling should be comparable and an improvement")
	}
}

func TestComputeDeltaHandlesMissingSides(t *testing.T) {
	tests := map[string]struct {
		hasA, hasB bool
		wantNote   string
	}{
		"neither": {false, false, "not measured in either phase"},
		"no base": {false, true, "not measured on the ISP path"},
		"no warp": {true, false, "not measured over WARP"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			d := computeDelta("download", HigherIsBetter, 1, tc.hasA, 2, tc.hasB)
			if d.Comparable() {
				t.Error("Comparable() = true with a missing side")
			}
			if d.Improved() {
				t.Error("Improved() = true with a missing side")
			}
			if !strings.Contains(d.Note, tc.wantNote) {
				t.Errorf("Note = %q, want %q", d.Note, tc.wantNote)
			}
		})
	}
}

// A zero baseline makes a percentage meaningless; the absolute change is still
// useful, and the note must say why.
func TestComputeDeltaZeroBaseline(t *testing.T) {
	d := computeDelta("loss", LowerIsBetter, 0, true, 5, true)

	if d.PctChange != 0 {
		t.Errorf("PctChange = %v, want 0 for a zero baseline", d.PctChange)
	}
	if d.AbsDiff != 5 {
		t.Errorf("AbsDiff = %v, want 5", d.AbsDiff)
	}
	if !strings.Contains(d.Note, "zero") {
		t.Errorf("Note = %q, want it to explain the missing percentage", d.Note)
	}
}

// --- compare ---------------------------------------------------------------

func phaseFile(phase, revision string, started time.Time, servers ...Server) *File {
	f := validFile()
	f.Phase = phase
	f.ServerList.Revision = revision
	f.StartedAt = started
	f.EndedAt = started.Add(6 * time.Minute)
	f.Servers = servers
	return f
}

func measuredServer(id string, downloadMbps float64, avgMs float64) Server {
	return Server{
		ID: id, Name: id, Group: "sg", Protocol: "http-file",
		Ping: &Ping{Method: "icmp", Sent: 10, Received: 10, AvgMs: avgMs, JitterMs: avgMs / 10},
		Download: &Series{Metric: "download", Parallel: 1, MedianSteadyMbps: downloadMbps,
			Samples: []Sample{{Bytes: 1, ElapsedMs: 1, SteadyMbps: downloadMbps}}},
		Warnings: []string{},
	}
}

func TestCompareComputesDeltasInTheRightDirection(t *testing.T) {
	base := phaseFile("baseline", "2026-09-20", time.Now().Add(-20*time.Minute),
		measuredServer("sg-1", 40, 60), measuredServer("sg-2", 40, 60))
	warp := phaseFile("warp", "2026-09-20", time.Now().Add(-10*time.Minute),
		measuredServer("sg-1", 120, 50), measuredServer("sg-2", 20, 80))

	c, err := Compare(base, warp, CompareOptions{})
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}

	if len(c.Servers) != 2 {
		t.Fatalf("got %d comparisons, want 2", len(c.Servers))
	}

	first := c.Servers[0]
	if first.Download.Baseline != 40 || first.Download.Warp != 120 {
		t.Errorf("download = %v -> %v, want 40 -> 120", first.Download.Baseline, first.Download.Warp)
	}
	if !first.Download.Improved() {
		t.Error("a tripled download should count as an improvement")
	}
	if first.Download.PctChange != 200 {
		t.Errorf("PctChange = %v, want 200", first.Download.PctChange)
	}

	// Latency is lower-is-better, so 60 -> 50 improved and 60 -> 80 worsened.
	if !first.Latency.Improved() {
		t.Error("latency 60 -> 50 should count as an improvement")
	}
	if c.Servers[1].Latency.Improved() {
		t.Error("latency 60 -> 80 should not count as an improvement")
	}
	// Throughput went up on one server and down on the other.
	if c.Servers[1].Download.Improved() {
		t.Error("a download falling from 40 to 20 should not count as an improvement")
	}

	if c.Summary.Download.Total != 2 || c.Summary.Download.Improved != 1 {
		t.Errorf("download tally = %+v, want 1 of 2", c.Summary.Download)
	}
	if c.Summary.Latency.Improved != 1 || c.Summary.Latency.Total != 2 {
		t.Errorf("latency tally = %+v, want 1 of 2", c.Summary.Latency)
	}
	if head := c.Summary.Headline(); !strings.Contains(head, "download improved on 1/2") {
		t.Errorf("Headline() = %q", head)
	}
}

// Comparing a file with itself, or two files from the same phase, cannot mean
// anything.
func TestCompareRejectsSamePhase(t *testing.T) {
	a := phaseFile("baseline", "2026-09-20", time.Now(), measuredServer("sg-1", 1, 1))
	b := phaseFile("baseline", "2026-09-20", time.Now(), measuredServer("sg-1", 2, 2))

	if _, err := Compare(a, b, CompareOptions{}); err == nil {
		t.Error("Compare() accepted two baseline files")
	}
	if _, err := Compare(nil, b, CompareOptions{}); err == nil {
		t.Error("Compare() accepted a nil file")
	}
}

// A changed server list invalidates a like-for-like comparison.
func TestCompareGuardsTheRevision(t *testing.T) {
	base := phaseFile("baseline", "2026-09-19", time.Now().Add(-10*time.Minute), measuredServer("sg-1", 40, 60))
	warp := phaseFile("warp", "2026-09-20", time.Now(), measuredServer("sg-1", 80, 50))

	_, err := Compare(base, warp, CompareOptions{})
	if err == nil {
		t.Fatal("Compare() compared two different revisions without --force")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error = %v, want it to name the escape hatch", err)
	}

	c, err := Compare(base, warp, CompareOptions{Force: true})
	if err != nil {
		t.Fatalf("Compare(Force) error = %v", err)
	}
	if len(c.Warnings) == 0 || !strings.Contains(strings.Join(c.Warnings, " "), "revisions") {
		t.Errorf("Warnings = %v, want the revision mismatch recorded", c.Warnings)
	}
}

// Time-of-day drift is the obvious alternative explanation for a difference, so
// a long gap must be surfaced rather than buried.
func TestCompareWarnsAboutTheInterPhaseGap(t *testing.T) {
	start := time.Now().Add(-2 * time.Hour)
	base := phaseFile("baseline", "2026-09-20", start, measuredServer("sg-1", 40, 60))

	// The baseline ends six minutes after it starts.
	warp := phaseFile("warp", "2026-09-20", start.Add(time.Hour), measuredServer("sg-1", 80, 50))

	c, err := Compare(base, warp, CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(c.Warnings, " "), "apart") {
		t.Errorf("Warnings = %v, want the inter-phase gap flagged", c.Warnings)
	}

	// A short gap must not warn.
	soon := phaseFile("warp", "2026-09-20", start.Add(7*time.Minute), measuredServer("sg-1", 80, 50))
	c2, err := Compare(base, soon, CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(c2.Warnings, " "), "apart") {
		t.Errorf("Warnings = %v, want no gap warning for seven minutes", c2.Warnings)
	}
}

// An asymmetry between phases is itself a finding about WARP, not something to
// drop.
func TestCompareReportsAsymmetries(t *testing.T) {
	base := phaseFile("baseline", "2026-09-20", time.Now().Add(-10*time.Minute),
		measuredServer("sg-1", 40, 60), measuredServer("sg-2", 40, 60))
	warp := phaseFile("warp", "2026-09-20", time.Now(), measuredServer("sg-1", 80, 50))

	c, err := Compare(base, warp, CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(c.Warnings, " ")
	if !strings.Contains(joined, "sg-2") {
		t.Errorf("Warnings = %v, want the missing server named", c.Warnings)
	}

	var sg2 *ServerDelta
	for i := range c.Servers {
		if c.Servers[i].ID == "sg-2" {
			sg2 = &c.Servers[i]
		}
	}
	if sg2 == nil {
		t.Fatal("the server missing from the WARP phase was dropped from the comparison")
	}
	if sg2.Download.Comparable() {
		t.Error("a one-sided metric should not be comparable")
	}
}

func TestCompareNotesAnAddressChange(t *testing.T) {
	base := measuredServer("sg-1", 40, 60)
	base.ResolvedIP = "203.0.113.9"
	warp := measuredServer("sg-1", 80, 50)
	warp.ResolvedIP = "198.51.100.4"

	c, err := Compare(
		phaseFile("baseline", "2026-09-20", time.Now().Add(-10*time.Minute), base),
		phaseFile("warp", "2026-09-20", time.Now(), warp),
		CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(strings.Join(c.Servers[0].Notes, " "), "address changed") {
		t.Errorf("Notes = %v, want the address change recorded", c.Servers[0].Notes)
	}
}

// A metric only one phase measured is an asymmetry worth naming.
func TestCompareNotesOneSidedMetrics(t *testing.T) {
	base := measuredServer("sg-1", 40, 60)
	base.Upload = nil
	warp := measuredServer("sg-1", 80, 50)
	warp.Upload = &Series{Metric: "upload", Parallel: 1, MedianSteadyMbps: 10,
		Samples: []Sample{{Bytes: 1, ElapsedMs: 1, SteadyMbps: 10}}}

	c, err := Compare(
		phaseFile("baseline", "2026-09-20", time.Now().Add(-10*time.Minute), base),
		phaseFile("warp", "2026-09-20", time.Now(), warp),
		CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(strings.Join(c.Servers[0].Notes, " "), "upload measured in only one phase") {
		t.Errorf("Notes = %v, want the one-sided upload recorded", c.Servers[0].Notes)
	}
}

func TestCompareCountsServers(t *testing.T) {
	base := phaseFile("baseline", "2026-09-20", time.Now().Add(-10*time.Minute), measuredServer("sg-1", 40, 60))
	warp := phaseFile("warp", "2026-09-20", time.Now(), measuredServer("sg-1", 40, 60), measuredServer("sg-2", 40, 60))

	c, err := Compare(base, warp, CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(c.Warnings, " "), "measured 1 and 2 servers") {
		t.Errorf("Warnings = %v, want the count mismatch recorded", c.Warnings)
	}
}

func TestSummaryHeadlineWithNothingComparable(t *testing.T) {
	if got := (Summary{}).Headline(); got != "no comparable measurements" {
		t.Errorf("Headline() = %q", got)
	}
}

func TestServerByIDAndHasMeasurement(t *testing.T) {
	f := validFile()
	if !f.HasMeasurement() {
		t.Error("HasMeasurement() = false for a file with a download series")
	}
	if len(f.ServerByID()) != 1 {
		t.Errorf("ServerByID() = %v", f.ServerByID())
	}

	empty := &File{}
	if empty.HasMeasurement() {
		t.Error("HasMeasurement() = true for an empty file")
	}
}

func TestConversionHelpers(t *testing.T) {
	if got := Ms(1500 * time.Millisecond); got != 1500 {
		t.Errorf("Ms() = %v, want 1500", got)
	}
	if got := Secs(2 * time.Minute); got != 120 {
		t.Errorf("Secs() = %v, want 120", got)
	}
	if got := Round1(41.27); got != 41.3 {
		t.Errorf("Round1() = %v, want 41.3", got)
	}
}

// --- noise threshold -------------------------------------------------------

// A published report that calls measurement noise a regression is not worth
// reading, so a change too small to matter must read as "same".
func TestSameTreatsTinyChangesAsNoise(t *testing.T) {
	tests := map[string]struct {
		delta Delta
		want  bool
	}{
		"identical": {
			computeDelta("latency_avg", LowerIsBetter, 22.0, true, 22.0, true),
			true,
		},
		"0.01ms on 22ms": {
			computeDelta("latency_avg", LowerIsBetter, 22.0, true, 22.01, true),
			true,
		},
		"0.1% throughput": {
			computeDelta("download", HigherIsBetter, 100, true, 100.1, true),
			true,
		},
		"1% throughput": {
			computeDelta("download", HigherIsBetter, 100, true, 101, true),
			false,
		},
		"0.01pp loss": {
			computeDelta("loss", LowerIsBetter, 0.02, true, 0.03, true),
			true,
		},
		"1pp loss": {
			computeDelta("loss", LowerIsBetter, 0.02, true, 1.02, true),
			false,
		},
		"not comparable": {
			computeDelta("download", HigherIsBetter, 1, false, 2, true),
			false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := tc.delta.Same(); got != tc.want {
				t.Errorf("Same() = %v, want %v (%+v)", got, tc.want, tc.delta)
			}
		})
	}
}

func TestVerdict(t *testing.T) {
	tests := map[string]struct {
		delta Delta
		want  string
	}{
		"faster download": {computeDelta("download", HigherIsBetter, 40, true, 120, true), "better"},
		"slower download": {computeDelta("download", HigherIsBetter, 40, true, 20, true), "worse"},
		"unchanged":       {computeDelta("download", HigherIsBetter, 40, true, 40, true), "same"},
		"lower latency":   {computeDelta("latency_avg", LowerIsBetter, 60, true, 50, true), "better"},
		"higher latency":  {computeDelta("latency_avg", LowerIsBetter, 60, true, 80, true), "worse"},
		"one-sided":       {computeDelta("download", HigherIsBetter, 40, true, 0, false), "n/a"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := tc.delta.Verdict(); got != tc.want {
				t.Errorf("Verdict() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The headline and the table must not disagree about what counts as an
// improvement.
func TestSummaryCountsThroughVerdict(t *testing.T) {
	base := phaseFile("baseline", "2026-09-20", time.Now().Add(-10*time.Minute),
		measuredServer("noise", 100, 50), measuredServer("real", 100, 50))
	warp := phaseFile("warp", "2026-09-20", time.Now(),
		measuredServer("noise", 100.1, 50), measuredServer("real", 200, 50))

	c, err := Compare(base, warp, CompareOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if c.Summary.Download.Improved != 1 {
		t.Errorf("Improved = %d, want 1: a 0.1%% change is noise, not an improvement", c.Summary.Download.Improved)
	}
	if c.Summary.Download.Total != 2 {
		t.Errorf("Total = %d, want 2", c.Summary.Download.Total)
	}
	if !strings.Contains(c.Summary.Headline(), "download improved on 1/2") {
		t.Errorf("Headline() = %q", c.Summary.Headline())
	}
}
