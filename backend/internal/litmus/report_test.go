package litmus

import (
	"strings"
	"testing"
)

func reportBodies(t *testing.T, checks []Check) map[string]string {
	t.Helper()
	v := commentVerdict(checks)
	return map[string]string{
		"comment": renderComment(commentRun, v, evidence{}, commentRun.CreatedAt),
		"summary": summaryCommentBody(commentRun, v, commentRun.CreatedAt, false),
		"verdict": renderVerdict(commentRun, v, true, nil, t.TempDir(), commentRun.CreatedAt, nil),
	}
}

// A legacy pass like run_ao-230 c1 must still show the tester's timer observation.
// The skill decides whether new observations are bugs or design opinions.
func TestReportsPromoteEveryWorthALookNote(t *testing.T) {
	checks := []Check{
		{ID: "c1", Name: "reset-duration", Expect: "the reset duration is readable", Claim: true, State: CheckPass,
			Note: "The reset duration appeared. Worth a look: The timer showed 1m 60s left. Worth a look: The rows use mixed wording.",
			Shot: 14, Receipts: []Receipt{{Seq: 14}, {Seq: 15}}},
		{ID: "b1", Name: "reset-duration", Leg: "base", State: CheckAbsent},
	}
	if v := commentVerdict(checks); v.Overall != CheckPass {
		t.Fatalf("opinion changed the recorded verdict: %s", v.Overall)
	}
	for name, body := range reportBodies(t, checks) {
		visible := body
		if name != "verdict" {
			visible = stripCommentDetails(body)
		}
		for _, want := range []string{
			"pass, 2 things worth a look",
			"- reset duration, the PR build, receipt 14: The timer showed 1m 60s left.",
			"- reset duration, the PR build, receipt 14: The rows use mixed wording.",
		} {
			if !strings.Contains(visible, want) {
				t.Errorf("%s missing %q:\n%s", name, want, body)
			}
		}
		if strings.Count(visible, "The timer showed 1m 60s left.") != 1 {
			t.Errorf("%s buried or repeated the opinion:\n%s", name, body)
		}
		section := strings.Index(body, "Worth a look")
		end := strings.Index(visible, "Passed:")
		if name == "summary" {
			end = strings.Index(visible, "Passed:")
		} else if name == "verdict" {
			end = strings.Index(body, "## Checks")
		}
		if section < 0 || end < section {
			t.Errorf("%s did not put opinions before the check results:\n%s", name, body)
		}
		if name != "verdict" && !strings.Contains(body, "Worth a look: The timer showed 1m 60s left.") {
			t.Errorf("%s lost the original note from full check details:\n%s", name, body)
		}
	}
}

func TestReportsExplainPartialWithObservedHeadProblems(t *testing.T) {
	for _, state := range []CheckState{CheckPartial, CheckFail} {
		t.Run(string(state), func(t *testing.T) {
			checks := []Check{
				{ID: "c1", Name: "composer-position", Expect: "the composer is kept in place", Claim: true, State: state, Note: "The composer jumped 300px.", Receipts: []Receipt{{Seq: 1}}},
				{ID: "b1", Name: "composer-position", Leg: "base", State: state, SameFault: true, Receipts: []Receipt{{Seq: 2, Leg: "base", AfterPath: evidenceFile(t)}}},
				{ID: "c2", Name: "login", Claim: true, State: CheckSkipped, Note: "would touch the owner's login"},
			}
			if v := commentVerdict(checks); v.Overall != CheckPartial {
				t.Fatalf("overall = %s, want partial", v.Overall)
			}
			for name, body := range reportBodies(t, checks) {
				want := "partial: something misbehaved; skipped claim checks: login on the PR build"
				if name != "verdict" {
					want = "partial: something misbehaved"
				}
				if !strings.Contains(body, want) || strings.Contains(body, "nothing failed") {
					t.Errorf("%s did not name the observed problem and skipped claim:\n%s", name, body)
				}
				if name != "verdict" && !strings.Contains(stripCommentDetails(body), "Not tested: login (would touch the owner's login; main: not compared)") {
					t.Errorf("%s did not keep the skipped claim visible:\n%s", name, body)
				}
			}
		})
	}
}

func TestReportsCountCompletedComparedAndDifferentChecksSeparately(t *testing.T) {
	checks := []Check{
		{ID: "c1", Name: "wheel-hold", Claim: true, State: CheckPass},
		{ID: "b1", Name: "wheel-hold", Leg: "base", State: CheckAbsent},
		{ID: "c2", Name: "composer-position", Claim: true, State: CheckPass},
		{ID: "b2", Name: "composer-position", Leg: "base", State: CheckPass},
		{ID: "c3", Name: "nearby-menu", State: CheckPartial},
		{ID: "b3", Name: "nearby-menu", Leg: "base", State: CheckPartial},
		{ID: "b4", Name: "base-only-context", Leg: "base", State: CheckPass},
		{ID: "c4", Name: "base-observation", State: CheckPass, Receipts: []Receipt{{Seq: 4, Leg: "base"}}},
	}
	for name, body := range reportBodies(t, checks) {
		if !strings.Contains(body, "4 completed checks on the PR build; 4 checks compared on main; 1 check with different results.") {
			t.Errorf("%s conflated completed, compared, or different results:\n%s", name, body)
		}
		if strings.Contains(body, "3 checks showed differences") || strings.Contains(body, "checks I finished") {
			t.Errorf("%s has a misleading count:\n%s", name, body)
		}
	}
}

