package litmus

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// composeFixture loads a real run's checks and run record (testdata/<name>.json) and writes a stand-in file for every
// after-shot a receipt points at.
func composeFixture(t *testing.T, name string) (Run, []Check) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	is(t, "")(nil, err)
	var f struct {
		Run    Run
		Checks []Check
	}
	is(t, "")(nil, json.Unmarshal(raw, &f))
	dir := t.TempDir()
	for i := range f.Checks {
		f.Checks[i].Claim = f.Checks[i].onLeg() == "head"
		for j := range f.Checks[i].Receipts {
			r := &f.Checks[i].Receipts[j]
			if r.AfterPath == "" {
				continue
			}
			r.AfterPath = filepath.Join(dir, filepath.Base(r.AfterPath))
			is(t, "")(nil, os.WriteFile(r.AfterPath, []byte("png"), 0o600))
		}
	}
	return f.Run, f.Checks
}

// composeComment stages the evidence of a fixture and renders the comment the way submit does.
func composeComment(t *testing.T, run Run, v Verdict) (string, []evItem) {
	t.Helper()
	svc, _ := newSvc(t)
	svc.media = &sizedMedia{gif: map[string]int{}}
	dir := filepath.Join(svc.store.root, "run_s1", "evidence")
	is(t, "")(nil, os.MkdirAll(dir, 0o750))
	items, limits, err := svc.stageEvidence(ctx, run, v, dir)
	if err != nil || len(limits) != 0 {
		t.Fatalf("stage: limits %q err %v", limits, err)
	}
	for i := range items {
		items[i].url = evidenceURL(run.Repo, fakeSHA, path.Join(evidencePath(run), items[i].file), false)
	}
	return renderComment(run, v, evidence{state: "published", commit: fakeSHA, items: items}, run.CreatedAt.Add(2*time.Minute)), items
}

func TestComposeRun12(t *testing.T) {
	run, checks := composeFixture(t, "compose-run12")
	// The tester did not repeat c2 on main and said why.
	checks = append(checks, Check{ID: "c4", Name: checks[1].Name, Expect: checks[1].Expect, State: CheckSkipped, Leg: "base",
		Note: "main has no Go back or Go forward buttons, so there was nothing to re-check"})
	v := commentVerdict(checks)
	if v.Overall != CheckPass {
		t.Fatalf("overall %s", v.Overall)
	}
	got, items := composeComment(t, run, v)
	t.Logf("composed comment:\n%s", got)

	for _, want := range []string{
		"**Result: pass.**",
		"Passed: Arrows beside brand, sidebar open (main does not do this); Arrows survive navigation and collapse (not tested on main)",
		"<details><summary>Full check notes</summary>",
		"- Arrows beside brand, sidebar open. Expected: With the sidebar expanded, Go back and Go forward buttons are visible next to the Orchestrator.inc brand.",
		"The PR build: pass. With the sidebar expanded on the PR build, Collapse sidebar, Go back, Go forward (both disabled on Home) and the Orchestrator.inc brand are all present.",
		"Main: fail. On main with the sidebar expanded, only Collapse sidebar and the Orchestrator.inc brand show.",
		"- Arrows survive navigation and collapse. Expected: After opening a project then collapsing and re-expanding the sidebar, both arrows remain visible and Go back becomes enabled.",
		"The PR build: pass. On the PR build, opening a project enabled Go back.",
		"Main: skipped. main has no Go back or Go forward buttons, so there was nothing to re-check",
		"PR build on the right: with the sidebar expanded on the PR build, Collapse sidebar, Go back, Go forward (both disabled on Home) and the Orchestrator.inc brand are all present.",
		"\nPR build: on the PR build, opening a project enabled Go back. Collapsing the sidebar kept both arrows, and re-expanding it showed both arrows plus the Orchestrator.inc brand.\n",
		`Step 2, PR build: click "Sample project 10"`,
		"/runs/6403/20261011-171714/",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q", want)
		}
	}
	for _, bad := range []string{"/Users/", "run_ao", "ao-10", "look at before merging", "could not compare"} {
		if strings.Contains(got, bad) {
			t.Errorf("comment contains %q", bad)
		}
	}
	for _, it := range items {
		if strings.Contains(it.caption, "/Users/") {
			t.Errorf("caption %q", it.caption)
		}
	}
	noSlopSummary(t, run, v)
}

