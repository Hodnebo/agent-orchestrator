package litmus

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func truthfulnessChecks(t *testing.T, run string) []Check {
	t.Helper()
	data, err := os.ReadFile("testdata/comment-truthfulness.json")
	if err != nil {
		t.Fatal(err)
	}
	var runs map[string][]Check
	if err := json.Unmarshal(data, &runs); err != nil {
		t.Fatal(err)
	}
	checks, ok := runs[run]
	if !ok {
		t.Fatalf("missing fixture %s", run)
	}
	return checks
}

func TestPlainVerdictPreservesTesterProseAndIdentifiers(t *testing.T) {
	checks := []Check{{
		ID: "c1", Name: "menu opens", Expect: "Head and base both show c1's menu.",
		Note: "Head: it opens. Base: it stays closed. The report keeps litmus-head-run_ao-243-2.",
		Risk: "c1 can hide base data", State: CheckPartial,
	}}
	plain := plainVerdict(commentRun, Verdict{Checks: checks, Summary: "Head compared with base; c1 remains partial."})
	if len(plain.Checks) != 1 {
		t.Fatalf("checks changed: %+v", plain.Checks)
	}
	for field, gotWant := range map[string][2]string{
		"name":    {plain.Checks[0].Name, checks[0].Name},
		"expect":  {plain.Checks[0].Expect, checks[0].Expect},
		"note":    {plain.Checks[0].Note, checks[0].Note},
		"risk":    {plain.Checks[0].Risk, checks[0].Risk},
		"summary": {plain.Summary, "Head compared with base; c1 remains partial."},
	} {
		if gotWant[0] != gotWant[1] {
			t.Errorf("plainVerdict changed %s: got %q, want %q", field, gotWant[0], gotWant[1])
		}
	}
	for _, text := range []string{
		"head, base, and c1. Head works.",
		"Head: it opens. Base: it stays closed. The report keeps litmus-head-run_ao-243-2.",
		"/head/base/c1", `C:\head\base\c1`, "https://example.com?leg=head&build=base#c1",
		"`head base c1`", "``head and `base` and c1``", "```\nhead and base\n```",
	} {
		if got := plainTester(text, commentRun, checks); got != text {
			t.Errorf("plainTester changed tester prose: got %q, want %q", got, text)
		}
	}
}

func TestEnvironmentObservationsRemainVisible(t *testing.T) {
	checks := truthfulnessChecks(t, "run_ao-244")
	v := commentVerdict(checks)
	bugs := failingBugComments(checks)
	if len(bugs) != 1 || bugs[0].Check.ID != "c3" {
		t.Fatalf("environment observations became bugs: %+v", bugs)
	}
	for name, body := range map[string]string{
		"comment": renderComment(commentRun, v, evidence{}, commentRun.CreatedAt),
		"summary": summaryCommentBody(commentRun, plainVerdict(commentRun, v), commentRun.CreatedAt, false),
		"bug":     bugCommentBody(commentRun, plainVerdict(commentRun, v), bugs[0], "", "", false, true),
		"verdict": renderVerdict(commentRun, v, false, nil, t.TempDir(), commentRun.CreatedAt, nil),
	} {
		for _, want := range []string{"Chat returned", "Terminal returned", "CEDAR-6778", "Environment: Initial model is not in the account plan."} {
			if !strings.Contains(body, want) {
				t.Errorf("%s hid %q:\n%s", name, want, body)
			}
		}
		if strings.Contains(body, "couldn't test this: Initial model") {
			t.Errorf("%s called an observed result untested:\n%s", name, body)
		}
	}
	if tally := count(checks); tally.completed != 3 || tally.compared != 2 || len(tally.skippedClaims) != 1 {
		t.Fatalf("observed coverage = %+v", tally)
	}
	if overall, _ := derive(checks[:2]); overall != CheckPartial {
		t.Fatalf("environment partial observations lost: %s", overall)
	}
	if checks[0].unreached() || checks[1].unreached() {
		t.Fatal("environment attribution erased a recorded observation")
	}
}

