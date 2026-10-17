package litmus

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestVerdictHeadlineUsesCheckEffects(t *testing.T) {
	for _, tc := range []struct {
		name, purpose, attribution string
		sameFault, evidence        bool
		want                       string
	}{
		{"same-fault risk", "risk", "", true, true, "partial: not everything could be tested"},
		{"same-fault exploration", "explore", "", true, true, "partial: not everything could be tested"},
		{"attributed pre-existing risk", "risk", "pre-existing", false, true, "partial: not everything could be tested"},
		{"same-fault claim", "claim", "", true, true, "partial: something misbehaved"},
		{"unmarked risk", "risk", "", false, true, "partial: something misbehaved"},
		{"risk without base evidence", "risk", "", true, false, "partial: something misbehaved"},
		{"environment", "risk", "environment", false, true, "partial: not everything could be tested"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := []Check{
				{ID: "c1", Name: "claim fix", State: CheckPass, Claim: true},
				{ID: "b1", Name: "claim fix", State: CheckAbsent, Leg: "base"},
				{ID: "c5", Name: "existing fault", State: CheckPartial, Purpose: tc.purpose, Claim: tc.purpose == "claim", Attribution: tc.attribution},
				{ID: "b5", Name: "existing fault", State: CheckPartial, Leg: "base", SameFault: tc.sameFault},
				{ID: "c2", Name: "cleanup", State: CheckSkipped, Claim: true, ClaimSentence: "Cleanup preserves facts.", Note: "no cleanup facts"},
			}
			if tc.evidence {
				checks[3].Receipts = []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}
			}
			v := commentVerdict(checks)
			want := tc.want + "; skipped claim checks: cleanup on the PR build"
			if v.Overall != CheckPartial || verdictLine(v.Overall, count(checks), "main") != want {
				t.Fatalf("verdict = %s, headline = %q, want %q", v.Overall, verdictLine(v.Overall, count(checks), "main"), want)
			}
			md := renderVerdict(commentRun, v, true, nil, t.TempDir(), commentRun.CreatedAt, nil)
			if !strings.HasPrefix(md, "# Verdict: "+want+"\n") {
				t.Fatalf("report headline = %q, want %q", strings.SplitN(md, "\n", 2)[0], want)
			}
		})
	}
}

