package runner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rpratama123/warpbench/internal/netprobe"
	"github.com/rpratama123/warpbench/internal/results"
	"github.com/rpratama123/warpbench/internal/serverlist"
	"github.com/rpratama123/warpbench/internal/throughput"
	"github.com/rpratama123/warpbench/internal/trace"
)

func strptr(s string) *string { return &s }

func server(id, group, tier string, caps ...string) serverlist.Server {
	return serverlist.Server{
		ID:           id,
		Group:        group,
		Name:         "Server " + id,
		Protocol:     "http-file",
		PingHost:     "127.0.0.1",
		DownloadURL:  strptr("https://example.invalid/" + id),
		Capabilities: caps,
		Tier:         tier,
	}
}

func listOf(groups []string, servers ...serverlist.Server) *serverlist.List {
	gs := make([]serverlist.Group, 0, len(groups))
	for _, g := range groups {
		gs = append(gs, serverlist.Group{ID: g, Name: strings.ToUpper(g)})
	}
	return &serverlist.List{Schema: 2, Revision: "2026-09-20", Groups: gs, Servers: servers}
}

// --- ordering --------------------------------------------------------------

// Both phases must measure the same targets in the same sequence, whatever
// order the user happened to toggle them in.
func TestOrderIsCanonicalRegardlessOfInputOrder(t *testing.T) {
	list := listOf([]string{"sg", "eu", "us"},
		server("sg-1", "sg", "quick", "download"),
		server("sg-2", "sg", "extended", "download"),
		server("eu-1", "eu", "quick", "download"),
		server("us-1", "us", "quick", "download"),
	)

	shuffled := []serverlist.Server{list.Servers[3], list.Servers[1], list.Servers[2], list.Servers[0]}
	got := Order(list, shuffled)

	want := []string{"sg-1", "sg-2", "eu-1", "us-1"}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("Order() = %v, want %v", ids(got), want)
		}
	}

	// Ordering repeatedly must be stable.
	for range 5 {
		again := Order(list, got)
		if strings.Join(ids(again), ",") != strings.Join(want, ",") {
			t.Fatalf("Order() is not stable: %v", ids(again))
		}
	}
}

func TestOrderSortsByGroupThenListOrder(t *testing.T) {
	list := listOf([]string{"eu", "sg"},
		server("eu-2", "eu", "quick", "download"),
		server("sg-1", "sg", "quick", "download"),
		server("eu-1", "eu", "quick", "download"),
	)

	got := Order(list, list.Servers)
	want := "eu-2,eu-1,sg-1" // group order first, then the list's own order
	if strings.Join(ids(got), ",") != want {
		t.Errorf("Order() = %v, want %s", ids(got), want)
	}
}

func TestOrderHandlesNilList(t *testing.T) {
	in := []serverlist.Server{server("a", "sg", "quick", "download")}
	got := Order(nil, in)
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("Order(nil, ...) = %v, want the input preserved", ids(got))
	}
}

func TestOrderDoesNotMutateInput(t *testing.T) {
	list := listOf([]string{"sg"}, server("sg-1", "sg", "quick", "download"))
	in := []serverlist.Server{list.Servers[0]}

	_ = Order(list, in)
	if in[0].ID != "sg-1" {
		t.Error("Order mutated its input slice")
	}
}

func ids(servers []serverlist.Server) []string {
	out := make([]string, 0, len(servers))
	for _, s := range servers {
		out = append(out, s.ID)
	}
	return out
}

// --- selection -------------------------------------------------------------

func TestSelectQuickUsesOnlyTheQuickTier(t *testing.T) {
	list := listOf([]string{"sg", "eu"},
		server("sg-quick", "sg", "quick", "download"),
		server("sg-ext", "sg", "extended", "download"),
		server("eu-quick", "eu", "quick", "download"),
	)

	got := Select(list, ModeQuick, nil, nil)
	if strings.Join(ids(got), ",") != "sg-quick,eu-quick" {
		t.Errorf("Select(quick) = %v, want only the quick tier", ids(got))
	}
}

// Extended means thorough: the tier marks what suffices for a fast signal, not
// what a long run is forbidden to touch.
func TestSelectExtendedUsesTheWholeList(t *testing.T) {
	list := listOf([]string{"sg", "eu"},
		server("sg-quick", "sg", "quick", "download"),
		server("sg-ext", "sg", "extended", "download"),
		server("eu-quick", "eu", "quick", "download"),
	)

	got := Select(list, ModeExtended, nil, nil)
	if len(got) != 3 {
		t.Errorf("Select(extended) = %v, want all three servers", ids(got))
	}
}

