package litmus

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExplicitClaimChoiceAndAuditedReclassification(t *testing.T) {
	svc, now := newSvc(t)
	svc.media = &fakeMedia{}
	run, err := svc.Intake(ctx, "s1", prURL)
	is(t, "")(run, err)
	explore := CheckClassification{Purpose: "explore"}
	claim := CheckClassification{Purpose: "claim", ClaimSentence: "The row now stops that click."}
	is(t, "TESTING_CHECK_PURPOSE_REQUIRED")(svc.CheckAdd(ctx, "s1", "menu", "opens", "", "", ""))
	is(t, "TESTING_CHECK_CLAIM_SENTENCE")(svc.CheckAdd(ctx, "s1", "menu", "opens", "", "", "", CheckClassification{Purpose: "claim"}))
	c, err := svc.CheckAdd(ctx, "s1", "PR claim headline", "The menu opens.", "", "", "", explore)
	is(t, "")(c, err)
	if c.Claim || c.Purpose != "explore" {
		t.Fatalf("inferred claim from text: %+v", c)
	}
	is(t, "TESTING_CHECK_STATE")(svc.CheckStart(ctx, "s1", c.ID, claim))
	_, err = svc.Submit(ctx, "s1", "", "")
	is(t, "TESTING_NO_CLAIM_CHECKS")(nil, err)
	words := readIntakeClaims(filepath.Join(svc.store.root, run.ID))
	if len(words) < 2 {
		t.Fatalf("intake title/body missing: %q", words)
	}
	for _, source := range words {
		if !strings.Contains(err.Error(), source) {
			t.Fatalf("submit did not echo intake: %v", err)
		}
	}
	is(t, "TESTING_REASON_REQUIRED")(svc.CheckReclassify(ctx, "s1", c.ID, claim, ""))
	c, err = svc.CheckReclassify(ctx, "s1", c.ID, claim, "This checks the stated behaviour.")
	is(t, "")(c, err)
	if !c.Claim || c.ClaimSentence != claim.ClaimSentence || len(c.Reclassifications) != 1 {
		t.Fatalf("classification missing: %+v", c)
	}
	audit := c.Reclassifications[0]
	if audit.Who != "s1" || !audit.At.Equal(*now) || audit.Reason != "This checks the stated behaviour." || audit.From != explore || audit.To != claim {
		t.Fatalf("audit: %+v", audit)
	}
	saved, _ := svc.store.Checks(run.ID)
	if !reflect.DeepEqual(saved[0], c) {
		t.Fatalf("audit not persisted: %+v", saved)
	}
	is(t, "")(svc.CheckStart(ctx, "s1", c.ID))
	is(t, "")(svc.CheckEnd(ctx, "s1", c.ID, CheckSkipped, "needs a fixture", false, 0))
	addSkippedRisks(t, svc, run.ID)
	v, err := svc.Submit(ctx, "s1", "", "")
	is(t, "")(v, err)
	if v.Overall != CheckUntested || !reflect.DeepEqual(v.IntakeClaims, words) {
		t.Fatalf("submit: %+v", v)
	}
}

func TestCheckStartInheritsStoredClassAndRequiresReclassifyForChanges(t *testing.T) {
	svc, _ := newSvc(t)
	is(t, "")(svc.Intake(ctx, "s1", prURL))
	claim := CheckClassification{Purpose: "claim", ClaimSentence: "The row now stops that click."}
	c, err := svc.CheckAdd(ctx, "s1", "menu", "the click stops", "", "", "", claim)
	is(t, "")(c, err)
	c, err = svc.CheckStart(ctx, "s1", c.ID)
	is(t, "")(c, err)
	if c.State != CheckRunning || c.Purpose != "claim" || !c.Claim || c.ClaimSentence != claim.ClaimSentence {
		t.Fatalf("start did not inherit claim: %+v", c)
	}
	_, err = svc.CheckStart(ctx, "s1", c.ID, CheckClassification{Purpose: "explore"})
	is(t, "TESTING_CHECK_STATE")(nil, err)
	if err.Error() != "check c1 is claim; use check reclassify with a reason before changing coverage" {
		t.Fatalf("changed class message = %q", err)
	}
}