func TestRequiredChecksSurviveTheProseBudget(t *testing.T) {
	checks := truthfulnessChecks(t, "run_ao-243")
	checks = append(checks, Check{ID: "risk", Name: "restore risk", Expect: "the session restores", Purpose: "risk", Risk: "state can be lost", State: CheckSkipped, Note: "no restore fixture"})
	for i := 0; i < 12; i++ {
		checks = append(checks, Check{ID: "long", Name: "long claim", Claim: true, State: CheckPass, Note: strings.Repeat("The completed claim still needs its observation. ", 10)})
	}
	v := plainVerdict(commentRun, commentVerdict(checks))
	for name, body := range map[string]string{
		"comment": renderComment(commentRun, v, evidence{}, commentRun.CreatedAt),
		"summary": summaryCommentBody(commentRun, v, commentRun.CreatedAt, false),
		"compact": compactChecks(commentRun, v),
		"bug":     bugCommentBody(commentRun, v, bugComment{Check: checks[0], Number: 1, Total: 1}, "", "", false, true),
	} {
		for _, c := range checks {
			if !strings.Contains(body, strings.ReplaceAll(c.Name, "-", " ")) {
				t.Errorf("%s hid check %s:\n%s", name, c.Name, body)
			}
		}
		if strings.Contains(body, "more checks are in the saved verdict") {
			t.Errorf("%s collapsed required checks", name)
		}
	}
	for name, body := range map[string]string{
		"comment": renderComment(commentRun, v, evidence{}, commentRun.CreatedAt),
		"summary": summaryCommentBody(commentRun, v, commentRun.CreatedAt, false),
		"bug":     bugCommentBody(commentRun, v, bugComment{Check: checks[0], Number: 1, Total: 1}, "", "", false, true),
	} {
		for _, want := range []string{"litmus-head-run_ao-243-2", "Risk: state can be lost", "Expected: the session restores", "no restore fixture"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lost full check detail %q", name, want)
			}
		}
	}
	compact := compactChecks(commentRun, v)
	if strings.Contains(compact, "state can be lost") {
		t.Fatal("compact summary repeated full risk detail")
	}
	if !strings.Contains(compact, "restore risk (no restore fixture; main: not compared)") {
		t.Fatalf("compact summary lost the untested reason: %s", compact)
	}
}

func TestEnvironmentObservationsDoNotAssignPRCause(t *testing.T) {
	for _, leg := range []string{"head", "base"} {
		t.Run(leg, func(t *testing.T) {
			head := Check{ID: "c1", Name: "answer", Claim: true, State: CheckPartial, Note: "The answer was correct after switching models."}
			base := Check{ID: "b1", Name: head.Name, Leg: "base", State: CheckAbsent, Note: "The fixture did not expose the feature."}
			if leg == "head" {
				head.Attribution, head.EnvironmentCause = "environment", "the initial model was unavailable"
			} else {
				base.Attribution, base.EnvironmentCause = "environment", "the fixture was unavailable"
			}
			v := commentVerdict([]Check{head, base})
			body := checkPara(commentRun, v, head, 300, 0)
			cause := "Environment: the initial model was unavailable."
			if leg == "base" {
				cause = "not on main, environment: the fixture was unavailable."
			}
			if !strings.Contains(body, "answer was correct after switching models.") || !strings.Contains(body, cause) {
				t.Fatalf("lost observed result or cause: %s", body)
			}
			for _, unsupported := range []string{"the PR's own problem", "This is new in the PR"} {
				if strings.Contains(body, unsupported) {
					t.Errorf("environment attribution proved a product cause: %s", body)
				}
			}
		})
	}
}

