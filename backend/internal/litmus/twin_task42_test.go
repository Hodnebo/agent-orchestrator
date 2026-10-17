package litmus

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCheckAddKeepsSameNameTwinCoverageInEitherOrder(t *testing.T) {
	tests := []struct {
		name        string
		firstLeg    string
		firstClass  CheckClassification
		secondLeg   string
		secondClass CheckClassification
		wantText    string
	}{
		{
			name:        "head before base class mismatch",
			firstLeg:    "head",
			firstClass:  CheckClassification{Purpose: "explore"},
			secondLeg:   "base",
			secondClass: CheckClassification{Purpose: "risk", Risk: "the menu could close"},
			wantText:    "main's twin must test what head check c1 tests (explore)",
		},
		{
			name:        "base before head class mismatch",
			firstLeg:    "base",
			firstClass:  CheckClassification{Purpose: "explore"},
			secondLeg:   "head",
			secondClass: CheckClassification{Purpose: "risk", Risk: "the menu could close"},
			wantText:    "main's twin must test what head check c2 tests (risk)",
		},
		{
			name:        "base before head claim sentence mismatch",
			firstLeg:    "base",
			firstClass:  CheckClassification{Purpose: "claim", ClaimSentence: `The row now stops that click.`},
			secondLeg:   "head",
			secondClass: CheckClassification{Purpose: "claim", ClaimSentence: `Clicking "Other models" in the chat model menu no longer closes the menu, and the "Use agent model" row is gone from the model list.`},
			wantText:    "claim sentence",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := newSvc(t)
			is(t, "")(svc.Intake(ctx, "s1", prURL))
			_, err := svc.CheckAdd(ctx, "s1", "same name", "observable", tt.firstLeg, "", "", tt.firstClass)
			is(t, "")(nil, err)
			_, err = svc.CheckAdd(ctx, "s1", "same name", "observable", tt.secondLeg, "", "", tt.secondClass)
			is(t, "TESTING_CHECK_TWIN_CLASS")(nil, err)
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("twin mismatch = %q, want it to contain %q", err, tt.wantText)
			}
		})
	}
}

func TestTwinRiskChecksMayKeepDifferentRiskStatements(t *testing.T) {
	svc, _ := newSvc(t)
	is(t, "")(svc.Intake(ctx, "s1", prURL))
	head, err := svc.CheckAdd(ctx, "s1", "git push", "push works", "head", "", "", CheckClassification{Purpose: "risk", Risk: "the PR may break credentials"})
	is(t, "")(head, err)
	base, err := svc.CheckAdd(ctx, "s1", "git push", "push works", "base", "", "", CheckClassification{Purpose: "risk", Risk: "the base build may lack credentials"})
	is(t, "")(base, err)
	if head.Risk == base.Risk {
		t.Fatalf("twin-specific risk statements were lost: head=%+v base=%+v", head, base)
	}
}

func TestHeadCheckStillRequiresAnExplicitClassAfterBaseWasAdded(t *testing.T) {
	svc, _ := newSvc(t)
	is(t, "")(svc.Intake(ctx, "s1", prURL))
	is(t, "")(svc.CheckAdd(ctx, "s1", "menu", "opens", "base", "", "", CheckClassification{Purpose: "explore"}))
	is(t, "TESTING_CHECK_PURPOSE_REQUIRED")(svc.CheckAdd(ctx, "s1", "menu", "opens", "head", "", ""))
}

func TestCheckStartRejectsSavedTwinCoverageMismatch(t *testing.T) {
	svc, _ := newSvc(t)
	run, err := svc.Intake(ctx, "s1", prURL)
	is(t, "")(run, err)
	checks := []Check{
		{ID: "c1", Name: "menu", Expect: "opens", State: CheckPending, Purpose: "claim", Claim: true, ClaimSentence: "The row now stops that click."},
		{ID: "c2", Name: "MENU", Expect: "opens", State: CheckPending, Leg: "base", Purpose: "claim", Claim: true, ClaimSentence: `Clicking "Other models" in the chat model menu no longer closes the menu, and the "Use agent model" row is gone from the model list.`},
	}
	is(t, "")(nil, svc.store.SaveChecks(run.ID, checks))
	_, err = svc.CheckStart(context.Background(), "s1", "c1")
	is(t, "TESTING_CHECK_TWIN_CLASS")(nil, err)
	if !strings.Contains(err.Error(), "claim sentence") {
		t.Fatalf("start mismatch = %q, want a claim sentence error", err)
	}
}