func TestSkippedClaimSupersession(t *testing.T) {
	const sentence = "Cleanup preserves facts."
	for _, tc := range []struct {
		name   string
		later  Check
		before bool
		want   CheckState
		wantBy string
	}{
		{name: "later pass", later: Check{ID: "c12", State: CheckPass, Claim: true, ClaimSentence: sentence}, want: CheckPass, wantBy: "c12"},
		{name: "later fail", later: Check{ID: "c12", State: CheckFail, Claim: true, ClaimSentence: sentence}, want: CheckFail, wantBy: "c12"},
		{name: "later partial", later: Check{ID: "c12", State: CheckPartial, Claim: true, ClaimSentence: sentence}, want: CheckPartial, wantBy: "c12"},
		{name: "different sentence", later: Check{ID: "c12", State: CheckPass, Claim: true, ClaimSentence: "Cleanup preserves all facts."}, want: CheckPartial},
		{name: "different whitespace", later: Check{ID: "c12", State: CheckPass, Claim: true, ClaimSentence: sentence + " "}, want: CheckPartial},
		{name: "different leg", later: Check{ID: "c12", State: CheckPass, Claim: true, ClaimSentence: sentence, Leg: "base"}, want: CheckPartial},
		{name: "later skip", later: Check{ID: "c12", State: CheckSkipped, Claim: true, ClaimSentence: sentence}, want: CheckPartial},
		{name: "later absent", later: Check{ID: "c12", State: CheckAbsent, Claim: true, ClaimSentence: sentence}, want: CheckFail},
		{name: "later running", later: Check{ID: "c12", State: CheckRunning, Claim: true, ClaimSentence: sentence}, want: CheckPartial},
		{name: "replaced pass", later: Check{ID: "c12", State: CheckPass, Claim: true, ClaimSentence: sentence, ReplacedBy: "c13"}, want: CheckPass, wantBy: "c13"},
		{name: "non-claim pass", later: Check{ID: "c12", State: CheckPass, ClaimSentence: sentence}, want: CheckPartial},
		{name: "earlier pass", later: Check{ID: "c12", State: CheckPass, Claim: true, ClaimSentence: sentence}, before: true, want: CheckPartial},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skipped := Check{ID: "c2", Name: "cleanup", Claim: true, ClaimSentence: sentence, State: CheckSkipped, Note: "no cleanup facts"}
			checks := []Check{
				{ID: "c1", Name: "proven fix", Claim: true, State: CheckPass},
				{ID: "b1", Name: "proven fix", Leg: "base", State: CheckAbsent},
			}
			if tc.before {
				checks = append(checks, tc.later, skipped)
			} else {
				checks = append(checks, skipped, tc.later)
			}
			if tc.later.ReplacedBy != "" {
				for i := range checks {
					if checks[i].ID == "c12" {
						checks[i].Purpose = "claim"
					}
				}
				checks = append(checks, Check{ID: "c13", Name: "replacement", Purpose: "claim", Claim: true, ClaimSentence: sentence, State: CheckPass, Replaces: "c12"})
			}
			v := commentVerdict(checks)
			if v.Overall != tc.want {
				t.Fatalf("derive = %s, reasons %q; want %s", v.Overall, v.Reasons, tc.want)
			}
			counted := slices.ContainsFunc(count(checks).skippedClaims, func(c Check) bool { return c.ID == skipped.ID })
			if counted != (tc.wantBy == "") {
				t.Fatalf("c2 counted as a skipped claim = %t, superseded by %q", counted, tc.wantBy)
			}
			if tc.wantBy != "" {
				for _, reason := range v.Reasons {
					if strings.HasPrefix(reason, "c2 ") {
						t.Fatalf("superseded skip still contributes a verdict reason: %q", reason)
					}
				}
			}
			md := renderVerdict(commentRun, v, true, nil, t.TempDir(), commentRun.CreatedAt, nil)
			if strings.Contains(md, "c2 superseded by "+tc.wantBy) != (tc.wantBy != "") {
				t.Fatalf("supersession missing or invented:\n%s", md)
			}
			if tc.wantBy != "" && strings.Contains(md, "## Litmus couldn't test this") {
				t.Fatalf("superseded skip still listed as untested:\n%s", md)
			}
			if tc.wantBy != "" && strings.Contains(compactChecks(commentRun, v), "Not tested: cleanup") {
				t.Fatal("superseded skip still appears in the comment's untested list")
			}
		})
	}
}

func TestSkippedClaimsWithoutStoredSentencesStayUnresolved(t *testing.T) {
	checks := []Check{
		{ID: "c2", Name: "cleanup", Claim: true, State: CheckSkipped},
		{ID: "c12", Name: "cleanup", Claim: true, State: CheckPass},
		{ID: "base", Name: "cleanup", Leg: "base", State: CheckAbsent},
	}
	if got, _ := derive(checks); got != CheckPartial || len(count(checks).skippedClaims) != 1 {
		t.Fatalf("empty claim sentences superseded a skip: %s, %+v", got, count(checks))
	}
}

func TestEarlierCheckEndedLaterDoesNotSupersedeSkip(t *testing.T) {
	svc, _ := newSvc(t)
	run, err := svc.Intake(ctx, "s1", prURL)
	is(t, "")(run, err)
	checks := []Check{
		{ID: "c12", Name: "earlier check", Claim: true, ClaimSentence: "Cleanup preserves facts.", State: CheckRunning, Receipts: []Receipt{{AfterPath: evidenceFile(t)}}},
		{ID: "c2", Name: "later skip", Claim: true, ClaimSentence: "Cleanup preserves facts.", State: CheckRunning},
		{ID: "base", Name: "earlier check", Leg: "base", State: CheckAbsent},
	}
	is(t, "")(nil, svc.store.SaveChecks(run.ID, checks))
	is(t, "")(svc.CheckEnd(ctx, "s1", "c2", CheckSkipped, "no cleanup facts", false, 0))
	is(t, "")(svc.CheckEnd(ctx, "s1", "c12", CheckPass, "facts preserved", false, 0))
	checks, err = svc.store.Checks(run.ID)
	is(t, "")(nil, err)
	if got, _ := derive(checks); got != CheckPartial || len(count(checks).skippedClaims) != 1 {
		t.Fatalf("completion order overrode stored check order: %s, %+v", got, count(checks))
	}
}

