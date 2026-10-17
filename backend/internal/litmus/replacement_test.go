package litmus

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCheckReplacementPersistsAudit(t *testing.T) {
	for _, state := range []CheckState{CheckFail, CheckPartial} {
		t.Run(string(state), func(t *testing.T) {
			svc, _ := newSvc(t)
			run, err := svc.Intake(ctx, "s1", prURL)
			is(t, "")(run, err)
			old := Check{ID: "c1", Name: "menu", Expect: "old expectation", State: state, Purpose: "claim", Claim: true, ClaimSentence: "The row now stops that click.", Note: "observed result", Receipts: []Receipt{{Seq: 1, AfterPath: evidenceFile(t)}}}
			is(t, "")(nil, svc.store.SaveChecks(run.ID, []Check{old}))
			reason := "The row now stops that click."
			added, err := svc.CheckAdd(ctx, "s1", "menu", "the click stops", "head", old.ID, reason, CheckClassification{Purpose: "claim", ClaimSentence: reason})
			is(t, "")(added, err)
			checks, err := svc.store.Checks(run.ID)
			is(t, "")(checks, err)
			if added.ID != "c2" || added.State != CheckPending || added.Replaces != old.ID || added.ReplaceReason != reason || len(checks) != 2 || checks[0].ReplacedBy != "" || checks[0].ReplaceReason != reason || checks[0].State != old.State || checks[0].Note != old.Note || len(checks[0].Receipts) != 1 {
				t.Fatalf("replacement lost plan or audit: %+v", checks)
			}
			md := renderVerdict(run, Verdict{Checks: checks, Overall: CheckUntested}, true, nil, filepath.Join(svc.store.root, run.ID), run.CreatedAt, nil)
			if !strings.Contains(md, "## Replacements\n\n- c2 was meant to replace c1 but did not finish: "+reason) || !strings.Contains(md, "old expectation") {
				t.Fatalf("replacement plan missing or old expect hidden:\n%s", md)
			}
			is(t, "")(svc.CheckStart(ctx, "s1", added.ID, CheckClassification{Purpose: "claim", ClaimSentence: reason}))
			checks, err = svc.store.Checks(run.ID)
			is(t, "")(checks, err)
			checks[1].Receipts = checks[0].Receipts
			is(t, "")(nil, svc.store.SaveChecks(run.ID, checks))
			is(t, "")(svc.CheckEnd(ctx, "s1", added.ID, CheckPass, "new result", false, 0))
			checks, err = svc.store.Checks(run.ID)
			is(t, "")(checks, err)
			if checks[0].ReplacedBy != added.ID {
				t.Fatalf("completed replacement did not retire old check: %+v", checks)
			}
		})
	}
}

