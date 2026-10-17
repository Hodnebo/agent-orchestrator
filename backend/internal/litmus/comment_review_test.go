package litmus

import (
	"strings"
	"testing"
)

func TestBugCommentComparesHeadConsoleErrorsWithMain(t *testing.T) {
	head := Check{
		ID: "head", Name: "menu opens", State: CheckFail,
		Receipts: []Receipt{{ConsoleTail: "Error: menu failed at /home/head/ui/menu.ts:17 request_id=pr-111 port=4100 hash=deadbeef 2026-10-16T18:08:51Z\nError: save failed at /home/head/ui/save.ts:8 request_id=pr-333 port=4300"}},
	}
	base := Check{
		ID: "base", Name: head.Name, Leg: "base", State: CheckPass,
		Receipts: []Receipt{{Leg: "base", ConsoleTail: "Error: menu failed at /workspace/main/views/other.ts:29 request_id=main-222 port=4200 hash=0123abcd 2027-11-17T19:09:52Z"}},
	}
	v := Verdict{Checks: []Check{head, base}}
	body := bugCommentBody(commentRun, v, bugComment{Check: head, Number: 1, Total: 1}, "", "", false)

	if !strings.Contains(body, "Error: save failed") {
		t.Fatalf("head-only console error missing:\n%s", body)
	}
	if strings.Contains(body, "Error: menu failed") || strings.Contains(body, "/home/") || strings.Contains(body, "/workspace/") {
		t.Fatalf("matching console error or local path leaked:\n%s", body)
	}
	if strings.Contains(body, "not compared with main") {
		t.Fatalf("comparison was available but called unverified:\n%s", body)
	}
}

func TestBugCommentNotesNoMainConsoleErrors(t *testing.T) {
	head := Check{ID: "head", Name: "menu opens", State: CheckFail, Receipts: []Receipt{{ConsoleTail: "Error: menu failed"}}}
	checks := []Check{head, {ID: "base", Name: head.Name, Leg: "base", State: CheckPass, Receipts: []Receipt{{Leg: "base"}}}}
	body := bugCommentBody(commentRun, Verdict{Checks: checks}, bugComment{Check: head, Number: 1, Total: 1}, "", "", false)
	if !strings.Contains(body, "No console errors were recorded on main.") || strings.Contains(body, "no console logs were recorded") {
		t.Fatalf("empty base console errors were mislabeled:\n%s", body)
	}
}

func TestBugCommentConsoleComparisonAppliesLineLimitAfterDiff(t *testing.T) {
	t.Run("base sixth line still matches", func(t *testing.T) {
		head := Check{ID: "head", Name: "menu opens", State: CheckFail, Receipts: []Receipt{{ConsoleTail: "Error: sixth"}}}
		baseTail := "Error: one\nError: two\nError: three\nError: four\nError: five\nError: sixth"
		base := Check{ID: "base", Name: head.Name, Leg: "base", State: CheckPass, Receipts: []Receipt{{Leg: "base", ConsoleTail: baseTail}}}
		comparison := compareConsoleErrors(head, []Check{head, base})
		if !comparison.compared || len(comparison.lines) != 0 {
			t.Fatalf("the sixth base error was not compared: %+v", comparison)
		}
	})

	t.Run("head-only sixth line is kept", func(t *testing.T) {
		shared := "Error: one\nError: two\nError: three\nError: four\nError: five"
		head := Check{ID: "head", Name: "menu opens", State: CheckFail, Receipts: []Receipt{{ConsoleTail: shared + "\nError: head only"}}}
		base := Check{ID: "base", Name: head.Name, Leg: "base", State: CheckPass, Receipts: []Receipt{{Leg: "base", ConsoleTail: shared}}}
		comparison := compareConsoleErrors(head, []Check{head, base})
		if len(comparison.lines) != 1 || !strings.Contains(comparison.lines[0], "Error: head only") {
			t.Fatalf("the head-only sixth error was hidden: %+v", comparison)
		}
	})
}