func TestSelectHonoursGroupsAndExplicitIDs(t *testing.T) {
	list := listOf([]string{"sg", "eu"},
		server("sg-1", "sg", "quick", "download"),
		server("eu-1", "eu", "quick", "download"),
		server("eu-2", "eu", "extended", "download"),
	)

	if got := Select(list, ModeExtended, []string{"eu"}, nil); strings.Join(ids(got), ",") != "eu-1,eu-2" {
		t.Errorf("Select(groups=eu) = %v", ids(got))
	}

	if got := Select(list, ModeQuick, []string{"eu"}, []string{"eu-2"}); strings.Join(ids(got), ",") != "eu-2" {
		t.Errorf("Select(onlyIDs=eu-2) = %v, want the quick-tier filter not to apply to an explicit pick", ids(got))
	}

	if got := Select(list, ModeExtended, nil, []string{"nope"}); len(got) != 0 {
		t.Errorf("Select(unknown id) = %v, want empty", ids(got))
	}
}

// --- budget ----------------------------------------------------------------

func TestBudgetForModes(t *testing.T) {
	quick := BudgetFor(ModeQuick)
	extended := BudgetFor(ModeExtended)

	if quick.DownloadSamples != 1 || quick.UploadSamples != 1 {
		t.Errorf("quick samples = %d/%d, want 1/1", quick.DownloadSamples, quick.UploadSamples)
	}
	if extended.DownloadSamples != 3 || extended.UploadSamples != 3 {
		t.Errorf("extended samples = %d/%d, want 3/3", extended.DownloadSamples, extended.UploadSamples)
	}
	if quick.PingCount != 10 || extended.PingCount != 30 {
		t.Errorf("ping counts = %d/%d, want 10/30", quick.PingCount, extended.PingCount)
	}
	if quick.DownloadDuration != 12*time.Second || quick.UploadDuration != 8*time.Second {
		t.Errorf("quick durations = %v/%v, want 12s/8s", quick.DownloadDuration, quick.UploadDuration)
	}
	if extended.DownloadDuration != 15*time.Second || extended.UploadDuration != 10*time.Second {
		t.Errorf("extended durations = %v/%v, want 15s/10s", extended.DownloadDuration, extended.UploadDuration)
	}
	// The methodology default that PLAN section 5.2 calls for.
	if quick.Warmup != time.Second {
		t.Errorf("warm-up = %v, want 1s", quick.Warmup)
	}
}

func TestBudgetWithParallel(t *testing.T) {
	if got := BudgetFor(ModeQuick).WithParallel(4).Parallel; got != 4 {
		t.Errorf("WithParallel(4) = %d", got)
	}
	if got := BudgetFor(ModeQuick).WithParallel(0).Parallel; got != 1 {
		t.Errorf("WithParallel(0) = %d, want 1", got)
	}
}

// Only capabilities the server declares should be budgeted, or the estimate
// would promise time the run will not spend.
func TestPerServerEstimateFollowsDeclaredCapabilities(t *testing.T) {
	b := BudgetFor(ModeQuick)

	downloadOnly := b.PerServer(server("a", "sg", "quick", "download"))
	withUpload := b.PerServer(server("b", "sg", "quick", "download", "upload"))

	if withUpload <= downloadOnly {
		t.Errorf("an upload-capable server should be estimated longer (%v vs %v)", withUpload, downloadOnly)
	}
	wantDiff := time.Duration(b.UploadSamples) * b.UploadDuration
	if diff := withUpload - downloadOnly; diff != wantDiff {
		t.Errorf("the difference should be the upload budget (%v), got %v", wantDiff, diff)
	}
}

func TestEstimateScalesWithSelection(t *testing.T) {
	b := BudgetFor(ModeQuick)
	one := server("a", "sg", "quick", "download")

	single := Estimate([]serverlist.Server{one}, b)
	triple := Estimate([]serverlist.Server{one, one, one}, b)

	if triple != 3*single {
		t.Errorf("Estimate(3) = %v, want 3x %v", triple, single)
	}
	if got := Estimate(nil, b); got != 0 {
		t.Errorf("Estimate(nil) = %v, want 0", got)
	}
}

// --- end-to-end run --------------------------------------------------------

