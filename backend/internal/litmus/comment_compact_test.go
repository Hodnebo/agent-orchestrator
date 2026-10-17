package litmus

import (
	"fmt"
	"strings"
	"testing"
)

// Strip all collapsed blocks when measuring what a reader sees initially.
func stripCommentDetails(body string) string {
	var out strings.Builder
	for len(body) > 0 {
		start := strings.Index(body, "<details>")
		if start < 0 {
			out.WriteString(body)
			break
		}
		out.WriteString(body[:start])
		end := strings.Index(body[start:], "</details>")
		if end < 0 {
			break
		}
		body = body[start+end+len("</details>"):]
	}
	return strings.TrimSpace(out.String())
}

func TestCompactRowsKeepFirstWholeSentence(t *testing.T) {
	first := "The menu retained " + strings.Repeat("every long named option ", 12) + "after reopening."
	checks := []Check{{ID: "c1", Name: "menu", State: CheckPartial, Note: first + " The second observation belongs in details."}}
	v := commentVerdict(checks)
	visible := stripCommentDetails(summaryCommentBody(commentRun, v, commentRun.CreatedAt, false))
	if !strings.Contains(visible, first) || strings.Contains(visible, "second observation") || strings.Contains(visible, "...") {
		t.Fatalf("row split a sentence or printed later notes:\n%s", visible)
	}
	body := summaryCommentBody(commentRun, v, commentRun.CreatedAt, false)
	if !strings.Contains(body, "The second observation belongs in details.") {
		t.Fatal("full notes lost")
	}
}

func TestCompactLedgerListsEveryCheckAndDeduplicatesTwins(t *testing.T) {
	checks := []Check{
		{ID: "h1", Name: "first claim", Claim: true, State: CheckPass, Note: "First complete note. More proof."},
		{ID: "b1", Name: "first claim", Leg: "base", State: CheckFail, Note: "Main failed."},
		{ID: "h2", Name: "second claim", Claim: true, State: CheckPass, Note: "Second complete note."},
		{ID: "h3", Name: "skipped claim", Claim: true, State: CheckSkipped, Note: "Missing account. Additional setup details."},
		{ID: "h4", Name: "skipped risk", Purpose: "risk", Risk: "the old state can disappear", State: CheckSkipped, Note: "Missing fixture."},
		{ID: "b4", Name: "skipped risk", Leg: "base", State: CheckSkipped, Note: "Different fixture missing."},
		{ID: "h5", Name: "pending check", State: CheckPending},
		{ID: "b5", Name: "pending check", Leg: "base", State: CheckPending, Note: "The unfinished main check still has setup notes."},
	}
	body := summaryCommentBody(commentRun, commentVerdict(checks), commentRun.CreatedAt, false)
	visible := stripCommentDetails(body)
	if !strings.Contains(visible, "Passed: first claim (main does not do this); second claim (main: not compared)") {
		t.Fatalf("missing passed line:\n%s", visible)
	}
	for _, name := range []string{"first claim", "second claim", "skipped claim", "skipped risk", "pending check"} {
		if strings.Count(visible, name) != 1 {
			t.Errorf("%q not named exactly once:\n%s", name, visible)
		}
	}
	if !strings.Contains(visible, "Not tested: skipped claim (Missing account; main: not compared); skipped risk (Missing fixture; not tested on main); pending check") {
		t.Fatalf("missing compact skipped line:\n%s", visible)
	}
	if strings.Count(body, "<summary>Full check notes</summary>") != 1 || !strings.HasSuffix(body, "</details>") {
		t.Fatalf("full notes block missing or not last:\n%s", body)
	}
	for _, c := range checks {
		if c.Note != "" && !strings.Contains(body, c.Note) {
			t.Errorf("full note lost: %q", c.Note)
		}
	}
	if strings.Contains(visible, "Additional setup details") || strings.Contains(visible, "More proof") {
		t.Fatalf("full notes leaked into visible rows:\n%s", visible)
	}
	if !strings.Contains(body, "the old state can disappear") {
		t.Fatal("risk missing from details")
	}
}

func TestCompactFailureHeadlineOnlyInFirstBugComment(t *testing.T) {
	checks := []Check{
		{ID: "h1", Name: "menu opens", Claim: true, State: CheckFail, Attribution: "pr", Note: "The menu closed."},
		{ID: "h2", Name: "settings save", Claim: true, State: CheckFail, Attribution: "pr", Note: "The settings were lost."},
	}
	const headline = "**Result: something broke; do not merge yet.**"
	for _, bugs := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d bugs", bugs), func(t *testing.T) {
			docs := draftCommentFixture(t, commentRunFixture{run: commentRun, checks: checks[:bugs]})
			if len(docs) != bugs {
				t.Fatalf("draft count = %d, want %d", len(docs), bugs)
			}
			if body := docs[0].Body; strings.Count(body, headline) != 1 || !strings.HasPrefix(body, fmt.Sprintf("%s\n\nBug 1 of %d:\n\n", headline, bugs)) {
				t.Fatalf("first comment must open with exactly one result headline followed by the bug:\n%s", body)
			}
			for i, doc := range docs[1:] {
				if strings.Contains(doc.Body, "**Result:") {
					t.Errorf("comment %d repeats the result headline:\n%s", i+2, doc.Body)
				}
			}
		})
	}
}

