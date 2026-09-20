package chart

import (
	"strings"
	"testing"
)

// This pins the rendered format. It is not a tautology: it is the file that
// fails when a layout change silently moves a column, drops a bar, or starts
// wrapping, and it doubles as documentation of what the output looks like.
//
// Regenerate deliberately with WARPBENCH_DUMP=1 go test ./internal/chart/ -run TestDump
// after reviewing the change, never to make a failure go away.
func TestGoldenFormat(t *testing.T) {
	wantUnicode := []string{
		"sg-linode            ISP  █████████████░░░░░░░░░░░░░░░░░░░░░░░ 41.2 Mbps",
		"                     WARP ████████████████████████████████████ 118.4 Mbps  +187%",
		"jp-vultr             ISP  ██████████░░░░░░░░░░░░░░░░░░░░░░░░░░ 33.8 Mbps",
		"                     WARP █████████████████████████████░░░░░░░ 96.1 Mbps   +184%",
		"id-myrepublic-iperf3 ISP  ████████████████████████████░░░░░░░░ 92.4 Mbps",
		"                     WARP ███████████████████████████░░░░░░░░░ 88.7 Mbps   -4%",
	}

	wantASCII := []string{
		"sg-linode            ISP  ######.......... 41.2 Mbps",
		"                     WARP ################ 118.4 Mbps  +187%",
		"jp-vultr             ISP  #####........... 33.8 Mbps",
		"                     WARP #############... 96.1 Mbps   +184%",
		"id-myrepublic-iperf3 ISP  ############.... 92.4 Mbps",
		"                     WARP ############.... 88.7 Mbps   -4%",
	}

	got := trimAll(Render(rows(), Options{Width: 80, Chars: Unicode}))
	assertGolden(t, "unicode/80", wantUnicode, got)

	got = trimAll(Render(rows(), Options{Width: 60, Chars: ASCII}))
	assertGolden(t, "ascii/60", wantASCII, got)
}

func assertGolden(t *testing.T, name string, want, got []string) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("%s: got %d lines, want %d\n--- got ---\n%s", name, len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s line %d:\n  got  %q\n  want %q", name, i, got[i], want[i])
		}
	}
}