func TestBugCommentConsoleComparisonUsesTheFullLineBeforeDisplayCap(t *testing.T) {
	prefix := "Error: " + strings.Repeat("x", 210)
	head := Check{ID: "head", Name: "menu opens", State: CheckFail, Receipts: []Receipt{{ConsoleTail: prefix + " head-only"}}}
	base := Check{ID: "base", Name: head.Name, Leg: "base", State: CheckPass, Receipts: []Receipt{{Leg: "base", ConsoleTail: prefix + " base-only"}}}
	comparison := compareConsoleErrors(head, []Check{head, base})
	if !comparison.compared || len(comparison.lines) != 1 || len([]rune(comparison.lines[0])) != 200 {
		t.Fatalf("the full line was not compared before the display cap: %+v", comparison)
	}
}

func TestPlainTesterPreservesTesterWords(t *testing.T) {
	run := commentRun
	checks := []Check{{ID: "c1", Name: "menu opens"}}
	for _, text := range []string{
		"works on base.", "works on base.\u2003Next.", "Base works. Head failed.",
		"the file is base.md", "/head/base/c1 is a path", "`head and base` are code",
		"head/base. Next, base passed.",
	} {
		if got := plainTester(text, run, checks); got != text {
			t.Errorf("plainTester changed %q to %q", text, got)
		}
	}
}

func TestUnknownHeadOnlyFailureGetsBugComment(t *testing.T) {
	for _, attribution := range []string{"unknown", "pre-existing"} {
		t.Run(attribution, func(t *testing.T) {
			head := Check{ID: "head", Name: "menu opens", State: CheckFail, Attribution: attribution, Note: "The menu closed."}
			bugs := failingBugComments([]Check{head})
			if len(bugs) != 1 || bugs[0].Base != nil {
				t.Fatalf("unknown head-only failure was dropped: %#v", bugs)
			}
			compact := compactChecks(commentRun, Verdict{Checks: []Check{head}}, bugs)
			if !strings.Contains(compact, "- menu opens: fail (main: not compared). See Bug 1.") {
				t.Fatalf("failing compact row did not link its bug: %q", compact)
			}
			body := bugCommentBody(commentRun, Verdict{Checks: []Check{head}}, bugs[0], "", "", false)
			if !strings.Contains(body, "Bug 1 of 1:") || !strings.Contains(body, "The menu closed.") {
				t.Fatalf("head-only failure did not produce a bug comment:\n%s", body)
			}
		})
	}
}

func TestBugEligibilityIsResolvedPerCheckBeforeGrouping(t *testing.T) {
	pass := func(id, name string) Check {
		return Check{ID: id, Name: name, Leg: "base", State: CheckPass}
	}
	fail := func(id, name, attribution, sameBug string) Check {
		return Check{ID: id, Name: name, Purpose: "risk", State: CheckFail, Attribution: attribution, SameBug: sameBug}
	}
	for _, tc := range []struct {
		name       string
		checks     []Check
		wantBugs   int
		wantRoot   string
		wantLinked int
	}{
		{
			name: "eligible root and link",
			checks: []Check{
				fail("h1", "first", "pr", ""), pass("b1", "first"),
				fail("h2", "second", "pr", "h1"), pass("b2", "second"),
			},
			wantBugs: 1, wantRoot: "h1", wantLinked: 1,
		},
		{
			name: "ineligible root does not hide eligible link",
			checks: []Check{
				fail("h1", "first", "environment", ""), Check{ID: "b1", Name: "first", Leg: "base", State: CheckSkipped},
				fail("h2", "second", "pr", "h1"), pass("b2", "second"),
			},
			wantBugs: 1, wantRoot: "h2", wantLinked: 0,
		},
		{
			name: "ineligible link is omitted from eligible root",
			checks: []Check{
				fail("h1", "first", "pr", ""), pass("b1", "first"),
				fail("h2", "second", "pr", "h1"), {ID: "b2", Name: "second", Leg: "base", State: CheckFail, SameFault: true, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}},
			},
			wantBugs: 1, wantRoot: "h1", wantLinked: 0,
		},
		{
			name: "ineligible root and link produce no bugs",
			checks: []Check{
				fail("h1", "first", "environment", ""), Check{ID: "b1", Name: "first", Leg: "base", State: CheckSkipped},
				fail("h2", "second", "environment", "h1"), Check{ID: "b2", Name: "second", Leg: "base", State: CheckSkipped},
			},
			wantBugs: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bugs := failingBugComments(tc.checks)
			if len(bugs) != tc.wantBugs {
				t.Fatalf("bugs = %#v, want %d", bugs, tc.wantBugs)
			}
			if tc.wantBugs == 0 {
				return
			}
			if bugs[0].Check.ID != tc.wantRoot || len(bugs[0].Linked) != tc.wantLinked {
				t.Fatalf("group = %#v, want root %s with %d linked", bugs[0], tc.wantRoot, tc.wantLinked)
			}
			if ledger := compactChecks(commentRun, Verdict{Checks: tc.checks}, bugs); strings.Contains(ledger, "Pre-existing. See Bug") {
				t.Fatalf("pre-existing observation linked to an unrelated bug: %s", ledger)
			}
		})
	}
}