// streamingServer serves an endless body and accepts uploads.
func streamingServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		buf := make([]byte, 64<<10)
		for {
			if _, err := w.Write(buf); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			default:
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func traceServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// recordingProgress captures the callbacks so the contract can be asserted.
type recordingProgress struct {
	mu       sync.Mutex
	servers  []string
	steps    []string
	traces   []string
	estimate time.Duration
	phase    string
}

func (p *recordingProgress) PhaseStarted(phase string, servers []serverlist.Server, estimate time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phase = phase
	p.estimate = estimate
	for _, s := range servers {
		p.servers = append(p.servers, s.ID)
	}
}

func (p *recordingProgress) ServerStarted(int, int, serverlist.Server) {}

func (p *recordingProgress) Step(s serverlist.Server, step string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.steps = append(p.steps, s.ID+":"+step)
}

func (p *recordingProgress) Trace(r trace.Result) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.traces = append(p.traces, r.Stage)
}

func (p *recordingProgress) ServerDone(serverlist.Server, results.Server) {}

func testBudget() Budget {
	return Budget{
		PingCount:        0, // ping is covered by its own unit tests
		PingInterval:     10 * time.Millisecond,
		PingTimeout:      time.Second,
		TimingSamples:    2,
		DownloadSamples:  2,
		UploadSamples:    1,
		DownloadDuration: 200 * time.Millisecond,
		UploadDuration:   150 * time.Millisecond,
		Warmup:           20 * time.Millisecond,
		MinBytes:         1,
		MaxBytes:         64 << 20,
		Parallel:         1,
	}
}