func TestCheckStartAcceptsSuppliedClassForLegacyPendingHead(t *testing.T) {
	svc, _ := newSvc(t)
	run, err := svc.Intake(ctx, "s1", prURL)
	is(t, "")(run, err)
	claim := CheckClassification{Purpose: "claim", ClaimSentence: "The row now stops that click."}
	checks := []Check{
		{ID: "c1", Name: "menu", Expect: "opens", State: CheckPending},
		{ID: "c2", Name: "MENU", Expect: "opens", State: CheckPending, Leg: "base", Purpose: claim.Purpose, Claim: true, ClaimSentence: claim.ClaimSentence},
	}
	is(t, "")(nil, svc.store.SaveChecks(run.ID, checks))
	started, err := svc.CheckStart(ctx, "s1", "c1", claim)
	is(t, "")(started, err)
	if started.State != CheckRunning || checkClassificationFor(started) != claim {
		t.Fatalf("legacy head start = %+v", started)
	}
}

func TestCheckReclassifyUpdatesPendingTwinAndAuditsBoth(t *testing.T) {
	svc, _ := newSvc(t)
	run, err := svc.Intake(ctx, "s1", prURL)
	is(t, "")(run, err)
	choice := CheckClassification{Purpose: "explore"}
	head, err := svc.CheckAdd(ctx, "s1", "menu", "opens", "head", "", "", choice)
	is(t, "")(head, err)
	base, err := svc.CheckAdd(ctx, "s1", "menu", "opens", "base", "", "")
	is(t, "")(base, err)
	want := CheckClassification{Purpose: "risk", Risk: "the menu could close"}
	head, err = svc.CheckReclassify(ctx, "s1", head.ID, want, "record the risk before either check runs")
	is(t, "")(head, err)
	checks, err := svc.store.Checks(run.ID)
	is(t, "")(checks, err)
	if len(checks) != 2 {
		t.Fatalf("checks = %+v", checks)
	}
	for _, check := range checks {
		if got := checkClassificationFor(check); got != want {
			t.Fatalf("%s coverage = %+v, want %+v", check.ID, got, want)
		}
		if len(check.Reclassifications) != 1 || check.Reclassifications[0].From != choice || check.Reclassifications[0].To != want {
			t.Fatalf("%s reclassification history = %+v", check.ID, check.Reclassifications)
		}
	}
	if !checks[0].Reclassifications[0].At.Equal(checks[1].Reclassifications[0].At) {
		t.Fatalf("paired audit times differ: %s and %s", checks[0].Reclassifications[0].At, checks[1].Reclassifications[0].At)
	}
}

func TestCheckReclassifyRefusesEndedTwin(t *testing.T) {
	svc, _ := newSvc(t)
	run, err := svc.Intake(ctx, "s1", prURL)
	is(t, "")(run, err)
	choice := CheckClassification{Purpose: "explore"}
	head, err := svc.CheckAdd(ctx, "s1", "menu", "opens", "head", "", "", choice)
	is(t, "")(head, err)
	base, err := svc.CheckAdd(ctx, "s1", "menu", "opens", "base", "", "")
	is(t, "")(base, err)
	is(t, "")(svc.CheckStart(ctx, "s1", base.ID))
	is(t, "")(svc.CheckEnd(ctx, "s1", base.ID, CheckSkipped, "base was unavailable", false, 0))

	_, err = svc.CheckReclassify(ctx, "s1", head.ID, CheckClassification{Purpose: "risk", Risk: "the menu could close"}, "record the risk")
	is(t, "TESTING_CHECK_TWIN_STATE")(nil, err)
	checks, err := svc.store.Checks(run.ID)
	is(t, "")(checks, err)
	if got := checkClassificationFor(checks[0]); got != choice {
		t.Fatalf("head coverage changed despite refusal: %+v", checks[0])
	}
}