func TestBugGroupingFollowsEligibleRootThroughIneligibleLink(t *testing.T) {
	checks := []Check{
		{ID: "h1", Name: "first", State: CheckFail, Attribution: "pr"},
		{ID: "b1", Name: "first", Leg: "base", State: CheckPass},
		{ID: "h2", Name: "middle", State: CheckFail, Attribution: "environment", SameBug: "h1"},
		{ID: "b2", Name: "middle", Leg: "base", State: CheckSkipped},
		{ID: "h3", Name: "last", State: CheckFail, Attribution: "pr", SameBug: "h2"},
		{ID: "b3", Name: "last", Leg: "base", State: CheckPass},
	}
	bugs := failingBugComments(checks)
	if len(bugs) != 1 || bugs[0].Check.ID != "h1" || len(bugs[0].Linked) != 1 || bugs[0].Linked[0].ID != "h3" {
		t.Fatalf("eligible root was not found through ineligible link: %#v", bugs)
	}
}

func TestSelfLinkedBugRenderingTerminates(t *testing.T) {
	checks := []Check{
		{ID: "h1", Name: "menu", State: CheckFail, Attribution: "pr", SameBug: "h1"},
		{ID: "b1", Name: "menu", Leg: "base", State: CheckPass},
	}
	bugs := failingBugComments(checks)
	if len(bugs) != 1 || bugs[0].Check.ID != "h1" || len(bugs[0].Linked) != 0 {
		t.Fatalf("self-link was not treated as local: %#v", bugs)
	}
	compact := compactChecks(commentRun, Verdict{Checks: checks}, bugs)
	if !strings.Contains(compact, "See Bug 1.") {
		t.Fatalf("self-link compact row did not point to its bug: %q", compact)
	}
}

func TestFirstBugCommentNamesOriginalProblemLine(t *testing.T) {
	checks := []Check{
		{ID: "claim", Name: "claim", Claim: true, State: CheckPass},
		{ID: "claim-main", Name: "claim", Leg: "base", Claim: true, State: CheckFail, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}},
		{ID: "bug", Name: "menu", State: CheckFail, Attribution: "pr"},
		{ID: "bug-main", Name: "menu", Leg: "base", State: CheckPass},
	}
	v := Verdict{Overall: CheckFail, Checks: checks}
	bugs := failingBugComments(checks)
	if len(bugs) != 1 {
		t.Fatalf("bug groups = %#v", bugs)
	}
	body := bugCommentBody(commentRun, v, bugs[0], "", "", false, true)
	want := "Main reproduces the original problem in 1 claim check that passes on the PR build."
	if !strings.Contains(body, want) {
		t.Fatalf("first bug comment omitted original problem line %q:\n%s", want, body)
	}
}

func TestFailVerdictHasABugForAnExplicitPRFailure(t *testing.T) {
	checks := []Check{
		{ID: "head", Name: "menu", State: CheckFail, Attribution: "pr"},
		{ID: "base", Name: "menu", Leg: "base", State: CheckFail, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}},
	}
	if overall, _ := derive(checks); overall != CheckFail {
		t.Fatalf("overall = %s, want fail", overall)
	}
	bugs := failingBugComments(checks)
	if len(bugs) != 1 || bugs[0].Check.ID != "head" {
		t.Fatalf("explicit PR failure had no bug comment: %#v", bugs)
	}
}
