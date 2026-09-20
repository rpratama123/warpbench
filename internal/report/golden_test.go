package report

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testCommandLine is what the golden report was generated with.
const testCommandLine = "warpbench --quick --groups id,sg,eu --phase baseline --out baseline.json\n" +
	"warpbench --quick --groups id,sg,eu --phase warp --out warp.json\n" +
	"warpbench --compare --report report.md baseline.json warp.json"

func goldenPath() string { return filepath.Join("testdata", "report.md") }

// TestMarkdownGolden pins the whole document.
//
// A report is a published artefact, so its shape is part of the deliverable: a
// change that silently drops the caveats section or the reproduce block would
// otherwise go unnoticed while every unit test still passed.
//
// Regenerate deliberately with WARPBENCH_UPDATE_GOLDEN=1 after reviewing the
// diff, never to make a failure go away.
func TestMarkdownGolden(t *testing.T) {
	got := Markdown(fixture(), Options{CommandLine: testCommandLine})

	if os.Getenv("WARPBENCH_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(goldenPath(), got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("golden updated")
		return
	}

	want, err := os.ReadFile(goldenPath())
	if err != nil {
		t.Fatalf("reading the golden file: %v\nrun with WARPBENCH_UPDATE_GOLDEN=1 to create it", err)
	}

	if bytes.Equal(got, want) {
		return
	}

	// Point at the first difference rather than dumping two long documents.
	gotLines := strings.Split(string(got), "\n")
	wantLines := strings.Split(string(want), "\n")
	for i := range max(len(gotLines), len(wantLines)) {
		var g, w string
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if g != w {
			t.Fatalf("report differs at line %d:\n  got  %q\n  want %q\n\nReview the change, then run WARPBENCH_UPDATE_GOLDEN=1 go test ./internal/report/", i+1, g, w)
		}
	}
	t.Fatal("report differs")
}