func TestCheckReplacementRefusalsLeaveLedgerUntouched(t *testing.T) {
	for _, tc := range []struct {
		name, target, reason, leg, code string
		old                             Check
		noWords                         bool
		purpose                         CheckClassification
	}{
		{name: "missing target", target: "c9", code: "TESTING_CHECK_NOT_FOUND", purpose: CheckClassification{Purpose: "claim", ClaimSentence: "The row now stops that click."}},
		{name: "other leg", old: Check{Leg: "base"}, code: "TESTING_CHECK_REPLACEMENT", purpose: CheckClassification{Purpose: "claim", ClaimSentence: "The row now stops that click."}},
		{name: "non-claim target", old: Check{Purpose: "explore"}, code: "TESTING_CHECK_REPLACEMENT", purpose: CheckClassification{Purpose: "explore"}},
		{name: "already replaced", old: Check{ReplacedBy: "c8"}, code: "TESTING_CHECK_REPLACEMENT"},
		{name: "pending", old: Check{State: CheckPending}, code: "TESTING_CHECK_REPLACEMENT"},
		{name: "running", old: Check{State: CheckRunning}, code: "TESTING_CHECK_REPLACEMENT"},
		{name: "passed", old: Check{State: CheckPass}, code: "TESTING_CHECK_REPLACEMENT"},
		{name: "absent", old: Check{State: CheckAbsent}, code: "TESTING_CHECK_REPLACEMENT"},
		{name: "skipped", old: Check{State: CheckSkipped}, code: "TESTING_CHECK_REPLACEMENT"},
		{name: "reason not PR text", reason: "An invented PR sentence.", code: "TESTING_CHECK_REPLACE_REASON", purpose: CheckClassification{Purpose: "claim", ClaimSentence: "The row now stops that click."}},
		{name: "short fragment", reason: "Other models", code: "TESTING_CHECK_REPLACE_REASON", purpose: CheckClassification{Purpose: "claim", ClaimSentence: "The row now stops that click."}},
		{name: "missing PR text", noWords: true, code: "TESTING_CHECK_REPLACE_REASON", purpose: CheckClassification{Purpose: "claim", ClaimSentence: "The row now stops that click."}},
		{name: "claim to explore", code: "TESTING_CHECK_REPLACEMENT", purpose: CheckClassification{Purpose: "explore"}},
		{name: "claim to risk", code: "TESTING_CHECK_REPLACEMENT", purpose: CheckClassification{Purpose: "risk", Risk: "the diff could break navigation"}},
		{name: "claim source changed", code: "TESTING_CHECK_REPLACEMENT", purpose: CheckClassification{Purpose: "claim", ClaimSentence: "A different PR sentence."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newSvc(t)
			run, err := svc.Intake(ctx, "s1", prURL)
			is(t, "")(run, err)
			old := tc.old
			old.ID, old.Name = "c1", "old check"
			if old.State == "" {
				old.State = CheckFail
			}
			if old.Purpose == "" {
				old.Purpose, old.Claim, old.ClaimSentence = "claim", true, "The row now stops that click."
			}
			is(t, "")(nil, svc.store.SaveChecks(run.ID, []Check{old}))
			if tc.noWords {
				is(t, "")(nil, svc.store.WriteFile(run.ID, "intake-claims.json", []byte("[]")))
			}
			path := filepath.Join(svc.store.root, run.ID, "checks.json")
			before, err := os.ReadFile(path)
			is(t, "")(nil, err)
			target, reason := tc.target, tc.reason
			if target == "" {
				target = old.ID
			}
			if reason == "" {
				reason = "The row now stops that click."
			}
			choice := tc.purpose
			if choice.Purpose == "" {
				choice = CheckClassification{Purpose: "claim", ClaimSentence: old.ClaimSentence}
			}
			is(t, tc.code)(svc.CheckAdd(ctx, "s1", "new check", "the click stops", tc.leg, target, reason, choice))
			after, err := os.ReadFile(path)
			is(t, "")(nil, err)
			if !bytes.Equal(before, after) {
				t.Fatal("refused replacement changed the ledger")
			}
		})
	}
	for _, args := range [][2]string{{"c1", ""}, {"", "The row now stops that click."}} {
		svc, _ := newSvc(t)
		is(t, "TESTING_CHECK_REPLACEMENT")(svc.CheckAdd(ctx, "s1", "menu", "opens", "", args[0], args[1], CheckClassification{Purpose: "explore"}))
	}
}

func TestReplacementRemovesVerdictFailureAndBaseTwin(t *testing.T) {
	shot := evidenceFile(t)
	claim := "The row now stops that click."
	old := Check{ID: "c1", Name: "bad expectation", State: CheckFail, Purpose: "claim", Claim: true, ClaimSentence: claim}
	head := Check{ID: "c2", Name: "actual promise", State: CheckPass, Purpose: "claim", Claim: true, ClaimSentence: claim, Replaces: "c1"}
	base := Check{ID: "c3", Name: head.Name, Leg: "base", State: CheckFail, Purpose: "claim", Claim: true, ClaimSentence: claim, Receipts: []Receipt{{Leg: "base", AfterPath: shot}}}
	checks := []Check{old, head, base}
	if got, _ := derive(checks); got != CheckFail {
		t.Fatalf("active failure verdict = %s", got)
	}
	checks[0].ReplacedBy = head.ID
	if got, _ := derive(checks); got != CheckPass {
		t.Fatalf("retired failure still counted: %s", got)
	}
	if got := count(checks); got.headProblems != 0 {
		t.Fatalf("retired failure still counted in comment: %+v", got)
	}
	if i := baseTwin(checks, head); i != 2 || baseResult(checks, head) != string(CheckFail) {
		t.Fatalf("active base twin was laundered: %d, %q", i, baseResult(checks, head))
	}
	if got, _ := derive(checks); got != CheckPass {
		t.Fatalf("active base twin changed completed replacement verdict: %s", got)
	}
	if err := validateSameBug(&head, checks, old.ID, CheckFail); err == nil {
		t.Fatal("retired check accepted as same-bug target")
	}
	if groups := failingBugComments(checks); len(groups) != 0 {
		t.Fatalf("retired failure still appears in comment: %+v", groups)
	}
}

