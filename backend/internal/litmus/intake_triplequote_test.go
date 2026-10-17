package litmus

import (
	"strings"
	"testing"
)

func TestBranchScrubberSkipsMultilineTripleQuotedStrings(t *testing.T) {
	tests := []struct {
		name, path, delimiter, inside, real string
	}{
		{name: "Python double-quoted docstring", path: "src/doc.py", delimiter: `"""`, inside: "if x == 0:", real: "if real > 0:"},
		{name: "Python single-quoted docstring", path: "src/doc.py", delimiter: `'''`, inside: "if x == 0:", real: "if real > 0:"},
		{name: "Kotlin", path: "src/doc.kt", delimiter: `"""`, inside: "if (x == 0) {", real: "if (real > 0) {"},
		{name: "Kotlin script", path: "src/doc.kts", delimiter: `"""`, inside: "if (x == 0) {", real: "if (real > 0) {"},
		{name: "Swift", path: "src/doc.swift", delimiter: `"""`, inside: "if x == 0 {", real: "if real > 0 {"},
		{name: "Java", path: "src/doc.java", delimiter: `"""`, inside: "if (x == 0) {", real: "if (real > 0) {"},
		{name: "Scala", path: "src/doc.scala", delimiter: `"""`, inside: "if (x == 0) {", real: "if (real > 0) {"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scrubbedBranchConditions(tt.path, tt.delimiter, tt.inside, tt.delimiter, tt.real)
			if len(got) != 1 || !strings.Contains(got[0], tt.real) {
				t.Fatalf("real branch after multiline string missing or string content leaked: %q", got)
			}
		})
	}
}

func TestBranchScrubberClosesRawTripleStringsAfterBackslash(t *testing.T) {
	for _, path := range []string{"src/check.kt", "src/check.kts", "src/check.scala"} {
		t.Run(path, func(t *testing.T) {
			got := scrubbedBranchConditions(path, `val text = """`, "if (hidden == 0) {", `literal backslash\"""`, "if (real > 0) {")
			if len(got) != 1 || !strings.Contains(got[0], "if (real > 0)") {
				t.Fatalf("branch after closing delimiter following a backslash missing or hidden branch leaked: %q", got)
			}
		})
	}
}

func TestBranchScrubberResetsOrdinaryQuotesPerLineInTripleQuoteLanguages(t *testing.T) {
	for _, path := range []string{"src/check.py", "src/check.kt", "src/check.kts", "src/check.swift", "src/check.java", "src/check.scala"} {
		t.Run(path, func(t *testing.T) {
			got := scrubbedBranchConditions(path, `value = "unterminated`, "if afterDouble > 0:", "value = 'unterminated", "if afterSingle > 0:")
			if len(got) != 2 || !strings.Contains(got[0], "afterDouble") || !strings.Contains(got[1], "afterSingle") {
				t.Errorf("ordinary quote state leaked past its line: %q", got)
			}
		})
	}
}
