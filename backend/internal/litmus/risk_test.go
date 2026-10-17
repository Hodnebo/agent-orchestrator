package litmus

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// addSkippedRisks gives submit fixtures planned risks without changing their observed results.
func addSkippedRisks(t *testing.T, svc *Service, runID string) {
	t.Helper()
	checks, err := svc.store.Checks(runID)
	is(t, "")(nil, err)
	checks = append(checks,
		Check{ID: "risk1", Name: "git push", Expect: "push succeeds", Purpose: "risk", Risk: "moving HOME loses git credentials", State: CheckSkipped, Note: "created a test repo; no session available"},
		Check{ID: "risk2", Name: "old transcripts", Expect: "old chat opens", Purpose: "risk", Risk: "moving HOME hides old transcripts", State: CheckSkipped, Note: "created an old transcript; no session available"},
	)
	is(t, "")(nil, svc.store.SaveChecks(runID, checks))
}

func TestRiskClassificationPersistsAndRequiresReclassification(t *testing.T) {
	svc, _ := newSvc(t)
	run, err := svc.Intake(ctx, "s1", prURL)
	is(t, "")(run, err)
	risk := CheckClassification{Purpose: "risk", Risk: "moving HOME loses git credentials"}
	for _, invalidChoice := range []CheckClassification{
		{Purpose: "risk"}, {Purpose: "risk", Risk: " "},
		{Purpose: "risk", Risk: risk.Risk, ClaimSentence: "claim"},
		{Purpose: "claim", Risk: risk.Risk, ClaimSentence: "claim"},
		{Purpose: "explore", Risk: risk.Risk},
	} {
		if _, err := svc.CheckAdd(ctx, "s1", "push", "push succeeds", "", "", "", invalidChoice); err == nil {
			t.Fatalf("accepted invalid classification: %+v", invalidChoice)
		}
	}
	c, err := svc.CheckAdd(ctx, "s1", "push", "push succeeds", "", "", "", CheckClassification{Purpose: "risk", Risk: "  " + risk.Risk + "  "})
	is(t, "")(c, err)
	if c.Purpose != "risk" || c.Risk != risk.Risk || c.Claim || c.ClaimSentence != "" {
		t.Fatalf("risk not stored: %+v", c)
	}
	is(t, "TESTING_CHECK_STATE")(svc.CheckStart(ctx, "s1", c.ID, CheckClassification{Purpose: "risk", Risk: "different risk"}))
	claim := CheckClassification{Purpose: "claim", ClaimSentence: "The row now stops that click."}
	c, err = svc.CheckReclassify(ctx, "s1", c.ID, claim, "tests the headline")
	is(t, "")(c, err)
	if c.Risk != "" || c.Reclassifications[0].From != risk {
		t.Fatalf("risk not cleared or audited: %+v", c)
	}
	is(t, "TESTING_REASON_REQUIRED")(svc.CheckReclassify(ctx, "s1", c.ID, risk, ""))
	c, err = svc.CheckReclassify(ctx, "s1", c.ID, risk, "the diff moves credentials")
	is(t, "")(c, err)
	checks, err := svc.store.Checks(run.ID)
	is(t, "")(checks, err)
	if !reflect.DeepEqual(checks[0], c) || c.Reclassifications[1].To != risk || c.Reclassifications[1].From != claim || c.Claim {
		t.Fatalf("risk reclassification not persisted: %+v", checks)
	}
}

func TestSubmitDoesNotRequireRiskChecks(t *testing.T) {
	svc, _, _, _ := submitSvc(t)
	checks, err := svc.store.Checks("run_s1")
	is(t, "")(nil, err)
	for i := range checks {
		if checks[i].Purpose == "risk" {
			checks[i].Purpose, checks[i].Risk = "explore", ""
		}
	}
	is(t, "")(nil, svc.store.SaveChecks("run_s1", checks))
	if _, err := svc.Submit(ctx, "s1", "", ""); err != nil {
		t.Fatalf("submit without risk checks: %v", err)
	}
}

