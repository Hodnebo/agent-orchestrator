package litmus

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPreExistingRequiresMatchingBaseEvidence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		headState CheckState
		baseState CheckState
		receipt   string
		want      bool
	}{
		{"no base", CheckFail, "", "", false},
		{"base passed", CheckFail, CheckPass, "saved", false},
		{"base partial differs", CheckFail, CheckPartial, "saved", false},
		{"head partial differs", CheckPartial, CheckFail, "saved", false},
		{"base absent", CheckFail, CheckAbsent, "saved", false},
		{"base still running", CheckFail, CheckRunning, "saved", false},
		{"fail missing after-shot", CheckFail, CheckFail, "", false},
		{"partial missing after-shot", CheckPartial, CheckPartial, "", false},
		{"deleted after-shot", CheckFail, CheckFail, "missing", false},
		{"wrong leg after-shot", CheckFail, CheckFail, "head", false},
		{"evidenced fail", CheckFail, CheckFail, "saved", true},
		{"evidenced partial", CheckPartial, CheckPartial, "saved", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newSvc(t)
			run, err := svc.Intake(ctx, "s1", prURL)
			is(t, "")(run, err)
			head := Check{ID: "c1", Name: "menu", Purpose: "risk", State: CheckRunning, Receipts: []Receipt{{Seq: 1, AfterPath: evidenceFile(t)}}}
			checks := []Check{head}
			if tc.baseState != "" {
				base := Check{ID: "c2", Name: "MENU", Leg: "base", State: tc.baseState, SameFault: true}
				if tc.receipt != "" {
					r := Receipt{Leg: "base", AfterPath: evidenceFile(t)}
					if tc.receipt == "missing" {
						r.AfterPath = filepath.Join(t.TempDir(), "missing.png")
					} else if tc.receipt == "head" {
						r.Leg = "head"
					}
					base.Receipts = []Receipt{r}
				}
				checks = append(checks, base)
			}
			is(t, "")(nil, svc.store.SaveChecks(run.ID, checks))
			code := "TESTING_CHECK_PRE_EXISTING_EVIDENCE"
			if tc.want {
				code = ""
			}
			is(t, code)(svc.checkEnd(ctx, "s1", "c1", tc.headState, "menu broken", false, 0, CheckAttribution{Kind: "pre-existing"}))
			saved, err := svc.store.Checks(run.ID)
			is(t, "")(nil, err)
			if !tc.want && (saved[0].State != CheckRunning || saved[0].Attribution != "") {
				t.Fatalf("refusal changed the check: %+v", saved[0])
			}
			// Stored labels from earlier runs must obey the same evidence rule.
			checks[0].State, checks[0].Attribution = tc.headState, "pre-existing"
			if got := preExisting(checks, checks[0]); got != tc.want {
				t.Fatalf("preExisting = %t, want %t", got, tc.want)
			}
			if !tc.want && tc.headState == CheckFail {
				if got, _ := derive(checks); got != CheckFail {
					t.Fatalf("unsupported attribution hid failure: %s", got)
				}
			}
		})
	}
	checks := []Check{
		{ID: "c1", Name: "claim", Claim: true, State: CheckPass},
		{ID: "c2", Name: "claim", Leg: "base", State: CheckFail, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}},
		{ID: "c3", Name: "menu", State: CheckFail, Attribution: "unknown"},
		{ID: "c4", Name: "menu", Leg: "base", State: CheckFail, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}},
	}
	if got, _ := derive(checks); got != CheckPartial {
		t.Fatalf("an unproven exploratory base match allowed %s", got)
	}
}