func TestReportsExplainPartialWhenBothBuildsPass(t *testing.T) {
	// Like run_ao-237, the claim checks pass on both builds without showing a change.
	checks := []Check{
		{ID: "c1", Name: "wheel-hold", Claim: true, State: CheckPass, Note: "The wheel held its position."},
		{ID: "b1", Name: "wheel-hold", Leg: "base", State: CheckPass},
		{ID: "c2", Name: "composer-position", Claim: true, State: CheckPass, Note: "The composer stayed in place."},
		{ID: "b2", Name: "composer-position", Leg: "base", State: CheckPass},
	}
	v := commentVerdict(checks)
	if v.Overall != CheckPartial || len(v.Reasons) != 1 || v.Reasons[0] != reasonNoDifference {
		t.Fatalf("verdict = %#v, want partial with no difference", v)
	}
	for name, body := range reportBodies(t, checks) {
		if !strings.Contains(body, "partial: no difference from main shown") || strings.Contains(body, "not everything could be tested") || strings.Contains(body, "something misbehaved") {
			t.Errorf("%s gave the wrong partial reason:\n%s", name, body)
		}
	}
	body := renderComment(commentRun, v, evidence{}, commentRun.CreatedAt)
	if !strings.Contains(body, plainReason(reasonNoDifference, "main")) {
		t.Fatalf("comment lost the no-difference reason:\n%s", body)
	}
	for i := range checks {
		if checks[i].onLeg() == "head" {
			checks[i].Note = strings.Repeat(checks[i].Note+" ", 200)
		}
	}
	body = renderComment(commentRun, commentVerdict(checks), evidence{}, commentRun.CreatedAt)
	visible := stripCommentDetails(body)
	if !strings.Contains(visible, "**Result: partial: no difference from main shown.**") || !strings.Contains(visible, "Nothing passed on the PR build that failed on main") {
		t.Fatalf("fallback lost the headline or short reason:\n%s", body)
	}
	if !strings.HasSuffix(body, commentMarker(commentRun.Repo, commentRun.PRNumber)) || strings.Count(body, "The wheel held its position.") != 200 || strings.Count(body, "The composer stayed in place.") != 200 {
		t.Fatalf("fallback lost required observations or the marker: %d characters", len(body))
	}
}

// Like run_ao-237, the base-only skip is a coverage gap, not a head failure.
func TestReportsListSkippedClaimsAndOmitEmptyUntestedSection(t *testing.T) {
	checks := []Check{
		{ID: "c1", Name: "wheel-hold", Claim: true, State: CheckPass},
		{ID: "b1", Name: "wheel-hold", Leg: "base", State: CheckPass},
		{ID: "b2", Name: "base-only-wheel", Expect: "the wheel holds its position", Claim: true, Leg: "base", State: CheckSkipped, Note: "the scroll target is missing from this build"},
	}
	for name, body := range reportBodies(t, checks) {
		want := "partial: not everything could be tested; skipped claim checks: base only wheel on main"
		if name != "verdict" {
			want = "partial: not everything could be tested"
		}
		if !strings.Contains(body, want) {
			t.Errorf("%s missing %q:\n%s", name, want, body)
		}
		if name == "verdict" && !strings.Contains(body, "Litmus couldn't test this") {
			t.Errorf("%s lost the full skipped-claim explanation:\n%s", name, body)
		}
		if name == "verdict" {
			if !strings.Contains(body, "scroll target is missing from this build") {
				t.Errorf("%s lost the skipped-claim reason:\n%s", name, body)
			}
		} else if !strings.Contains(stripCommentDetails(body), "Not tested: base only wheel (main: the scroll target is missing from this build)") {
			t.Errorf("%s lost the compact skipped-claim row:\n%s", name, body)
		}
		if strings.Contains(body, "something misbehaved") {
			t.Errorf("%s called a skipped base-only check a head problem:\n%s", name, body)
		}
	}
	checks = checks[:2]
	for name, body := range reportBodies(t, checks) {
		if name == "verdict" && strings.Contains(body, "Litmus couldn't test this") || name != "verdict" && strings.Contains(body, "Not tested:") || strings.Contains(body, "Worth a look") {
			t.Errorf("%s rendered an empty section:\n%s", name, body)
		}
	}
	checks = append(checks, Check{ID: "b2", Name: "base-environment", Claim: true, Leg: "base", State: CheckFail,
		Attribution: "environment", EnvironmentCause: "the scroll target is missing from this build", AttributionReceipt: 2})
	for name, body := range reportBodies(t, checks) {
		if strings.Contains(body, "Litmus couldn't test this") || !strings.Contains(body, "Environment: the scroll target is missing from this build.") || strings.Contains(body, "something misbehaved") {
			t.Errorf("%s lost the base environment observation cause or treated it as a head problem:\n%s", name, body)
		}
	}
}