func TestRiskOutcomesAndReports(t *testing.T) {
	for _, tc := range []struct {
		state CheckState
		want  CheckState
	}{{CheckSkipped, CheckPass}, {CheckFail, CheckFail}, {CheckPartial, CheckPartial}, {CheckPass, CheckPass}} {
		t.Run(string(tc.state), func(t *testing.T) {
			checks := []Check{
				{ID: "c1", Name: "compaction", State: CheckPass, Claim: true, Purpose: "claim", ClaimSentence: "Instructions survive compaction."},
				{ID: "b1", Name: "compaction", State: CheckFail, Leg: "base", Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}},
				{ID: "r1", Name: "git push", Expect: "push succeeds", Purpose: "risk", Risk: "moving HOME loses git credentials", State: tc.state, Note: "created a test repo; session unavailable"},
			}
			overall, reasons := derive(checks)
			if overall != tc.want {
				t.Fatalf("derive = %s, %q; want %s", overall, reasons, tc.want)
			}
			v := Verdict{Overall: overall, Checks: checks, Reasons: reasons}
			dir := t.TempDir()
			md := renderVerdict(commentRun, v, true, nil, filepath.Join(dir, "run"), commentRun.CreatedAt, nil)
			comment := renderComment(commentRun, v, evidence{}, commentRun.CreatedAt)
			compact := compactChecks(commentRun, v)
			if !strings.Contains(md, checks[2].Risk) || !strings.Contains(comment, checks[2].Risk) {
				t.Fatalf("risk missing from full report details:\n%s\n%s", md, comment)
			}
			if strings.Contains(stripCommentDetails(comment), checks[2].Risk) || strings.Contains(compact, checks[2].Risk) {
				t.Fatalf("full risk appeared in the compact check summary:\n%s\n%s", stripCommentDetails(comment), compact)
			}
			if !strings.Contains(compact, "git push") {
				t.Fatalf("risk check missing from compact summary:\n%s", compact)
			}
			if tc.state == CheckSkipped {
				if !strings.Contains(md, "Litmus couldn't test this") || !strings.Contains(stripCommentDetails(comment), "Not tested: git push") || !strings.Contains(compact, "Not tested: git push") {
					t.Fatalf("skipped risk outcome missing:\n%s\n%s\n%s", md, stripCommentDetails(comment), compact)
				}
			} else if tc.state == CheckPass {
				if !strings.Contains(compact, "Passed:") {
					t.Fatalf("passed risk outcome missing:\n%s", compact)
				}
			} else if !strings.Contains(compact, "- git push: "+string(tc.state)) {
				t.Fatalf("risk outcome missing:\n%s", compact)
			}
		})
	}
}

func TestRiskBaseTwinsKeepClassification(t *testing.T) {
	svc, _ := newSvc(t)
	run, err := svc.Intake(ctx, "s1", prURL)
	is(t, "")(run, err)
	head := Check{ID: "c1", Name: "git push", Expect: "push succeeds", Purpose: "risk", Risk: "HOME moves credentials", State: CheckPass}
	head.Receipts = []Receipt{{Action: "press", Leg: "head", Args: []string{"Enter"}}}
	is(t, "")(nil, svc.store.SaveChecks(run.ID, []Check{head}))
	id, err := svc.openReplayCheck(run.ID, head)
	is(t, "")(id, err)
	checks, err := svc.store.Checks(run.ID)
	is(t, "")(checks, err)
	twin := checks[len(checks)-1]
	if twin.ID != id || twin.Purpose != head.Purpose || twin.Risk != head.Risk || twin.Leg != "base" {
		t.Fatalf("replay lost risk: %+v", twin)
	}
	checks = skipOnBase([]Check{
		head,
		{ID: "c2", Name: "claim", State: CheckPass, Claim: true},
		{ID: "c3", Name: "claim", State: CheckPass, Leg: "base", Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}},
	}, "base stopped")
	if len(checks) != 4 {
		t.Fatalf("base skip not created: %+v", checks)
	}
	twin = checks[3]
	if twin.Purpose != head.Purpose || twin.Risk != head.Risk || twin.Leg != "base" || twin.State != CheckSkipped {
		t.Fatalf("skip-base lost risk: %+v", twin)
	}
}