func TestComposeRun13SaysTheSkipReasonOnceAndNotUnderTheChecks(t *testing.T) {
	run, checks := composeFixture(t, "compose-run13")
	v := commentVerdict(checks)
	v.SkipBase = "The PR build's search worked, but the Local/Cloud tabs and visual layout could not be confirmed here, so a comparison against main would not change the verdict"
	got, _ := composeComment(t, run, v)
	t.Logf("composed comment:\n%s", got)
	const reason = "I did not run main: the PR build's search worked, but the Local/Cloud tabs and visual layout could not be confirmed here, so a comparison against main would not change the verdict.\n"
	result := "**Result: partial: something misbehaved.**"
	if strings.Count(got, "I did not run main") != 1 || strings.Contains(got, "could not compare") || !strings.Contains(got, result+"\n\n") || !strings.Contains(got, reason) {
		t.Errorf("the reason is said once, after the result and counts:\n%s", got)
	}
	if strings.Index(got, reason) > strings.Index(got, "Passed:") {
		t.Errorf("run-wide skip reason follows the compact check list:\n%s", got)
	}
	for _, want := range []string{
		" The PR build shows a full-width search field above one row per harness in Settings > Harness; the Local/Cloud tabs did not appear because cloud is off here. I only checked page structure, not the dividers or logo tiles visually.\n",
		"\nPR build. The PR build shows a full-width search field above one row per harness in Settings > Harness; the Local/Cloud tabs did not appear because cloud is off here. I only checked page structure, not the dividers or logo tiles visually.\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q", want)
		}
	}
	for _, bad := range []string{"On the PR build, the PR build", "PR build: the PR build"} {
		if strings.Contains(got, bad) {
			t.Errorf("the build is named twice: %q", bad)
		}
	}
	noSlopSummary(t, run, v)
}

func TestCheckNotesKeepTesterBuildWordsAndIdentifiers(t *testing.T) {
	name := "Menu follows head and base state"
	expect := "On main, c1 keeps the menu open."
	note := "Head: the menu stayed open. Base: it closed. See c1 for the run record."
	check := Check{ID: "c1", Name: name, Expect: expect, Note: note, State: CheckPartial}
	v := plainVerdict(commentRun, Verdict{Checks: []Check{check}})
	if got := plainTester(note, commentRun, v.Checks); got != note {
		t.Fatalf("tester note changed from %q to %q", note, got)
	}
	full := fullCheckNotes(commentRun, v)
	for _, want := range []string{"- " + name, "Expected: " + expect, "The PR build: partial. " + note} {
		if !strings.Contains(full, want) {
			t.Errorf("full check notes lost %q:\n%s", want, full)
		}
	}
}

func TestHeroCaptionOfASingleSideCarriesItsNote(t *testing.T) {
	if got := heroCaption(commentRun, hero{pr: &shot{}, prNote: "Opening a project enabled Go back. Collapsing the sidebar kept both arrows"}); got != "PR build: opening a project enabled Go back. Collapsing the sidebar kept both arrows." {
		t.Errorf("head only: %q", got)
	}
	if got := heroCaption(commentRun, hero{base: &shot{}, baseNote: "There are no arrows."}); got != "Main: there are no arrows." {
		t.Errorf("base only: %q", got)
	}
	if got := heroCaption(commentRun, hero{pr: &shot{}}); got != "The PR build." {
		t.Errorf("no note: %q", got)
	}
}

func TestHeroCaptionKeepsTesterBuildWords(t *testing.T) {
	run := Run{BaseRef: "main"}
	if got := heroCaption(run, hero{pr: &shot{}, base: &shot{}, baseNote: "Main has no arrows.", prNote: "The PR build shows both arrows."}); got != "Main on the left. Main has no arrows. PR build on the right. The PR build shows both arrows." {
		t.Errorf("both sides: %q", got)
	}
	if got := heroCaption(run, hero{pr: &shot{}, base: &shot{}, baseNote: "There are no arrows.", prNote: "The PR build shows both arrows."}); got != "Main on the left: there are no arrows. PR build on the right. The PR build shows both arrows." {
		t.Errorf("one side: %q", got)
	}
}