func TestSupersededSkipDoesNotBlockAfterControlLoss(t *testing.T) {
	run := commentRun
	run.Legs = []Leg{{Name: "head", Control: &LegControl{State: "lost", Cause: "native_dialog", Since: 1}}}
	checks := []Check{
		{ID: "c2", Claim: true, ClaimSentence: "Cleanup preserves facts.", State: CheckSkipped},
		{ID: "c12", Claim: true, ClaimSentence: "Cleanup preserves facts.", State: CheckPass},
	}
	if got := uncontrollableHeadReason(run, checks); got != "" {
		t.Fatalf("superseded claim still blocks the run: %q", got)
	}
}

func TestSupersessionNamesFirstLaterResultOnEachLeg(t *testing.T) {
	for _, leg := range []string{"head", "base"} {
		t.Run(leg, func(t *testing.T) {
			checks := []Check{
				{ID: "c2", Leg: leg, Claim: true, ClaimSentence: "Cleanup preserves facts.", State: CheckSkipped},
				{ID: "c12", Leg: leg, Claim: true, ClaimSentence: "Cleanup preserves facts.", State: CheckPass},
				{ID: "c13", Leg: leg, Claim: true, ClaimSentence: "Cleanup preserves facts.", State: CheckFail},
			}
			md := renderVerdict(commentRun, commentVerdict(checks), true, nil, t.TempDir(), commentRun.CreatedAt, nil)
			if !strings.Contains(md, "c2 superseded by c12") || len(count(checks).skippedClaims) != 0 {
				t.Fatalf("first later result did not supersede %s skip:\n%s", leg, md)
			}
			if overall, _ := derive(checks); leg == "head" && overall != CheckFail {
				t.Fatalf("later failure was laundered by the earlier pass: %s", overall)
			}
		})
	}
}

func TestVerdictEffectGoldens(t *testing.T) {
	for _, name := range []string{"skipped-claim", "superseded-claim"} {
		t.Run(name, func(t *testing.T) {
			runDir := t.TempDir()
			after := filepath.Join(runDir, "base-risk.png")
			if err := os.WriteFile(after, []byte("png fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			checks := []Check{
				{ID: "c1", Name: "claim fix", Expect: "the fix works", State: CheckPass, Claim: true, Purpose: "claim", ClaimSentence: "The fix works."},
				{ID: "b1", Name: "claim fix", State: CheckAbsent, Leg: "base"},
				{ID: "c2", Name: "cleanup", Expect: "cleanup preserves facts", State: CheckSkipped, Claim: true, Purpose: "claim", ClaimSentence: "Cleanup preserves facts.", Note: "no cleanup facts"},
				{ID: "c5", Name: "existing fault", Expect: "existing folders open", State: CheckPartial, Purpose: "risk", Risk: "folder setup can break", Note: "existing folders remain partly broken"},
				{ID: "b5", Name: "existing fault", State: CheckPartial, Leg: "base", SameFault: true, Receipts: []Receipt{{Seq: 1, Leg: "base", AfterPath: after}}},
			}
			if name == "superseded-claim" {
				checks = append(checks, Check{ID: "c12", Name: "cleanup with facts", Expect: "cleanup preserves facts", State: CheckPass, Claim: true, Purpose: "claim", ClaimSentence: "Cleanup preserves facts.", Note: "created cleanup facts; cleanup preserved them"})
			}
			v := commentVerdict(checks)
			body := renderVerdict(commentRun, v, true, nil, runDir, commentRun.CreatedAt, nil)
			goldenPath := filepath.Join("testdata", "verdict-"+name+".golden.md")
			if os.Getenv("UPDATE_VERDICT_GOLDENS") == "1" {
				if err := os.WriteFile(goldenPath, []byte(body), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatal(err)
			}
			if body != string(want) {
				t.Fatalf("generated report differs from %s:\n%s", goldenPath, goldenDiff(string(want), body))
			}
		})
	}
}
