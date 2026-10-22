package main

import "testing"

func TestArtifactSummaryNamesUpToTwoFiles(t *testing.T) {
	files := func(paths ...string) []artifactFile {
		out := make([]artifactFile, 0, len(paths))
		for _, path := range paths {
			out = append(out, artifactFile{Path: path})
		}
		return out
	}
	cases := []struct {
		files []artifactFile
		want  string
	}{
		{nil, "-"},
		{files("report.html"), "report.html"},
		{files("report.html", "notes.md"), "report.html, notes.md"},
		{files("a.md", "b.md", "c.md", "d.md"), "a.md, b.md (+2)"},
	}
	for _, tc := range cases {
		if got := artifactSummary(tc.files); got != tc.want {
			t.Errorf("artifactSummary(%d files) = %q, want %q", len(tc.files), got, tc.want)
		}
	}
}
