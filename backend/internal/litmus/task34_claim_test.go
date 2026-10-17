package litmus

import (
	"strings"
	"testing"
)

func TestClaimAndRiskMainStatusWording(t *testing.T) {
	for _, tc := range []struct {
		name, purpose string
		state         CheckState
		want          string
	}{
		{"claim pass", "claim", CheckPass, "main already does this"},
		{"claim fail", "claim", CheckFail, "main does not do this"},
		{"claim partial", "claim", CheckPartial, "main does part of this"},
		{"claim absent", "claim", CheckAbsent, "not on main"},
		{"risk pass", "risk", CheckPass, "works on main"},
		{"risk fail", "risk", CheckFail, "fails on main"},
		{"risk partial", "risk", CheckPartial, "partly broken on main"},
		{"explore partial", "explore", CheckPartial, "partly broken on main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mainStatus("main", tc.state, tc.purpose)
			if got != tc.want {
				t.Fatalf("mainStatus = %q, want %q", got, tc.want)
			}
			if tc.purpose == "claim" && strings.Contains(got, "broken") {
				t.Fatalf("claim status used broken wording: %q", got)
			}
		})
	}
}

func TestCheckParaUsesRelativeWordingForClaims(t *testing.T) {
	for _, tc := range []struct {
		name, purpose, want, avoid string
		claim                      bool
	}{
		{"claim", "claim", "main does not do this", "partly broken on main", true},
		{"risk", "risk", "On main, it did not work.", "main does not do this", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head := Check{ID: "head", Name: "menu opens", Expect: "the menu stays open", State: CheckPass, Claim: tc.claim, Purpose: tc.purpose}
			base := Check{ID: "base", Name: "menu opens", Expect: head.Expect, State: CheckFail, Leg: "base", Claim: tc.claim, Purpose: tc.purpose}
			if tc.claim {
				base.Note = "The menu closed on main."
			}
			got := checkPara(commentRun, Verdict{Checks: []Check{head, base}}, head, 300, 0)
			if !strings.Contains(got, tc.want) || strings.Contains(got, tc.avoid) {
				t.Fatalf("checkPara = %q, want %q and no %q", got, tc.want, tc.avoid)
			}
			if tc.claim && !strings.Contains(got, "the menu closed on main.") {
				t.Fatalf("claim check lost base note: %q", got)
			}
		})
	}
}

func TestFailedOnBaseCountsEvidencedPartialOnlyForClaims(t *testing.T) {
	base := func(t *testing.T) Check {
		t.Helper()
		return Check{
			ID: "base", Name: "menu opens", Leg: "base", State: CheckPartial,
			Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}},
		}
	}
	for _, tc := range []struct {
		name, purpose string
		claim, want   bool
	}{
		{"claim purpose", "claim", true, true},
		{"claim flag", "", true, true},
		{"risk", "risk", false, false},
		{"explore", "explore", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head := Check{ID: "head", Name: "menu opens", State: CheckPass, Claim: tc.claim, Purpose: tc.purpose}
			if got := failedOnBase([]Check{head, base(t)}, head); got != tc.want {
				t.Fatalf("failedOnBase = %t, want %t", got, tc.want)
			}
		})
	}

	head := Check{ID: "head", Name: "menu opens", State: CheckPass, Claim: true, Purpose: "claim"}
	baseWithoutShot := Check{ID: "base", Name: head.Name, Leg: "base", State: CheckPartial}
	if failedOnBase([]Check{head, baseWithoutShot}, head) {
		t.Fatal("an evidenced difference was claimed without a base after-shot")
	}
}

func TestCommentsDropReplacedChecks(t *testing.T) {
	claim := "The row now stops that click."
	old := Check{ID: "old", Name: "old wording", State: CheckFail, Claim: true, Purpose: "claim", ClaimSentence: claim, ReplacedBy: "new"}
	newCheck := Check{ID: "new", Name: "new wording", State: CheckPass, Claim: true, Purpose: "claim", ClaimSentence: claim, Replaces: "old"}
	base := Check{ID: "base", Name: newCheck.Name, Leg: "base", State: CheckFail, Claim: true, Purpose: "claim", Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}}
	checks := []Check{old, newCheck, base}
	if got := count(checks); got.completed != 1 {
		t.Fatalf("count included replaced check: %+v", got)
	}
	for name, body := range map[string]string{
		"compact":    compactChecks(commentRun, Verdict{Checks: checks}),
		"full notes": fullCheckNotes(commentRun, Verdict{Checks: checks}),
		"comment":    renderComment(commentRun, Verdict{Overall: CheckPass, Checks: checks}, evidence{}, commentRun.CreatedAt),
	} {
		if strings.Contains(body, "old wording") {
			t.Errorf("%s included replaced check:\n%s", name, body)
		}
	}
}

func TestClaimMainTwinUsesOneComparisonIncludingEnvironment(t *testing.T) {
	for _, state := range []CheckState{CheckPass, CheckFail, CheckPartial, CheckAbsent, CheckSkipped} {
		for _, purpose := range []string{"claim", ""} {
			t.Run(string(state)+"/"+purpose, func(t *testing.T) {
				head := Check{ID: "head", Name: "menu opens", Expect: "the menu stays open", State: CheckPass, Claim: true, Purpose: purpose}
				base := Check{ID: "base", Name: head.Name, Expect: head.Expect, State: state, Leg: "base", Attribution: "environment", EnvironmentCause: "the agent lacks credentials", Note: "The agent could not authenticate."}
				checks := []Check{head, base}
				want := mainStatus("main", state, "claim") + ", environment: the agent lacks credentials"
				if got := mainComparison(commentRun, checks, head); got != want {
					t.Fatalf("mainComparison=%q, want %q", got, want)
				}
				for name, text := range map[string]string{
					"compact":   compactChecks(commentRun, Verdict{Checks: checks}),
					"paragraph": checkPara(commentRun, Verdict{Checks: checks}, head, 300, 0),
				} {
					if !strings.Contains(text, want) || strings.Contains(text, "broken") {
						t.Errorf("%s=%q, want %q without broken", name, text, want)
					}
				}
			})
		}
	}
}