func TestEnvironmentFailureCapsPassAndRejectsBasePass(t *testing.T) {
	for _, claim := range []bool{false, true} {
		checks := []Check{
			{ID: "c1", Name: "claim", Claim: true, State: CheckPass},
			{ID: "c2", Name: "claim", Leg: "base", State: CheckFail, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}},
			{ID: "c9", Name: "fixture", Claim: claim, State: CheckFail, Attribution: "environment", EnvironmentCause: "fixture unavailable"},
		}
		overall, reasons := derive(checks)
		if overall != CheckPartial || !strings.Contains(strings.Join(reasons, " "), "Environment: fixture unavailable.") {
			t.Fatalf("claim=%t: %s %q", claim, overall, reasons)
		}
		v := Verdict{Overall: overall, Checks: checks, Reasons: reasons}
		if md := renderVerdict(commentRun, v, false, nil, t.TempDir(), commentRun.CreatedAt, nil); !strings.Contains(md, "Environment: fixture unavailable.") || strings.Contains(md, "## Litmus couldn't test this") {
			t.Fatalf("environment missing from verdict: %s", md)
		}
		if md := renderComment(commentRun, v, evidence{}, commentRun.CreatedAt); !strings.Contains(md, "Environment: fixture unavailable.") {
			t.Fatalf("environment missing from comment: %s", md)
		}
	}
	svc, _ := newSvc(t)
	run, err := svc.Intake(ctx, "s1", prURL)
	is(t, "")(run, err)
	checks := []Check{
		{ID: "c1", Name: "menu", State: CheckRunning, Receipts: []Receipt{{Seq: 1, AfterPath: evidenceFile(t)}}},
		{ID: "c2", Name: "menu", Leg: "base", State: CheckPass},
	}
	is(t, "")(nil, svc.store.SaveChecks(run.ID, checks))
	_, err = svc.checkEnd(ctx, "s1", "c1", CheckFail, "menu crashes", false, 0, CheckAttribution{Kind: "environment", Cause: "fixture unavailable", Receipt: 1})
	is(t, "TESTING_CHECK_ENVIRONMENT_DIFFERENCE")(nil, err)
	if !strings.Contains(err.Error(), "use unknown or pr") {
		t.Fatalf("missing recovery advice: %v", err)
	}
	saved, err := svc.store.Checks(run.ID)
	is(t, "")(nil, err)
	if saved[0].State != CheckRunning || saved[0].Attribution != "" {
		t.Fatalf("refusal changed the check: %+v", saved[0])
	}
	// The same evidence must win when the head finishes before the base.
	checks[0].Claim, checks[1].State = true, CheckRunning
	checks[1].Receipts = []Receipt{{Seq: 2, Leg: "base", AfterPath: evidenceFile(t)}}
	is(t, "")(nil, svc.store.SaveChecks(run.ID, checks))
	is(t, "")(svc.checkEnd(ctx, "s1", "c1", CheckFail, "menu crashes", false, 0, CheckAttribution{Kind: "environment", Cause: "fixture unavailable", Receipt: 1}))
	is(t, "")(svc.checkEnd(ctx, "s1", "c2", CheckPass, "menu opens", false, 0))
	saved, err = svc.store.Checks(run.ID)
	is(t, "")(nil, err)
	overall, reasons := derive(saved)
	if overall != CheckFail || !strings.Contains(strings.Join(reasons, " "), "environment attribution is not supported") {
		t.Fatalf("late base pass hid the failure: %s %q", overall, reasons)
	}
	if got := failingBugComments(saved); len(got) != 1 || got[0].Check.ID != "c1" {
		t.Fatalf("late base pass hid the bug comment: %+v", got)
	}
	v := Verdict{Overall: overall, Checks: saved, Reasons: reasons}
	md := renderVerdict(commentRun, v, false, nil, t.TempDir(), commentRun.CreatedAt, nil)
	if !strings.Contains(md, "| c1 menu |  |  | fail | pass |") || strings.Contains(md, "c1 menu (head leg): Litmus couldn't test this") {
		t.Fatalf("late base pass mislabeled in verdict:\n%s", md)
	}
	if saved[0].Attribution != "environment" {
		t.Fatal("the original attribution was overwritten")
	}
}

func TestEnvironmentAbsenceCapsPassAndRejectsBasePass(t *testing.T) {
	for _, claim := range []bool{false, true} {
		checks := []Check{
			{ID: "c1", Name: "claim", Claim: true, State: CheckPass},
			{ID: "c2", Name: "claim", Leg: "base", State: CheckFail, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}},
			{ID: "c3", Name: "menu", Claim: claim, State: CheckAbsent, Attribution: "environment", EnvironmentCause: "fixture unavailable"},
		}
		if overall, _ := derive(checks); overall != CheckPartial {
			t.Fatalf("claim=%t: environment absence allowed %s", claim, overall)
		}
	}
	svc, _ := newSvc(t)
	run, err := svc.Intake(ctx, "s1", prURL)
	is(t, "")(run, err)
	checks := []Check{
		{ID: "c1", Name: "menu", State: CheckRunning, Receipts: []Receipt{{Seq: 1, AfterPath: evidenceFile(t)}}},
		{ID: "c2", Name: "menu", Leg: "base", State: CheckPass},
	}
	is(t, "")(nil, svc.store.SaveChecks(run.ID, checks))
	is(t, "TESTING_CHECK_ENVIRONMENT_DIFFERENCE")(svc.checkEnd(ctx, "s1", "c1", CheckAbsent, "menu missing", false, 0, CheckAttribution{Kind: "environment", Cause: "fixture unavailable", Receipt: 1}))
	saved, err := svc.store.Checks(run.ID)
	is(t, "")(nil, err)
	if saved[0].State != CheckRunning || saved[0].Attribution != "" {
		t.Fatalf("refusal changed the check: %+v", saved[0])
	}
	// Stored labels must also yield to a base pass recorded after the head ended.
	checks[0].State, checks[0].Attribution = CheckAbsent, "environment"
	if overall, _ := derive(checks); overall != CheckFail {
		t.Fatalf("environment attribution hid the absence: %s", overall)
	}
	if bugs := failingBugComments(checks); len(bugs) != 1 || bugs[0].Check.ID != "c1" || bugs[0].Check.unreached() {
		t.Fatalf("environment attribution hid the bug comment: %+v", bugs)
	}
}