func TestCompactFailurePointsToGroupedBug(t *testing.T) {
	checks := []Check{
		{ID: "h1", Name: "initial check", State: CheckFail, Attribution: "pr", Note: "The branch label vanished. The workspace still loaded. A third observation remains in details."},
		{ID: "b1", Name: "initial check", Leg: "base", State: CheckPass},
		{ID: "h2", Name: "visible confirmation", State: CheckFail, Attribution: "pr", SameBug: "h1", Note: "This is the same fault."},
		{ID: "b2", Name: "visible confirmation", Leg: "base", State: CheckPass},
		{ID: "h3", Name: "partial confirmation", State: CheckPartial, Attribution: "pr", SameBug: "h2", Note: "The same fault partly blocked this path."},
	}
	bugs := failingBugComments(checks)
	if len(bugs) != 1 || len(bugs[0].Linked) != 2 {
		t.Fatalf("linked checks became separate bugs: %+v", bugs)
	}
	v := commentVerdict(checks)
	body := bugCommentBody(commentRun, v, bugs[0], "", "", false, true)
	visible := stripCommentDetails(body)
	if !strings.Contains(visible, "Bug 1 of 1:") || !strings.Contains(visible, "Checks: initial check; visible confirmation; partial confirmation") {
		t.Fatalf("group lost its check list:\n%s", visible)
	}
	if strings.Count(visible, "See Bug 1.") != 3 || strings.Count(visible, "The branch label vanished.") != 1 || strings.Contains(visible, "third observation") {
		t.Fatalf("bug rows repeat findings or lose references:\n%s", visible)
	}
	if !strings.Contains(body, "A third observation remains in details.") {
		t.Fatal("full bug note lost")
	}
	checks[2].SameBug = ""
	checks[4].SameBug = ""
	if bugs := failingBugComments(checks); len(bugs) != 3 {
		t.Fatalf("unlinked checks were inferred as one fault: %+v", bugs)
	}
}

func TestCompactPreExistingFindingIsOneVisibleRow(t *testing.T) {
	checks := []Check{
		{ID: "h1", Name: "folder setup", Purpose: "risk", Risk: "setup can return to the wrong folder", State: CheckPartial, Note: "Setup returns home. The folder remains."},
		{ID: "b1", Name: "folder setup", Leg: "base", State: CheckPartial, SameFault: true, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}, Note: "Main returns home too."},
	}
	visible := stripCommentDetails(renderComment(commentRun, commentVerdict(checks), evidence{}, commentRun.CreatedAt))
	if strings.Count(visible, "folder setup") != 1 || !strings.Contains(visible, "folder setup: partial (partly broken on main). Pre-existing. Setup returns home.") {
		t.Fatalf("pre-existing finding repeated or lost:\n%s", visible)
	}
}

func TestTesterProsePreservedInComments(t *testing.T) {
	checks := []Check{{ID: "c1", Name: "branch header", Expect: "Branch header matches the attached open PR head and nondefault base.", State: CheckFail, Attribution: "pr", Note: "SessionBranchSummary sets base to undefined. The base branch and head commit are unchanged; see c1."}}
	v := commentVerdict(checks)
	bugs := failingBugComments(checks)
	body := bugCommentBody(commentRun, v, bugs[0], "", "", false, true)
	for _, want := range []string{checks[0].Expect, checks[0].Note} {
		if !strings.Contains(body, want) {
			t.Errorf("tester words changed: want %q:\n%s", want, body)
		}
	}
	for _, bad := range []string{"sets main to undefined", "open PR the PR build"} {
		if strings.Contains(body, bad) {
			t.Errorf("rewrote prose: %s", body)
		}
	}
}

func TestCompactBugPreservesRecordedEnvironmentCauseAfterComparison(t *testing.T) {
	checks := []Check{
		{ID: "h1", Name: "answer", State: CheckFail, Attribution: "environment", EnvironmentCause: "The configured model was unavailable.", Note: "The request failed."},
		{ID: "b1", Name: "answer", Leg: "base", State: CheckPass, Note: "Main answered."},
	}
	bugs := failingBugComments(checks)
	if len(bugs) != 1 {
		t.Fatalf("later main pass did not expose PR difference: %+v", bugs)
	}
	body := bugCommentBody(commentRun, commentVerdict(checks), bugs[0], "", "", false, true)
	if !strings.Contains(body, "Environment: The configured model was unavailable.") {
		t.Fatalf("recorded cause lost after attribution resolution:\n%s", body)
	}
}

func TestCompactExplicitBugDoesNotRequireMainToRun(t *testing.T) {
	checks := []Check{
		{ID: "h1", Name: "answer", State: CheckFail, Attribution: "pr", Note: "The response failed."},
		{ID: "b1", Name: "answer", Leg: "base", State: CheckSkipped, Note: "The account could not log in."},
	}
	if bugs := failingBugComments(checks); len(bugs) != 1 {
		t.Fatalf("skipped main hid explicit PR finding: %+v", bugs)
	}
}

func TestCompactLedgerDoesNotReferenceMissingBugSections(t *testing.T) {
	checks := []Check{{ID: "h1", Name: "menu", State: CheckFail, Attribution: "pr", Note: "The menu closed. More details stay collapsed."}}
	body := summaryCommentBody(commentRun, commentVerdict(checks), commentRun.CreatedAt, false)
	visible := stripCommentDetails(body)
	if strings.Contains(visible, "see Bug") || !strings.Contains(visible, "The menu closed.") || strings.Contains(visible, "More details") {
		t.Fatalf("ledger points to an absent bug section:\n%s", visible)
	}
}
