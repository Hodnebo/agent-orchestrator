package litmus

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTask42QuotedAPIPathsTrailingBackslash(t *testing.T) {
	line := `const valid = "/api/v1/valid"; const broken = "/api/v1/broken\`
	got := quotedAPIPaths("client.ts", []string{line})
	if want := []string{"/api/v1/valid"}; !slices.Equal(got, want) {
		t.Fatalf("quotedAPIPaths = %q, want %q", got, want)
	}
}

func TestTask42RenderPackHandlesArbitraryText(t *testing.T) {
	large := strings.Repeat("<if>", 200*1024/len("<if>"))
	binary := string([]byte{0xff, 0xfe, 0x00, '\n', 0xc3})
	cases := []struct {
		name   string
		data   intakeData
		recipe string
	}{
		{name: "empty"},
		{
			name: "unclosed quote and trailing backslash",
			data: intakeData{
				repo: "o/r",
				pr:   pullRequest{Number: 1, Body: `An unclosed quote " stays as text.`},
				diff: "diff --git a/frontend/src/app.ts b/frontend/src/app.ts\n@@ -1 +1 @@\n+const path = \"/api/v1/open\\\n",
			},
			recipe: "kind: \"unfinished\\",
		},
		{
			name: "binary",
			data: intakeData{
				repo: "o/r",
				pr:   pullRequest{Number: 2, Title: binary, Body: binary},
				diff: "diff --git a/client.ts b/client.ts\n@@ -1 +1 @@\n+" + binary,
			},
			recipe: binary,
		},
		{
			name: "two hundred kilobyte line",
			data: intakeData{
				repo: "o/r",
				pr:   pullRequest{Number: 3},
				diff: "diff --git a/frontend/src/ui/view.tsx b/frontend/src/ui/view.tsx\n@@ -1 +1 @@\n+" + large,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderPack(tc.data, tc.recipe); got == "" {
				t.Fatal("renderPack returned an empty pack")
			}
		})
	}
}

func TestTask42BranchWordAngleScanIsLinear(t *testing.T) {
	line := strings.Repeat("<if>", 200*1024/len("<if>"))
	_, work := branchAngleSyntaxRanges(line)
	if work > 2*len(line) {
		t.Fatalf("angle scan examined %d bytes for %d input bytes", work, len(line))
	}
	if changedBranchLine(line) {
		t.Fatal("control words inside markup were reported as branches")
	}
}

func TestTask42TestNamesAreEscapedAndOneLine(t *testing.T) {
	diff := "diff --git a/backend/internal/litmus/widget_test.go b/backend/internal/litmus/widget_test.go\n" +
		"@@ -0,0 +1 @@\n" +
		"+\tt.Run(\"first line\\n- injected *text*\\rnext\", func(t *testing.T) {})\n"
	got := authorTestCases(diff)
	want := "## Cases the author's tests name\n\n- first line\\n\\- injected \\*text\\*\\rnext\n"
	if got != want {
		t.Fatalf("author test cases = %q, want %q", got, want)
	}
}

func TestTask42UnknownGHFailureIsPrivate(t *testing.T) {
	svc, _ := newSvc(t)
	gh := svc.gh.(*fakeGH)
	var pr map[string]any
	if err := json.Unmarshal(gh.out["pr view"], &pr); err != nil {
		t.Fatal(err)
	}
	pr["closingIssuesReferences"] = []map[string]any{{
		"number": 7,
		"repository": map[string]any{
			"name":  "agent-orchestrator",
			"owner": map[string]any{"login": "OrchestratorInc"},
		},
	}}
	gh.out["pr view"], _ = json.Marshal(pr)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	private := "remote error at " + filepath.Join(home, "private", "issue.txt")
	gh.err["issue view"] = errors.New(private)
	run, err := svc.Intake(ctx, "s1", prURL)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"I could not read the linked issue #7 (gh request failed)."}; !slices.Equal(run.Gaps, want) {
		t.Fatalf("public gaps = %q, want %q", run.Gaps, want)
	}
	log := readRunFile(t, svc, run, intakeLogFile)
	if strings.Contains(log, home) || !strings.Contains(log, strings.ReplaceAll(private, home, "~")) {
		t.Fatalf("private issue error was not scrubbed and retained: %q", log)
	}
}

func TestTask42GitListFailureIsPrivate(t *testing.T) {
	svc, _ := newSvc(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	private := "fatal: could not read " + filepath.Join(home, "private", "repo.git")
	svc.runner.(*fakeRunner).gitErr["diff --numstat"] = errors.New(private)
	run, err := svc.Intake(ctx, "s1", prURL)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"I could not read the PR's file list with git, so the areas it changes are unknown."}; !slices.Equal(run.Gaps, want) {
		t.Fatalf("public gaps = %q, want %q", run.Gaps, want)
	}
	log := readRunFile(t, svc, run, intakeLogFile)
	if strings.Contains(log, home) || !strings.Contains(log, fmt.Sprintf("Could not read PR file list with git: %s", strings.ReplaceAll(private, home, "~"))) {
		t.Fatalf("private git error was not scrubbed and retained: %q", log)
	}
}

func TestTask42IntakeKeepsFullTrimmedTitle(t *testing.T) {
	svc, _ := newSvc(t)
	gh := svc.gh.(*fakeGH)
	var pr map[string]any
	if err := json.Unmarshal(gh.out["pr view"], &pr); err != nil {
		t.Fatal(err)
	}
	title := "  " + strings.Repeat("A longer title ", 20) + "  "
	pr["title"] = title
	gh.out["pr view"], _ = json.Marshal(pr)
	run, err := svc.Intake(ctx, "s1", prURL)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSpace(title); run.Title != want {
		t.Fatalf("run title length %d, want %d", len(run.Title), len(want))
	}
}
