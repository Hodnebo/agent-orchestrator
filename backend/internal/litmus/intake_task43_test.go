package litmus

import (
	"strings"
	"testing"
)

func TestIdentifierScannerWorkIsLinearForConstantLookups(t *testing.T) {
	line := strings.Repeat("limit 模型限制 ", 100_000)
	names, work := sourceIdentifiers(line)
	if len(names) != 2 || !names["limit"] || !names["模型限制"] || names["lim"] {
		t.Fatalf("identifier index = %v", names)
	}
	if limit := 2*len(line) + 1; work > limit {
		t.Fatalf("scanned %d work units for %d bytes, limit %d", work, len(line), limit)
	}
}

func TestTernaryScannerWorkIsLinearOnRepeatedQuestions(t *testing.T) {
	line := strings.Repeat("?x", 100_000)
	got, work := scanTernary(line)
	if got {
		t.Fatal("question marks without a colon were reported as a ternary")
	}
	if limit := 5*len(line) + 10; work > limit {
		t.Fatalf("scanned %d work units for %d bytes, limit %d", work, len(line), limit)
	}
}

func TestRegexLiteralScannerWorkIsLinearOnRepeatedOpenClasses(t *testing.T) {
	line := strings.Repeat("[/", 100_000)
	scanner := newRegexLiteralScanner(line)
	for i := 1; i < len(line); i += 2 {
		if _, ok := scanner.end(i); ok {
			t.Fatalf("slash at %d unexpectedly closed a regex", i)
		}
	}
	if limit := 8*len(line) + 10; scanner.work > limit {
		t.Fatalf("scanned %d work units for %d bytes, limit %d", scanner.work, len(line), limit)
	}
}

func TestRegexLiteralScannerKeepsScrubberBehavior(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{`const matcher = /https?:\/\/example\.test\/[a-z]+/gi`, "const matcher = lit"},
		{`const quotient = value / other`, "const quotient = value / other"},
	}
	for _, tt := range cases {
		scrubber := newBranchScrubber("src/example.ts")
		if got := strings.TrimSpace(scrubber.code(tt.line)); got != tt.want {
			t.Errorf("code(%q) = %q, want %q", tt.line, got, tt.want)
		}
	}
	line := `/[a-z]+/gi`
	if end, ok := regexLiteralEnd(line, 0); !ok || end != len(line) {
		t.Fatalf("regexLiteralEnd(%q, 0) = (%d, %t), want (%d, true)", line, end, ok, len(line))
	}
}
