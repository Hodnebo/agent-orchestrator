package litmus

import (
	"strings"
	"testing"
	"time"
)

func TestAllSkippedCommentSaysNotTested(t *testing.T) {
	checks := []Check{
		{ID: "c1", Name: "nothing-to-click", Expect: "the feature can be checked", State: CheckSkipped, Note: "No control was available. Litmus did not test it."},
		{ID: "c2", Name: "nothing-to-click", Expect: "the feature can be checked", State: CheckSkipped, Leg: "base", Note: "No control was available. Litmus did not test it."},
	}
	v := commentVerdict(checks)
	v.Summary = "The controls were out of reach for this run."
	body := renderComment(commentRun, v, evidence{}, commentRun.CreatedAt.Add(time.Minute))
	if !strings.Contains(body, "**Result: the PR's stated behavior was not checked.**") {
		t.Fatalf("all-skipped comment does not say it was not tested:\n%s", body)
	}
	if got := compactChecks(commentRun, v); got != "Not tested: nothing to click (No control was available; not tested on main)" {
		t.Fatalf("skipped twin should appear once with main state inline: %q", got)
	}
	if !strings.Contains(body, "Tester summary:\n\nThe controls were out of reach for this run.") || strings.Contains(body, "Untested:") {
		t.Fatalf("an unlabeled summary was not kept separate from the skip list:\n%s", body)
	}
	if !strings.Contains(body, "- nothing to click. Expected: the feature can be checked") || strings.Count(body, "No control was available. Litmus did not test it.") != 2 {
		t.Fatalf("all-skipped comment lost its full note:\n%s", body)
	}
	for _, unwanted := range []string{"something broke; do not merge yet", "the PR build works"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("all-skipped comment says %q:\n%s", unwanted, body)
		}
	}
}

func TestSkippedCommentRowsNameEachLegAndCombineTwins(t *testing.T) {
	checks := []Check{
		{ID: "b1", Name: "wheel-hold", Leg: "base", State: CheckSkipped, Note: "The scroll target is missing. The browser was unavailable."},
		{ID: "c1", Name: "wheel-hold", Expect: "the wheel holds its position", Claim: true, State: CheckSkipped, Note: "The scroll target is missing. The browser was unavailable."},
		{ID: "c2", Name: "login", Expect: "the account page opens", State: CheckSkipped, Note: "Would touch the owner's login."},
		{ID: "b3", Name: "main-only", Leg: "base", State: CheckSkipped, Note: "the control is missing"},
		{ID: "c4", Name: "environment", State: CheckSkipped, Attribution: "environment", EnvironmentCause: "the PR browser could not start"},
		{ID: "b4", Name: "environment", Leg: "base", State: CheckSkipped, Attribution: "environment", EnvironmentCause: "main needs a display"},
	}
	v := commentVerdict(checks)
	for name, body := range map[string]string{
		"comment": renderComment(commentRun, v, evidence{}, commentRun.CreatedAt),
		"summary": summaryCommentBody(commentRun, v, commentRun.CreatedAt, false),
	} {
		for _, want := range []string{
			"Not tested: wheel hold (The scroll target is missing; not tested on main)",
			"login (Would touch the owner's login; main: not compared)",
			"main only (main: the control is missing)",
			"environment (the PR browser could not start; not tested on main, environment: main needs a display)",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q:\n%s", name, want, body)
			}
		}
		if strings.Count(body, "Not tested:") != 1 || strings.Count(stripCommentDetails(body), "wheel hold (") != 1 || strings.Count(body, "- wheel hold.") != 1 {
			t.Errorf("%s repeated or dropped a skipped check:\n%s", name, body)
		}
	}
}

func TestSkippedRiskRowsAnnotateOnlyHead(t *testing.T) {
	for _, tc := range []struct {
		state CheckState
		row   string
	}{
		{CheckPass, "Passed: push (not tested on main)"},
		{CheckSkipped, "Not tested: push (main could not launch; not tested on main)"},
	} {
		t.Run(string(tc.state), func(t *testing.T) {
			head := Check{ID: "c1", Name: "push", Expect: "push succeeds", Purpose: "risk", Risk: "moving HOME loses git credentials", State: tc.state, Note: "main could not launch"}
			base := head
			base.ID, base.Leg, base.State = "b1", "base", CheckSkipped
			checks := []Check{head, base}
			v := commentVerdict(checks)
			if compact := compactChecks(commentRun, v); !strings.Contains(compact, tc.row) {
				t.Fatalf("compact check summary = %q, want %q", compact, tc.row)
			}
			for name, body := range map[string]string{
				"comment": renderComment(commentRun, v, evidence{}, commentRun.CreatedAt),
				"summary": summaryCommentBody(commentRun, v, commentRun.CreatedAt, false),
			} {
				if strings.Count(body, "Risk: "+head.Risk) != 1 || strings.Contains(compactChecks(commentRun, v), head.Risk) {
					t.Errorf("%s must keep full risks in the notes block:\n%s", name, body)
				}
				if i := strings.Index(body, "<details><summary>Full check notes</summary>"); i < 0 || strings.Contains(body[:i], head.Risk) {
					t.Errorf("%s exposed the full risk outside the notes block:\n%s", name, body)
				}
			}
		})
	}
}