func TestReplacementWithoutClaimSourceKeepsFailure(t *testing.T) {
	checks := []Check{
		{ID: "old", State: CheckFail, Claim: true, Purpose: "claim", ReplacedBy: "new"},
		{ID: "new", State: CheckPass, Claim: true, Purpose: "claim", Replaces: "old"},
	}
	if got, _ := derive(checks); got != CheckFail {
		t.Fatalf("replacement with no PR sentence hid the failed claim: %s", got)
	}
	md := renderVerdict(commentRun, Verdict{Overall: CheckFail, Checks: checks}, true, nil, t.TempDir(), commentRun.CreatedAt, nil)
	if strings.Contains(md, "new replaces old") || !strings.Contains(md, "new was meant to replace old but did not take effect") {
		t.Fatalf("stale replacement was presented as effective:\n%s", md)
	}
}

func TestPassedReplacementRetiresFailureAtSubmit(t *testing.T) {
	svc, _, runDir, artifactDir := submitSvc(t)
	checks, err := svc.store.Checks("run_s1")
	is(t, "")(checks, err)
	claim := "The row now stops that click."
	checks = append(checks, Check{ID: "retired", Name: "wrong expect", State: CheckFail, Purpose: "claim", Claim: true, ClaimSentence: claim, Note: "[old note](missing.png)"})
	is(t, "")(nil, svc.store.SaveChecks("run_s1", checks))
	added, err := svc.CheckAdd(ctx, "s1", "correct expect", "the click stops", "", "retired", claim, CheckClassification{Purpose: "claim", ClaimSentence: claim})
	is(t, "")(added, err)
	checks, err = svc.store.Checks("run_s1")
	is(t, "")(checks, err)
	checks[len(checks)-1].State = CheckPass
	checks[len(checks)-2].ReplacedBy = added.ID
	checks[len(checks)-1].Receipts = checks[0].Receipts
	is(t, "")(nil, svc.store.SaveChecks("run_s1", checks))
	v, err := svc.Submit(ctx, "s1", "", "")
	is(t, "")(v, err)
	if v.Overall != CheckPass {
		t.Fatalf("retired failure still counts: %s, %v", v.Overall, v.Reasons)
	}
	md, err := os.ReadFile(filepath.Join(runDir, "verdict.md"))
	is(t, "")(md, err)
	if !strings.Contains(string(md), added.ID+" replaces retired: The row now stops that click.") {
		t.Fatalf("submitted verdict omitted replacement:\n%s", md)
	}
	index, err := os.ReadFile(filepath.Join(artifactDir, "index.md"))
	is(t, "")(index, err)
	if strings.Contains(string(index), "wrong expect") {
		t.Fatalf("artifact index still shows the retired failure:\n%s", index)
	}
	if retired, found := checkByID(v.Checks, "retired"); !found || retired.ReplacedBy != added.ID {
		t.Fatal("API verdict lost replacement audit metadata")
	}
}