func TestPlainNameDropsLocalPaths(t *testing.T) {
	for in, want := range map[string]string{
		"Sample project 10 /Users/dev/.ao/data/litmus/repos/x/2db3ffe just now": "Sample project 10",
		"Sample project 10\n/home/dev/x just now":                               "Sample project 10",
		"Open  ~/work/app  now":                                                 "Open",
		"/Users/dev/x Settings":                                                 "Settings",
		"Collapse sidebar":                                                      "Collapse sidebar",
	} {
		if got := plainName(in); got != want {
			t.Errorf("plainName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := stepText("click", []string{"@e21"}, "button", "Sample project 10 /Users/dev/.ao/data/x just now"); got != `click "Sample project 10"` {
		t.Errorf("step text: %q", got)
	}
	if got := caption(actionRec{Seq: 2, Action: "click", Args: []string{"@e21"}, TargetRole: "button", TargetName: "Sample project 10 /Users/dev/.ao/x"}); got != `Step 2: click "Sample project 10"` {
		t.Errorf("srt caption: %q", got)
	}
}

func TestEvidencePathHasNoSessionOrRunID(t *testing.T) {
	run := Run{ID: "run_ao-10", SessionID: "ao-10", PRNumber: 6403, CreatedAt: time.Date(2026, 10, 11, 22, 47, 14, 0, time.FixedZone("IST", 5*3600+1800))}
	if got := evidencePath(run); got != "runs/6403/20261011-171714" {
		t.Errorf("evidence path %q", got)
	}
}

func TestSkippedChecksUseNamesAndKeepFullExpectationsInDetails(t *testing.T) {
	checks := []Check{
		{ID: "c1", Name: "report-wrapper", Expect: "A report delivery inside a Claude paste wrapper is recorded", State: CheckSkipped, Note: "this change has no screen."},
		{ID: "c2", Name: "cli-flag", Expect: "I run the new flag and it prints the count", State: CheckSkipped, Note: "no app to drive."},
	}
	v := commentVerdict(checks)
	got := renderComment(commentRun, v, evidence{}, commentRun.CreatedAt)
	for _, want := range []string{
		"**Result: the PR's stated behavior was not checked.**",
		"Not tested: report wrapper (this change has no screen; main: not compared); cli flag (no app to drive; main: not compared)",
		"- report wrapper. Expected: A report delivery inside a Claude paste wrapper is recorded.",
		"The PR build: skipped. this change has no screen.",
		"- cli flag. Expected: I run the new flag and it prints the count.",
		"The PR build: skipped. no app to drive.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "could not reach any check") {
		t.Errorf("the result line already says it:\n%s", got)
	}
	some := commentVerdict([]Check{checks[0], {ID: "c3", Name: "x", Expect: "x works", State: CheckPartial, Claim: true, Note: "half", Receipts: []Receipt{{Seq: 1}}}})
	if got := renderComment(commentRun, some, evidence{}, commentRun.CreatedAt); strings.Contains(got, "could not test this from the app") {
		t.Errorf("only some checks were skipped:\n%s", got)
	}
}

func TestCheckNamesAndExpectationsStaySeparate(t *testing.T) {
	check := Check{ID: "c1", Name: "settings menu", Expect: "Settings page opens", Note: "The Settings label stayed visible.", State: CheckPass}
	compact := compactChecks(commentRun, Verdict{Checks: []Check{check}})
	full := fullCheckNotes(commentRun, Verdict{Checks: []Check{check}})
	if compact != "Passed: settings menu (main: not compared)" {
		t.Fatalf("compact summary should use Name: %q", compact)
	}
	for _, want := range []string{"- settings menu. Expected: Settings page opens.", "The PR build: pass. The Settings label stayed visible."} {
		if !strings.Contains(full, want) {
			t.Fatalf("full notes lost separate name, expectation, or note:\n%s", full)
		}
	}
}

// TestComposeRun14 is the all-partial rerun of #6266: nothing failed on either build, so the comment must not say
// anything failed, and a partial pair is not described as a failure.
func TestComposeRun14(t *testing.T) {
	run, checks := composeFixture(t, "compose-run14")
	for i := range checks {
		checks[i].SameFault = checks[i].onLeg() == "base"
	}
	v := commentVerdict(checks)
	if v.Overall != CheckPartial || len(v.Reasons) != 4 ||
		!strings.Contains(v.Reasons[0], "fails the same way on main and the PR build") ||
		!strings.Contains(v.Reasons[2], "fails the same way on main and the PR build") {
		t.Fatalf("overall %s reasons %q", v.Overall, v.Reasons)
	}
	got, _ := composeComment(t, run, v)
	t.Logf("composed comment:\n%s", got)
	paras := strings.Split(got, "\n\n")
	if want := "**Result: partial: something misbehaved.**"; paras[1] != want {
		t.Errorf("result line %q", paras[1])
	}
	compact := compactChecks(run, v)
	if !strings.Contains(compact, "- Harness toolbar layout: partial (main does part of this). Search field sits at the top of Settings > Harness above the list.") ||
		!strings.Contains(compact, "- Harness rows and logos: partial (main does part of this). Rows list every harness;") {
		t.Errorf("compact partial row %q", paras[5])
	}
	full := fullCheckNotes(run, v)
	for _, want := range []string{
		"- Harness toolbar layout. Expected: Search field is full width; Local/Cloud tabs appear above it when cloud is on.",
		"The PR build: partial. Search field sits at the top of Settings > Harness above the list.",
		"Main: partial. Search field above the list; no Local/Cloud tabs since cloud is off, same as the PR build.",
	} {
		if !strings.Contains(full, want) {
			t.Errorf("full notes missing %q:\n%s", want, full)
		}
	}
	if strings.Contains(got, "Pre-existing") || strings.Contains(got, "What I could not confirm on the PR build I could not confirm on main either.") {
		t.Errorf("claim partials were described as pre-existing:\n%s", got)
	}
	for _, bad := range []string{"Whatever failed", "so the PR did not cause it", "same thing happens", "do not rely on this run"} {
		if strings.Contains(got, bad) {
			t.Errorf("comment contains %q", bad)
		}
	}
	noSlopSummary(t, run, v)
}

func TestSameFaultClaimReasonAndUnclaimedPreExistingOutcome(t *testing.T) {
	mk := func(id, name, leg, purpose string, s CheckState) Check {
		return Check{
			ID: id, Name: name, Purpose: purpose, Claim: purpose == "claim", State: s, Leg: leg,
			SameFault: leg == "base", Receipts: []Receipt{{Seq: 1, Leg: leg, Action: "click", AfterPath: evidenceFile(t)}},
		}
	}
	const sameFaultReason = "fails the same way on main and the PR build"
	for _, tc := range []struct {
		name       string
		purpose    string
		checks     []Check
		want       CheckState
		wantReason string
	}{
		{
			name:       "claim fail",
			purpose:    "claim",
			checks:     []Check{mk("c1", "a", "", "claim", CheckFail), mk("c2", "a", "base", "claim", CheckFail)},
			want:       CheckPartial,
			wantReason: sameFaultReason,
		},
		{
			name:       "claim partial",
			purpose:    "claim",
			checks:     []Check{mk("c1", "a", "", "claim", CheckPartial), mk("c2", "a", "base", "claim", CheckPartial)},
			want:       CheckPartial,
			wantReason: sameFaultReason,
		},
		{
			name:       "risk without claims",
			purpose:    "risk",
			checks:     []Check{mk("c1", "a", "", "risk", CheckFail), mk("c2", "a", "base", "risk", CheckFail)},
			want:       CheckUntested,
			wantReason: reasonUntested,
		},
		{
			name:       "explore without claims",
			purpose:    "explore",
			checks:     []Check{mk("c1", "a", "", "explore", CheckPartial), mk("c2", "a", "base", "explore", CheckPartial)},
			want:       CheckUntested,
			wantReason: reasonUntested,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			overall, reasons := derive(tc.checks)
			if overall != tc.want {
				t.Fatalf("purpose %s: %s %q, want %s", tc.purpose, overall, reasons, tc.want)
			}
			if tc.wantReason == sameFaultReason {
				if len(reasons) != 1 || !strings.Contains(reasons[0], sameFaultReason) || !strings.Contains(plainReason(reasons[0], "main"), sameFaultReason) {
					t.Fatalf("same-fault claim reason = %q", reasons)
				}
				return
			}
			if len(reasons) != 1 || reasons[0] != tc.wantReason || plainReason(reasons[0], "main") != "I did not check the PR's stated behavior." {
				t.Fatalf("unclaimed pre-existing reason = %q", reasons)
			}
		})
	}
}

func TestVerdictLineForAllPartialAndUnreached(t *testing.T) {
	for _, c := range []struct {
		name   string
		checks []Check
		want   string
	}{
		{"partial pairs only", []Check{{Name: "menu", State: CheckPartial}, {Name: "menu", State: CheckPartial, Leg: "base"}}, "partial: something misbehaved"},
		{"a failing pair", []Check{{Name: "menu", State: CheckFail}, {Name: "menu", State: CheckFail, Leg: "base"}}, "partial: something misbehaved"},
		{"a check never reached", []Check{{Name: "login", State: CheckSkipped, Claim: true}}, "partial: not everything could be tested; skipped claim checks: login on the PR build"},
	} {
		if got := verdictLine(CheckPartial, count(c.checks), "main"); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCompactPassLineUsesTheCheckName(t *testing.T) {
	check := Check{ID: "c1", Name: "history arrows", Expect: "Go back and Go forward buttons are visible beside the Orchestrator.inc brand", State: CheckPass}
	if got := compactChecks(commentRun, Verdict{Checks: []Check{check}}); got != "Passed: history arrows (main: not compared)" {
		t.Fatalf("compact line used the expectation instead of Name: %q", got)
	}
}

// Run ao-17 (#6266): one check passed and failed on main, the other was skipped on the PR build and never ran anywhere.
func TestComposeRun17CountsTheSkippedCheckAsNotDone(t *testing.T) {
	run, checks := composeFixture(t, "compose-run17")
	v := commentVerdict(checks)
	if v.Overall != CheckPartial {
		t.Fatalf("overall %s, want partial", v.Overall)
	}
	got, _ := composeComment(t, run, v)
	if !strings.Contains(got, "**Result: partial: not everything could be tested.**") || !strings.Contains(got, "Not tested: Toolbar scrolls with list (") || strings.Contains(got, "only ran on the PR build") {
		t.Errorf("result line:\n%s", got)
	}
	noSlopSummary(t, run, v)
}

func TestPlainVerdictKeepsComparisonWords(t *testing.T) {
	note := "Not yet compared against main; Local/Cloud tabs were not visible because cloud is off here."
	for _, checks := range [][]Check{
		{{ID: "c1", Name: "search field", Note: note, State: CheckPass}},
		{{ID: "c1", Name: "search field", Note: note, State: CheckPass}, {ID: "b1", Name: "search field", Leg: "base", State: CheckPass}},
	} {
		v := plainVerdict(commentRun, Verdict{Checks: checks})
		if v.Checks[0].Note != note {
			t.Fatalf("plainVerdict rewrote comparison wording: %q", v.Checks[0].Note)
		}
	}
}

func TestComparisonNoteRemainsVerbatimInFullNotes(t *testing.T) {
	note := "Not yet compared against main; Local/Cloud tabs were not visible because cloud is off here."
	check := Check{ID: "c1", Name: "search field", Expect: "the search field appears", Note: note, State: CheckPass}
	full := fullCheckNotes(commentRun, Verdict{Checks: []Check{check}})
	if !strings.Contains(full, "The PR build: pass. "+note) {
		t.Fatalf("full notes rewrote or dropped tester comparison wording:\n%s", full)
	}
}