func TestRunProducesACompleteFile(t *testing.T) {
	srv := streamingServer(t)
	tr := traceServer(t, "ip=203.0.113.9\ncolo=SIN\nloc=ID\nwarp=off\ngateway=off\n")

	list := listOf([]string{"sg", "eu"},
		serverlist.Server{
			ID: "eu-1", Group: "eu", Name: "EU One", Protocol: "http-file",
			PingHost: "127.0.0.1", DownloadURL: strptr(srv.URL + "/100MB.bin"),
			Capabilities: []string{"download", "upload", "timings"}, Tier: "extended",
			Provider: "Test", City: "Amsterdam", Country: "NL",
		},
		serverlist.Server{
			ID: "sg-1", Group: "sg", Name: "SG One", Protocol: "http-file",
			PingHost: "127.0.0.1", DownloadURL: strptr(srv.URL + "/100MB.bin"),
			Capabilities: []string{"download"}, Tier: "quick",
		},
	)

	prog := &recordingProgress{}
	cfg := Config{
		Phase:            "baseline",
		Mode:             ModeQuick,
		List:             list,
		Servers:          Select(list, ModeQuick, nil, nil),
		Budget:           testBudget(),
		Masked:           true,
		ServerListSource: serverlist.SourceEmbedded,
		ServerListOrigin: "embedded:servers.json",
		Progress:         prog,
		Tool:             results.Tool{Version: "test", Commit: "abc", Go: "go-test"},
	}

	file, err := Run(context.Background(), cfg, Deps{
		TraceDoer: http.DefaultClient,
		TraceURL:  tr.URL,
		Client:    throughput.NewHTTPClient(0),
		UserAgent: "warpbench-test",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if file.Schema != results.SchemaVersion {
		t.Errorf("Schema = %d, want %d", file.Schema, results.SchemaVersion)
	}
	if file.Phase != "baseline" {
		t.Errorf("Phase = %q", file.Phase)
	}
	if file.Tool.Version != "test" {
		t.Errorf("Tool.Version = %q", file.Tool.Version)
	}
	if file.ServerList.Revision != "2026-09-20" || file.ServerList.Source != "embedded" {
		t.Errorf("ServerList = %+v", file.ServerList)
	}
	if file.Environment.OS == "" || file.Environment.Arch == "" {
		t.Errorf("Environment = %+v, want the platform recorded", file.Environment)
	}
	if file.StartedAt.IsZero() || file.EndedAt.IsZero() {
		t.Error("timestamps are zero")
	}
	if file.DurationSec < 0 {
		t.Errorf("DurationSec = %v", file.DurationSec)
	}

	// Quick mode selects the quick tier, so eu-1 (extended) must be absent.
	if len(file.Servers) != 1 || file.Servers[0].ID != "sg-1" {
		t.Fatalf("measured %v, want only sg-1", measuredIDs(file))
	}

	if file.Configuration.Mode != "quick" {
		t.Errorf("Configuration.Mode = %q", file.Configuration.Mode)
	}
	if file.Configuration.Parallel != 1 || file.Configuration.Masked != true {
		t.Errorf("Configuration = %+v", file.Configuration)
	}

	// Two traces: before and after the phase.
	if len(file.Traces) != 2 {
		t.Fatalf("got %d traces, want before and after", len(file.Traces))
	}
	if file.Traces[0].Stage != "before-baseline" || file.Traces[1].Stage != "after-baseline" {
		t.Errorf("trace stages = %q, %q", file.Traces[0].Stage, file.Traces[1].Stage)
	}
	if file.Traces[0].Warp != "off" {
		t.Errorf("trace warp = %q, want off", file.Traces[0].Warp)
	}
	// Reports are published, so the address must arrive masked.
	if file.Traces[0].IP != "203.0.113.0/24" {
		t.Errorf("trace IP = %q, want it masked", file.Traces[0].IP)
	}

	measured := file.Servers[0]
	if measured.Download == nil || len(measured.Download.Samples) != 2 {
		t.Fatalf("download series = %+v, want two samples", measured.Download)
	}
	if measured.Download.MedianSteadyMbps <= 0 {
		t.Error("median steady rate should be positive against a streaming server")
	}
	if measured.ResolvedIP == "" {
		t.Error("ResolvedIP is empty; the contacted address must be recorded")
	}
	if measured.Upload != nil {
		t.Error("sg-1 does not declare upload, so the series must be absent")
	}

	// Progress must have reported the phase and each step, or a UI has nothing
	// to draw.
	if prog.phase != "baseline" {
		t.Errorf("PhaseStarted phase = %q", prog.phase)
	}
	if prog.estimate <= 0 {
		t.Error("PhaseStarted estimate = 0")
	}
	if len(prog.servers) != 1 || prog.servers[0] != "sg-1" {
		t.Errorf("PhaseStarted servers = %v", prog.servers)
	}
	if len(prog.steps) == 0 {
		t.Error("no Step callbacks were delivered")
	}
	if len(prog.traces) != 2 {
		t.Errorf("Trace callbacks = %v, want one per reading", prog.traces)
	}
}

// The end-to-end run must be serialisable and conform to the published schema,
// which is the contract --compare relies on.
func TestRunOutputPassesItsOwnSchema(t *testing.T) {
	srv := streamingServer(t)
	tr := traceServer(t, "ip=198.51.100.7\ncolo=SIN\nwarp=on\n")

	list := listOf([]string{"sg"}, serverlist.Server{
		ID: "sg-1", Group: "sg", Name: "SG One", Protocol: "http-file",
		PingHost: "127.0.0.1", DownloadURL: strptr(srv.URL + "/f.bin"),
		Capabilities: []string{"download", "upload", "timings"}, Tier: "quick",
	})

	file, err := Run(context.Background(), Config{
		Phase:            "warp",
		Mode:             ModeQuick,
		List:             list,
		Servers:          list.Servers,
		Budget:           testBudget(),
		Masked:           false,
		ServerListSource: serverlist.SourceRemote,
		ServerListOrigin: "https://example.invalid/servers.json",
		Tool:             results.Tool{Version: "test"},
	}, Deps{TraceDoer: http.DefaultClient, TraceURL: tr.URL, Client: throughput.NewHTTPClient(0)})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := results.Validate(data); err != nil {
		t.Fatalf("the run's own output does not satisfy the results schema: %v", err)
	}
}

// One broken target must not lose the rest of the run.
func TestRunContinuesWhenAServerFails(t *testing.T) {
	srv := streamingServer(t)
	tr := traceServer(t, "warp=off\n")

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()

	list := listOf([]string{"sg", "eu"},
		serverlist.Server{
			ID: "sg-broken", Group: "sg", Name: "Broken", Protocol: "http-file",
			PingHost: "127.0.0.1", DownloadURL: strptr(broken.URL + "/f.bin"),
			Capabilities: []string{"download"}, Tier: "quick",
		},
		serverlist.Server{
			ID: "sg-ok", Group: "sg", Name: "Fine", Protocol: "http-file",
			PingHost: "127.0.0.1", DownloadURL: strptr(srv.URL + "/f.bin"),
			Capabilities: []string{"download"}, Tier: "quick",
		},
	)

	file, err := Run(context.Background(), Config{
		Phase: "baseline", Mode: ModeQuick, List: list, Servers: list.Servers,
		Budget: testBudget(), ServerListSource: serverlist.SourceEmbedded,
	}, Deps{TraceDoer: http.DefaultClient, TraceURL: tr.URL, Client: throughput.NewHTTPClient(0)})
	if err != nil {
		t.Fatalf("Run() error = %v, want the failure contained", err)
	}

	if len(file.Servers) != 2 {
		t.Fatalf("measured %d servers, want both attempted", len(file.Servers))
	}

	brokenOut := file.Servers[0]
	if brokenOut.ID != "sg-broken" {
		brokenOut = file.Servers[1]
	}
	if len(brokenOut.Warnings) == 0 {
		t.Error("the failing server recorded no warning")
	}
	if brokenOut.Download != nil {
		t.Error("a failed download must not produce a series that renders as zero")
	}

	var healthy *results.Server
	for i := range file.Servers {
		if file.Servers[i].ID == "sg-ok" {
			healthy = &file.Servers[i]
		}
	}
	if healthy == nil || healthy.Download == nil {
		t.Fatal("the healthy server was lost when its neighbour failed")
	}
}

func TestRunRejectsEmptySelection(t *testing.T) {
	_, err := Run(context.Background(), Config{List: listOf([]string{"sg"})}, Deps{})
	if err == nil {
		t.Error("Run() accepted an empty selection")
	}
	_, err = Run(context.Background(), Config{}, Deps{})
	if err == nil {
		t.Error("Run() accepted a nil server list")
	}
}

// The trace endpoint is recorded even when it cannot be reached: an unknown
// WARP state changes what the result can claim.
func TestRunRecordsAFailedTrace(t *testing.T) {
	srv := streamingServer(t)

	list := listOf([]string{"sg"}, serverlist.Server{
		ID: "sg-1", Group: "sg", Name: "SG", Protocol: "http-file",
		PingHost: "127.0.0.1", DownloadURL: strptr(srv.URL + "/f.bin"),
		Capabilities: []string{"download"}, Tier: "quick",
	})

	file, err := Run(context.Background(), Config{
		Phase: "baseline", Mode: ModeQuick, List: list, Servers: list.Servers,
		Budget: testBudget(), ServerListSource: serverlist.SourceEmbedded,
	}, Deps{
		TraceDoer: http.DefaultClient,
		TraceURL:  "http://127.0.0.1:1/nope",
		Client:    throughput.NewHTTPClient(0),
	})
	if err != nil {
		t.Fatalf("Run() error = %v, want a failed trace to be survivable", err)
	}
	if len(file.Traces) != 2 {
		t.Fatalf("got %d traces, want both attempts recorded", len(file.Traces))
	}
	if file.Traces[0].Err == "" {
		t.Error("the failed trace recorded no error")
	}
	if file.Traces[0].Warp != "unknown" {
		t.Errorf("trace warp = %q, want unknown when the endpoint is unreachable", file.Traces[0].Warp)
	}
}

func TestSummarizePingDropsTheFirstSample(t *testing.T) {
	// The first reply includes cold-path costs -- ARP, route lookup -- that
	// later replies do not, so it is discarded per the methodology.
	got := summarizePing(&netprobe.PingResult{
		Method: netprobe.MethodICMP,
		Target: "example.com",
		Addr:   "203.0.113.1",
		Sent:   4,
		RTTs: []time.Duration{
			500 * time.Millisecond,
			10 * time.Millisecond,
			12 * time.Millisecond,
			14 * time.Millisecond,
		},
	})
	if got.MinMs != 10 {
		t.Errorf("MinMs = %v, want 10: the cold first reply must be dropped", got.MinMs)
	}
	if got.Received != 4 {
		t.Errorf("Received = %d, want 4: loss reflects replies, not kept samples", got.Received)
	}
	if got.LossPct != 0 {
		t.Errorf("LossPct = %v, want 0", got.LossPct)
	}
}

func TestSummarizeTimingsCarriesOccurrence(t *testing.T) {
	got := summarizeTimings(netprobe.Timing{
		Connect:    20 * time.Millisecond,
		Total:      100 * time.Millisecond,
		Proto:      "HTTP/1.1",
		ConnectRan: true,
		TLSRan:     false,
	}, 3)

	if got.Samples != 3 {
		t.Errorf("Samples = %d", got.Samples)
	}
	if !got.ConnectRan || got.TLSRan {
		t.Errorf("occurrence flags = connect:%v tls:%v, want connect ran and tls not", got.ConnectRan, got.TLSRan)
	}
}

func TestMaskIf(t *testing.T) {
	if got := maskIf("203.0.113.9", true); got != "203.0.113.0/24" {
		t.Errorf("maskIf(mask=true) = %q", got)
	}
	if got := maskIf("203.0.113.9", false); got != "203.0.113.9" {
		t.Errorf("maskIf(mask=false) = %q, want the address unchanged", got)
	}
	if got := maskIf("", true); got != "" {
		t.Errorf("maskIf(\"\") = %q", got)
	}
}

func measuredIDs(f *results.File) []string {
	out := make([]string, 0, len(f.Servers))
	for _, s := range f.Servers {
		out = append(out, s.ID)
	}
	return out
}