func TestReplacementCannotBeReclassifiedAwayFromItsClaim(t *testing.T) {
	claim := "The row now stops that click."
	for _, tc := range []struct {
		name   string
		choice CheckClassification
	}{
		{name: "explore", choice: CheckClassification{Purpose: "explore"}},
		{name: "risk", choice: CheckClassification{Purpose: "risk", Risk: "the diff could break navigation"}},
	} {
		for _, operation := range []string{"start", "reclassify"} {
			t.Run(tc.name+"/"+operation, func(t *testing.T) {
				svc, _ := newSvc(t)
				run, err := svc.Intake(ctx, "s1", prURL)
				is(t, "")(run, err)
				old := Check{ID: "c1", Name: "old row", State: CheckFail, Purpose: "claim", Claim: true, ClaimSentence: claim, Receipts: []Receipt{{Seq: 1, AfterPath: evidenceFile(t)}}}
				is(t, "")(nil, svc.store.SaveChecks(run.ID, []Check{old}))
				added, err := svc.CheckAdd(ctx, "s1", "new row", "the click stops", "", old.ID, claim, CheckClassification{Purpose: "claim", ClaimSentence: claim})
				is(t, "")(added, err)
				if operation == "start" {
					is(t, "TESTING_CHECK_STATE")(svc.CheckStart(ctx, "s1", added.ID, tc.choice))
				} else {
					is(t, "TESTING_CHECK_REPLACEMENT")(svc.CheckReclassify(ctx, "s1", added.ID, tc.choice, "the replacement needs different coverage"))
				}
				checks, err := svc.store.Checks(run.ID)
				is(t, "")(checks, err)
				if checks[1].Purpose != "claim" || checks[1].Replaces != old.ID {
					t.Fatalf("rejected reclassification changed replacement: %+v", checks[1])
				}
			})
		}
	}
}

func TestReplacementScreenshotsStayOutOfCommentEvidence(t *testing.T) {
	claim := "The row now stops that click."
	checks := []Check{
		{ID: "c1", Name: "retired check", State: CheckFail, Purpose: "claim", Claim: true, ClaimSentence: claim, ReplacedBy: "c2", Receipts: []Receipt{{Seq: 1, AfterPath: evidenceFile(t)}}},
		{ID: "c2", Name: "current check", State: CheckPass, Purpose: "claim", Claim: true, ClaimSentence: claim, Replaces: "c1", Receipts: []Receipt{{Seq: 2, AfterPath: evidenceFile(t)}}},
	}
	heroes, extras := planShots(checks)
	if len(heroes) != 1 || heroes[0].label != "current check" || heroes[0].pr == nil || heroes[0].pr.rcpt.Seq != 2 || len(extras) != 0 {
		t.Fatalf("retired screenshot still planned for comment: %+v, %+v", heroes, extras)
	}
}

func TestReplacementKeepsExplicitEarlierShotAvailable(t *testing.T) {
	svc, _ := newSvc(t)
	dir := t.TempDir()
	shot := filepath.Join(dir, "earlier.png")
	whitePNG(t, shot, 100, 100)
	claim := "The row now stops that click."
	old := Check{ID: "c1", Name: "retired check", State: CheckFail, Purpose: "claim", Claim: true, ClaimSentence: claim, ReplacedBy: "c2", Receipts: []Receipt{{Seq: 1, Leg: "head", AfterPath: shot, ThemeShots: []ThemeShot{{Theme: "light", Path: shot}}}}}
	head := Check{ID: "c2", Name: "current check", State: CheckFail, Purpose: "claim", Claim: true, ClaimSentence: claim, Replaces: "c1", Shot: 1, Receipts: []Receipt{{Seq: 3, Leg: "head", AfterPath: shot, ThemeShots: []ThemeShot{{Theme: "light", Path: shot}}}}}
	base := Check{ID: "c3", Name: head.Name, State: CheckPass, Leg: "base", Purpose: "claim", Claim: true, ClaimSentence: claim, Receipts: []Receipt{{Seq: 2, Leg: "base", AfterPath: shot, ThemeShots: []ThemeShot{{Theme: "light", Path: shot}}}}}
	svc.media = &fakeMedia{}
	v := Verdict{Overall: CheckFail, Checks: []Check{old, head, base}}
	items, _, err := svc.stageEvidence(ctx, commentRun, v, dir)
	is(t, "")(items, err)
	if !slices.ContainsFunc(items, func(item evItem) bool { return item.kind == kindBug && item.check == head.Name }) {
		t.Fatalf("explicit earlier receipt no longer resolves after retirement: %+v", items)
	}
	docs, err := svc.draftComments(commentRun, v)
	is(t, "")(docs, err)
	if len(docs) != 1 || docs[0].ImageFile == "" || strings.Contains(docs[0].Body, "retired check") || strings.Contains(docs[0].Body, "no recorded common") {
		t.Fatalf("draft lost the explicit receipt or exposed a retired check: %+v", docs)
	}
}