func TestCheckReclassifyPreservesAndSeparatesTwinRiskStatements(t *testing.T) {
	svc, _ := newSvc(t)
	run, err := svc.Intake(ctx, "s1", prURL)
	is(t, "")(run, err)
	checks := []Check{
		{ID: "c1", Name: "git push", Expect: "push works", State: CheckPending, Purpose: "explore"},
		{ID: "c2", Name: "git push", Expect: "push works", State: CheckPending, Leg: "base", Purpose: "risk", Risk: "the base build may lack credentials"},
	}
	is(t, "")(nil, svc.store.SaveChecks(run.ID, checks))
	headRisk := CheckClassification{Purpose: "risk", Risk: "the PR may break credentials"}
	is(t, "")(svc.CheckReclassify(ctx, "s1", "c1", headRisk, "record the PR-specific risk"))
	checks, err = svc.store.Checks(run.ID)
	is(t, "")(checks, err)
	if checks[1].Risk != "the base build may lack credentials" {
		t.Fatalf("base-specific risk was replaced: %+v", checks[1])
	}
	baseRisk := CheckClassification{Purpose: "risk", Risk: "base credentials differ from the PR"}
	is(t, "")(svc.CheckReclassify(ctx, "s1", "c2", baseRisk, "record the base-specific risk"))
	checks, err = svc.store.Checks(run.ID)
	is(t, "")(checks, err)
	if checks[0].Risk != headRisk.Risk || checks[1].Risk != baseRisk.Risk {
		t.Fatalf("reclassification did not preserve each twin's risk: %+v", checks)
	}
}

func TestReplayRejectsPendingBaseTwinCoverageMismatch(t *testing.T) {
	svc, _, _, _ := driveSvc(t)
	base, err := svc.attachEngine(ctx, "run_s1", Leg{Name: "base", State: "running", DebugPort: 9401, Engine: &EngineInfo{AppOrigin: "http://app.test"}})
	is(t, "")(base, err)
	run, err := svc.store.Get("run_s1")
	is(t, "")(run, err)
	run.Legs = append(run.Legs, base)
	is(t, "")(nil, svc.store.Save(run))
	is(t, "")(nil, svc.store.SaveChecks(run.ID, []Check{
		{ID: "c1", Name: "menu", Expect: "opens", State: CheckPass, Purpose: "claim", Claim: true, ClaimSentence: "The row now stops that click.", Receipts: []Receipt{{Seq: 1, Leg: "head", Action: "click", Args: []string{"@e7"}, TargetRole: "button", TargetName: "Go"}}},
		{ID: "c2", Name: "MENU", Expect: "opens", State: CheckPending, Leg: "base", Purpose: "claim", Claim: true, ClaimSentence: `Clicking "Other models" in the chat model menu no longer closes the menu, and the "Use agent model" row is gone from the model list.`},
	}))

	_, err = svc.Replay(ctx, "s1", "c1", "base")
	is(t, "TESTING_CHECK_TWIN_CLASS")(nil, err)
	if !strings.Contains(err.Error(), "claim sentence") {
		t.Fatalf("replay mismatch = %q, want a claim sentence error", err)
	}
}

func TestTestingHintsRequireLaunchedBaseAndReplayableHeadActions(t *testing.T) {
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	check := Check{ID: "c1", Name: "menu", State: CheckPass, Purpose: "explore", Receipts: []Receipt{{Action: "snapshot", Leg: "head"}}}
	run := Run{State: RunRunning, Deadline: now.Add(time.Hour)}
	_, hints := testingStatusGuidance(run, []Check{check}, now)
	if slicesContainsHint(hints, "ao testing replay") {
		t.Fatalf("unlaunched base suggested replay: %+v", hints)
	}
	run.Legs = []Leg{{Name: "base", State: "running", Engine: &EngineInfo{}}}
	_, hints = testingStatusGuidance(run, []Check{check}, now)
	if slicesContainsHint(hints, "ao testing replay") {
		t.Fatalf("non-replayable action suggested replay: %+v", hints)
	}
	check.Receipts = []Receipt{{Action: "click", Leg: "head"}}
	_, hints = testingStatusGuidance(run, []Check{check}, now)
	if !slicesContainsHint(hints, "ao testing replay c1 --leg base") {
		t.Fatalf("launched base and replayable action did not suggest replay: %+v", hints)
	}
}

func TestSameBugHintIncludesRequiredResult(t *testing.T) {
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	run := Run{State: RunRunning, Deadline: now.Add(time.Hour)}
	checks := []Check{
		{ID: "c1", Name: "earlier", State: CheckFail, Purpose: "risk", Risk: "menu can close"},
		{ID: "c2", Name: "later", State: CheckPartial, Purpose: "risk", Risk: "menu can close"},
	}
	_, hints := testingStatusGuidance(run, checks, now)
	if !slicesContainsHint(hints, "Re-end with --result partial --same-bug <id>") {
		t.Fatalf("same-bug hint omitted its required result: %+v", hints)
	}
}

func slicesContainsHint(hints []TestingHint, text string) bool {
	for _, hint := range hints {
		if strings.Contains(hint.Text, text) {
			return true
		}
	}
	return false
}
