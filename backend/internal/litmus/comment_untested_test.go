package litmus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const explicitTesterSummary = "The completed checks found the expected behavior.\n\nReview links and **upgrade dedup** were not checked."

func TestCommentsRenderTesterSummaryInFull(t *testing.T) {
	summary := Verdict{
		Overall: CheckPass,
		Summary: explicitTesterSummary,
		Checks:  []Check{{ID: "c1", Name: "batched review", Claim: true, State: CheckPass, Note: "The review was delivered.", Receipts: []Receipt{{Seq: 1}}}},
	}
	for name, body := range map[string]string{
		"render":  renderComment(commentRun, summary, evidence{}, commentRun.CreatedAt),
		"summary": summaryCommentBody(commentRun, summary, commentRun.CreatedAt, false),
	} {
		if strings.Count(body, "Tester summary:\n\n"+explicitTesterSummary) != 1 {
			t.Errorf("%s did not preserve the complete tester summary:\n%s", name, body)
		}
	}
	for name, verdict := range map[string]Verdict{"pass": summary} {
		body := draftCommentMD(t, verdict, name)
		if strings.Count(body, "Tester summary:\n\n"+explicitTesterSummary) != 1 {
			t.Errorf("%s comment.md did not preserve the complete tester summary:\n%s", name, body)
		}
	}

	failure := summary
	failure.Overall = CheckFail
	failure.Checks = []Check{{ID: "c1", Name: "batched review", Claim: true, State: CheckFail, Attribution: "pr", Note: "The review was not delivered."}}
	bugs := failingBugComments(failure.Checks)
	if len(bugs) != 1 {
		t.Fatalf("failure did not produce one bug: %+v", bugs)
	}
	bug := bugCommentBody(commentRun, failure, bugs[0], "", "", false, true)
	if strings.Count(bug, "Tester summary:\n\n"+explicitTesterSummary) != 1 {
		t.Fatalf("bug-first comment did not preserve the complete tester summary:\n%s", bug)
	}
	if body := draftCommentMD(t, failure, "fail"); strings.Count(body, "Tester summary:\n\n"+explicitTesterSummary) != 1 {
		t.Fatalf("fail comment.md did not preserve the complete tester summary:\n%s", body)
	}
}

func draftCommentMD(t *testing.T, v Verdict, suffix string) string {
	t.Helper()
	svc := newTestService(t, NewStore(t.TempDir()), nil)
	run := commentRun
	run.ID = "untested_" + suffix
	if err := svc.store.Save(run); err != nil {
		t.Fatal(err)
	}
	docs, err := svc.draftComments(run, v)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) == 0 {
		t.Fatal("draftComments wrote no documents")
	}
	body, err := os.ReadFile(filepath.Join(svc.store.root, run.ID, "comment.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestSkippedRowsComeFromRecordedReasonsNotSummaryProse(t *testing.T) {
	checks := []Check{
		{ID: "c1", Name: "review links", Claim: true, State: CheckSkipped, Note: "The linked issue could not be reached."},
		{ID: "c2", Name: "upgrade dedup", Claim: true, State: CheckSkipped, Note: "An upgrade fixture was unavailable."},
	}
	withUntestedLabel := Verdict{Overall: CheckUntested, Summary: "Untested: CI details and review links.", Checks: checks}
	withoutUntestedLabel := withUntestedLabel
	withoutUntestedLabel.Summary = "CI details and review links were left out of this run."

	wantRows := "Not tested: review links (The linked issue could not be reached; main: not compared)"
	for name, verdict := range map[string]Verdict{"with label": withUntestedLabel, "without label": withoutUntestedLabel} {
		body := summaryCommentBody(commentRun, verdict, commentRun.CreatedAt, false)
		if !strings.Contains(body, wantRows) {
			t.Errorf("%s lost the row from the recorded skip reason:\n%s", name, body)
		}
		if !strings.Contains(body, "upgrade dedup (An upgrade fixture was unavailable; main: not compared)") {
			t.Errorf("%s lost the second recorded skip:\n%s", name, body)
		}
		if got := strings.Count(body, "Not tested:"); got != 1 {
			t.Errorf("%s has %d Not tested labels, want one from the recorded check:\n%s", name, got, body)
		}
		if !strings.Contains(body, "Tester summary:\n\n"+verdict.Summary) {
			t.Errorf("%s did not keep summary prose under Tester summary:\n%s", name, body)
		}
	}
}
