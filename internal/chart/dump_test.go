package chart

import (
	"os"
	"strings"
	"testing"
)

func TestDump(t *testing.T) {
	if os.Getenv("WARPBENCH_DUMP") == "" {
		t.Skip("set WARPBENCH_DUMP to regenerate the golden output")
	}
	out := strings.Join(trimAll(Render(rows(), Options{Width: 80, Chars: Unicode})), "\n") +
		"\n---\n" +
		strings.Join(trimAll(Render(rows(), Options{Width: 60, Chars: ASCII})), "\n")
	if err := os.WriteFile("/tmp/chart-golden.txt", []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
}

func trimAll(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimRight(l, " ")
	}
	return out
}