func TestBaseCheckInheritsActiveHeadClass(t *testing.T) {
	svc, _ := newSvc(t)
	is(t, "")(svc.Intake(ctx, "s1", prURL))
	claim := CheckClassification{Purpose: "claim", ClaimSentence: "The row now stops that click."}
	head, err := svc.CheckAdd(ctx, "s1", "Menu opens", "the click stops", "head", "", "", claim)
	is(t, "")(head, err)
	_, err = svc.CheckAdd(ctx, "s1", "menu opens", "the click stops", "base", "", "", CheckClassification{Purpose: "explore"})
	is(t, "TESTING_CHECK_TWIN_CLASS")(nil, err)
	if err.Error() != "main's twin must test what head check c1 tests (claim)" {
		t.Fatalf("base class message = %q", err)
	}
	base, err := svc.CheckAdd(ctx, "s1", "menu OPENS", "the click stops", "base", "", "")
	is(t, "")(base, err)
	if base.Purpose != claim.Purpose || !base.Claim || base.ClaimSentence != claim.ClaimSentence {
		t.Fatalf("base twin did not inherit head class: %+v", base)
	}
	_, err = svc.CheckAdd(ctx, "s1", "unpaired base", "the click stops", "base", "", "")
	is(t, "TESTING_CHECK_PURPOSE_REQUIRED")(nil, err)
}

func TestAttribution5848KeepsRealFailureAndSeparatesEnvironment(t *testing.T) {
	svc, _ := newSvc(t)
	run, err := svc.Intake(ctx, "s1", prURL)
	is(t, "")(run, err)
	checks := []Check{
		{ID: "c1", Name: "real regression", Expect: "menu opens", State: CheckRunning, Claim: true, Purpose: "claim", ClaimSentence: "The row now stops that click.", Receipts: []Receipt{{Seq: 1, AfterPath: evidenceFile(t)}}},
		{ID: "c9", Name: "agent version", Expect: "agent starts", State: CheckPending, Purpose: "explore", Receipts: []Receipt{{Seq: 9, Reason: "fixture layout prevented capture"}}},
	}
	is(t, "")(nil, svc.store.SaveChecks(run.ID, checks))
	is(t, "")(svc.CheckEnd(ctx, "s1", "c1", CheckFail, "menu crashes", false, 0, CheckAttribution{Kind: "pr"}))
	is(t, "")(svc.CheckStart(ctx, "s1", "c9", CheckClassification{Purpose: "explore"}))
	is(t, "TESTING_CHECK_ENVIRONMENT_EVIDENCE")(svc.CheckEnd(ctx, "s1", "c9", CheckFail, "install failed", false, 0, CheckAttribution{Kind: "environment", Receipt: 9}))
	is(t, "TESTING_CHECK_ENVIRONMENT_EVIDENCE")(svc.CheckEnd(ctx, "s1", "c9", CheckFail, "install failed", false, 0, CheckAttribution{Kind: "environment", Cause: "fixture layout", Receipt: 1}))
	c, err := svc.CheckEnd(ctx, "s1", "c9", CheckFail, "install failed", false, 0, CheckAttribution{Kind: "environment", Cause: "fixture layout", Receipt: 9})
	is(t, "")(c, err)
	if c.unreached() || c.State != CheckFail || c.Note != "install failed" {
		t.Fatalf("lost observation or reachability: %+v", c)
	}
	checks, _ = svc.store.Checks(run.ID)
	overall, reasons := derive(checks)
	if overall != CheckFail {
		t.Fatalf("real failure lost: %s %q", overall, reasons)
	}
	v := Verdict{Overall: overall, Checks: checks, Reasons: reasons}
	md := renderVerdict(run, v, false, nil, filepath.Join(svc.store.root, run.ID), run.CreatedAt, nil)
	if strings.Contains(md, "## Litmus couldn't test this") || !strings.Contains(md, "c9 agent version (head leg): fail (environment: fixture layout)") || !strings.Contains(md, "Note: install failed") {
		t.Fatalf("environment not separate:\n%s", md)
	}
	comment := renderComment(run, v, evidence{}, run.CreatedAt)
	if strings.Contains(comment, "Bug 2") || !strings.Contains(comment, "Environment: fixture layout.") || !strings.Contains(comment, "install failed") {
		t.Fatalf("environment became bug:\n%s", comment)
	}
	checks[1].Claim = true
	if got, _ := derive(checks[1:]); got != CheckPartial {
		t.Fatalf("environment-only verdict = %s", got)
	}
	base := checks[1]
	base.Leg, base.State = "base", CheckFail
	head := base
	head.Leg, head.State, head.Attribution = "head", CheckPass, "pr"
	if got, _ := derive([]Check{head, base}); got == CheckPass {
		t.Fatal("environment failure on base proved a PR improvement")
	}
	checks[1].Attribution = "pre-existing"
	if got, _ := derive(checks); got != CheckFail {
		t.Fatalf("pre-existing hid real failure = %s", got)
	}
	if got, _ := derive(checks[1:]); got != CheckFail {
		t.Fatal("pre-existing without base evidence hid a failure")
	}
}