func TestCommentDoesNotClaimWorksWithoutPassedClaim(t *testing.T) {
	checks := []Check{
		{ID: "c1", Name: "main claim", Claim: true, State: CheckPartial, Note: "Only one part could be checked."},
		{ID: "c2", Name: "nearby", State: CheckPass, Note: "The unrelated menu opened."},
	}
	v := commentVerdict(checks)
	for name, body := range map[string]string{
		"saved":     renderComment(commentRun, v, evidence{}, commentRun.CreatedAt),
		"published": summaryCommentBody(commentRun, v, commentRun.CreatedAt, false),
	} {
		for _, bad := range []string{"PR build works", "does what it says", "works as far"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s claims %q without a passed claim: %s", name, bad, body)
			}
		}
		if !strings.Contains(body, "- main claim: partial") || !strings.Contains(body, "Passed: nearby") {
			t.Errorf("%s lost claim result: %s", name, body)
		}
	}
}

func TestCommentDoesNotClaimWorksWithCompletedNonclaimPartial(t *testing.T) {
	checks := []Check{
		{ID: "c1", Name: "claim", Claim: true, State: CheckPass},
		{ID: "b1", Name: "claim", Leg: "base", State: CheckAbsent},
		{ID: "c2", Name: "nearby", State: CheckPartial, Note: "The nearby menu opened only partway."},
		{ID: "b2", Name: "nearby", Leg: "base", State: CheckPass},
	}
	v := commentVerdict(checks)
	if v.Overall != CheckPartial {
		t.Fatalf("overall = %s, want partial", v.Overall)
	}
	for _, body := range []string{renderComment(commentRun, v, evidence{}, commentRun.CreatedAt), summaryCommentBody(commentRun, v, commentRun.CreatedAt, false)} {
		if strings.Contains(body, "PR build works") || strings.Contains(body, "does what it says") {
			t.Fatalf("partial result claims the PR works: %s", body)
		}
	}
}

func TestCommentNeverSaysNoDifferenceWhenAnyCheckDiffers(t *testing.T) {
	checks := []Check{
		{ID: "c1", Name: "claim", Claim: true, State: CheckPass},
		{ID: "c2", Name: "claim", Leg: "base", State: CheckPass},
		{ID: "c3", Name: "unclaimed", State: CheckPartial},
		{ID: "c4", Name: "unclaimed", Leg: "base", State: CheckPass},
	}
	v := commentVerdict(checks)
	for _, body := range []string{renderComment(commentRun, v, evidence{}, commentRun.CreatedAt), summaryCommentBody(commentRun, v, commentRun.CreatedAt, false)} {
		if strings.Contains(body, "no difference") || strings.Contains(body, "nothing differed") {
			t.Fatalf("different results called equal: %s", body)
		}
	}
}

func TestCommentListsSkippedReasonsAndKeepsAcceptedNotesWhole(t *testing.T) {
	note := "The menu opened after clicking its button. The list displayed every expected option in order. Selecting the last option updated the label and kept focus on the menu. Closing and reopening the menu retained the same choice. The final observation is still part of this note."
	checks := []Check{
		{ID: "c1", Name: "menu", Expect: "the menu remembers a choice", Claim: true, State: CheckPass, Note: note},
		{ID: "c2", Name: "login", Expect: "the account page opens", State: CheckSkipped, Note: "would touch the owner's login"},
	}
	v := commentVerdict(checks)
	for _, body := range []string{renderComment(commentRun, v, evidence{}, commentRun.CreatedAt), summaryCommentBody(commentRun, v, commentRun.CreatedAt, false)} {
		if !strings.Contains(body, "The final observation is still part of this note.") || strings.Contains(body, "...") {
			t.Fatalf("accepted note was cut: %s", body)
		}
	}
	body := summaryCommentBody(commentRun, v, commentRun.CreatedAt, false)
	if !strings.Contains(body, "Passed: menu") || !strings.Contains(body, "Not tested: login (would touch the owner's login; main: not compared)") || !strings.Contains(body, "Full check notes") || !strings.Contains(body, "The final observation is still part of this note.") {
		t.Fatalf("missing ledger fields: %s", body)
	}
	bug := bugCommentBody(commentRun, v, bugComment{Check: checks[0], Number: 1, Total: 1}, "", "", false, true)
	if !strings.Contains(bug, "Not tested: login (would touch the owner's login; main: not compared)") || !strings.Contains(bug, "Full check notes") || !strings.Contains(bug, "The final observation is still part of this note.") {
		t.Fatalf("bug comment lost skipped checks: %s", bug)
	}
}