func TestBugManifestNumbersOnlyRenderedBugs(t *testing.T) {
	svc := newTestService(t, NewStore(t.TempDir()), nil)
	run := commentRun
	run.ID = "truthful_numbers"
	run.Comments = []PostedComment{{Name: "old fixed bug", Number: 1, Fixed: true}, {Name: "current bug", Number: 2, ID: 77}}
	if err := svc.store.Save(run); err != nil {
		t.Fatal(err)
	}
	checks := []Check{{ID: "c1", Name: "current bug", State: CheckFail, Attribution: "pr", Note: "The menu closed."}}
	docs, err := svc.draftComments(run, commentVerdict(checks))
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].Number != 1 || !strings.HasPrefix(docs[0].Body, "**Result: something broke; do not merge yet.**\n\nBug 1 of 1:") {
		t.Fatalf("numbered drafts = %+v", docs)
	}
	data, err := os.ReadFile(filepath.Join(svc.store.root, run.ID, "comments", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []commentManifestEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Number != 1 {
		t.Fatalf("manifest = %+v", entries)
	}
	if old, ok := postedCommentFor(svc.previousComments(run), "current bug"); !ok || old.ID != 77 {
		t.Fatalf("renumbering lost comment identity: %+v", old)
	}
	// An older saved draft keeps hand edits, but its generated ordinal and
	// local evidence name must agree with the current rendered bug set.
	docs[0].Number = 2
	docs[0].Body = "Bug 2 of 2: hand-edited observation.\n\n![The base build on the left and the PR build on the right](bug-2.png)"
	if err := os.WriteFile(filepath.Join(svc.store.root, run.ID, "comments", "1.md"), []byte(docs[0].Body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := svc.writeCommentManifest(run, docs); err != nil {
		t.Fatal(err)
	}
	prepared, err := svc.commentDrafts(run, commentVerdict(checks), evidence{bugImages: map[string]string{"current bug": "https://example.com/bug.png"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared) != 1 || prepared[0].Number != 1 || !strings.HasPrefix(prepared[0].Body, "Bug 1 of 1: hand-edited observation.") || !strings.Contains(prepared[0].Body, "https://example.com/bug.png") || strings.Contains(prepared[0].Body, "bug-2.png") {
		t.Fatalf("saved draft retained stale ordinals or lost hand edits: %+v", prepared)
	}
}

func TestStagedBugImageRegeneratesTheThemePair(t *testing.T) {
	svc := newTestService(t, NewStore(t.TempDir()), nil)
	svc.media = &sizedMedia{}
	run := commentRun
	run.ID = "theme_pair"
	if err := svc.store.Save(run); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(svc.store.root, run.ID)
	head, base := filepath.Join(dir, "head.png"), filepath.Join(dir, "base.png")
	whitePNG(t, head, 10, 10)
	whitePNG(t, base, 10, 10)
	headCheck := Check{ID: "c1", Name: "menu", State: CheckFail, Attribution: "pr", Receipts: []Receipt{{AfterPath: head, ThemeShots: []ThemeShot{{Theme: "light", Path: head}}}}}
	baseCheck := Check{ID: "b1", Name: "menu", State: CheckPass, Leg: "base", Receipts: []Receipt{{Leg: "base", AfterPath: base, ThemeShots: []ThemeShot{{Theme: "light", Path: base}}}}}
	writeFile(t, filepath.Join(dir, "comments", "bug-1.png"), "stale mismatched comparison")
	stage := filepath.Join(dir, "evidence")
	if err := os.Mkdir(stage, 0o750); err != nil {
		t.Fatal(err)
	}
	items, _, err := svc.stageEvidence(context.Background(), run, Verdict{Checks: []Check{headCheck, baseCheck}}, stage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openEvidenceImage(filepath.Join(stage, "bug-1.png")); err != nil {
		t.Fatalf("staged the stale draft instead of regenerating: %v", err)
	}
	for _, item := range items {
		if item.kind == kindBug {
			if !strings.Contains(item.caption, "light theme") {
				t.Fatalf("staged caption lost theme: %q", item.caption)
			}
			return
		}
	}
	t.Fatal("staged comparison missing")
}