func replacementSubmitChecks(t *testing.T, state CheckState) (*Service, Run, []Check) {
	t.Helper()
	svc, _, _, _ := submitSvc(t)
	run, err := svc.store.Get("run_s1")
	is(t, "")(run, err)
	claim := "The row now stops that click."
	checks, err := svc.store.Checks(run.ID)
	is(t, "")(checks, err)
	checks = append(checks,
		Check{ID: "retired", Name: "old row", Expect: "the old click stops", State: CheckFail, Purpose: "claim", Claim: true, ClaimSentence: claim, Note: "the old row failed", Receipts: []Receipt{{Seq: 9, AfterPath: evidenceFile(t, filepath.Join(svc.store.root, run.ID))}}},
		Check{ID: "replacement", Name: "new row", Expect: "the corrected click stops", State: state, Purpose: "claim", Claim: true, ClaimSentence: claim, Replaces: "retired", ReplaceReason: claim, Note: "the replacement was not reached"},
	)
	is(t, "")(nil, svc.store.SaveChecks(run.ID, checks))
	return svc, run, checks
}

func TestIncompleteReplacementLeavesFailureAcrossClosePaths(t *testing.T) {
	for _, tc := range []struct {
		name, operation string
		state           CheckState
		wantErr         string
	}{
		{name: "skipped submit", operation: "submit", state: CheckSkipped},
		{name: "pending submit", operation: "submit", state: CheckPending, wantErr: "TESTING_CHECKS_INCOMPLETE"},
		{name: "skipped blocked", operation: "blocked", state: CheckSkipped},
		{name: "pending blocked", operation: "blocked", state: CheckPending},
		{name: "skipped deadline", operation: "deadline", state: CheckSkipped},
		{name: "pending deadline", operation: "deadline", state: CheckPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, run, _ := replacementSubmitChecks(t, tc.state)
			var verdict Verdict
			var err error
			switch tc.operation {
			case "submit":
				verdict, err = svc.Submit(ctx, "s1", "", "")
			case "blocked":
				verdict, err = svc.SubmitBlocked(ctx, "s1", "tester stopped")
			case "deadline":
				if !svc.closeAtDeadline(ctx, run, quiet) {
					t.Fatal("deadline policy did not close the run")
				}
				verdict.Checks, err = svc.store.Checks(run.ID)
			}
			is(t, tc.wantErr)(verdict, err)
			if tc.wantErr != "" {
				checks, readErr := svc.store.Checks(run.ID)
				is(t, "")(checks, readErr)
				verdict.Checks = checks
			}
			if tc.operation == "deadline" {
				verdict.Overall, _ = derive(verdict.Checks)
			}
			if err == nil && verdict.Overall != CheckFail {
				t.Fatalf("%s replacement changed verdict: %s, %v", tc.name, verdict.Overall, verdict.Reasons)
			}
			old, found := checkByID(verdict.Checks, "retired")
			if !found || old.ReplacedBy != "" {
				t.Fatalf("%s replacement retired old failure: %+v", tc.name, verdict.Checks)
			}
			if tc.operation == "deadline" {
				replacement, found := checkByID(verdict.Checks, "replacement")
				if !found || replacement.State != CheckSkipped {
					t.Fatalf("deadline did not convert replacement to skipped: %+v", verdict.Checks)
				}
			}
		})
	}
}
