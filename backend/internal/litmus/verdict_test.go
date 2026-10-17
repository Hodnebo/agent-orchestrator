package litmus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/sessionartifacts"
)

// fakeMedia stands in for ffmpeg: it records the arguments and creates the output file (the last argument).
type fakeMedia struct {
	calls [][]string
	err   error
}

func (f *fakeMedia) Run(_ context.Context, args ...string) error {
	f.calls = append(f.calls, args)
	if f.err != nil {
		return f.err
	}
	return os.WriteFile(args[len(args)-1], []byte("mp4"), 0o600)
}

func TestChecksPlanRules(t *testing.T) {
	svc, _ := newSvc(t)
	is(t, "")(svc.Intake(ctx, "s1", prURL))
	run, _ := svc.store.Get("run_s1")
	run.State, run.Repo, run.Legs = RunPrepared, "example/risk-fixture", []Leg{{Name: "head", State: "prepared"}, {Name: "base", State: "prepared"}}
	is(t, "")(nil, svc.store.Save(run))
	launchCode := func(want string) {
		t.Helper()
		for _, leg := range []string{"head", "base"} {
			_, err := svc.Launch(ctx, "s1", leg)
			is(t, want)(nil, err)
		}
	}
	launchCode("TESTING_RECIPE_MISSING")
	c, err := svc.CheckAdd(ctx, "s1", "menu opens", "list shown", "base", "", "", CheckClassification{Purpose: "risk", Risk: "base risk"})
	is(t, "")(c, err)
	if c.Leg != "base" || c.Name != "menu opens" {
		t.Fatalf("base check: %+v", c)
	}
	is(t, "TESTING_LEG_INVALID")(svc.CheckAdd(ctx, "s1", "menu opens", "list shown", "main", "", "", CheckClassification{Purpose: "explore"}))
	is(t, "TESTING_CHECK_TWIN_CLASS")(svc.CheckAdd(ctx, "s1", "menu opens", "list shown", "", "", "", CheckClassification{Purpose: "explore"}))
	is(t, "")(svc.CheckAdd(ctx, "s1", "menu opens", "list shown", "", "", "", CheckClassification{Purpose: "risk", Risk: "head risk"}))
	launchCode("TESTING_RECIPE_MISSING")
	for i := 0; i < 2; i++ {
		is(t, "")(svc.CheckAdd(ctx, "s1", fmt.Sprint("risk ", i), "still works", "", "", "", CheckClassification{Purpose: "risk", Risk: "HOME moves user data"}))
		if i == 0 {
			launchCode("TESTING_RECIPE_MISSING")
		} else {
			// A missing recipe stops the test before any process starts.
			launchCode("TESTING_RECIPE_MISSING")
		}
	}
	// A successful first launch persists RunRunning even if its leg later crashes.
	is(t, "")(nil, svc.store.SaveChecks(run.ID, nil))
	_, err = svc.update(run.ID, func(r *Run) { r.State, r.Legs[0].State = RunRunning, "failed" })
	is(t, "")(nil, err)
	launchCode("TESTING_RECIPE_MISSING")
	for i := 2; i <= 20; i++ { // there is no cap: the run's budget is the only limit
		is(t, "")(svc.CheckAdd(ctx, "s1", fmt.Sprint("check ", i), "x", "", "", "", CheckClassification{Purpose: "explore"}))
	}
}

func TestVerdictListsSeedGapsAsLimits(t *testing.T) {
	prGap := "The PR build started without its sample content (seed step 2 failed: no project)."
	mainGap := "The main build started without its sample content (seed step 2 failed: no project)."
	intakeGap := "I could not read the linked issue #1 (not found)."
	for _, intake := range []bool{false, true} {
		run := commentRun
		run.Gaps = []string{prGap, mainGap}
		if intake {
			run.Gaps = append(run.Gaps, intakeGap)
		}
		md := renderVerdict(run, commentVerdict(commentChecks(t)), false, nil, t.TempDir(), run.CreatedAt.Add(time.Minute), nil)
		before, limits, ok := strings.Cut(md, "## Limits\n")
		if !ok || strings.Contains(before, prGap) || strings.Contains(before, mainGap) || !strings.Contains(limits, "- "+prGap) || !strings.Contains(limits, "- "+mainGap) {
			t.Fatalf("seed gaps are not separate limits:\n%s", md)
		}
		if strings.Contains(before, "What intake could not get") != intake || strings.Contains(before, intakeGap) != intake {
			t.Fatalf("intake heading or gap is wrong:\n%s", md)
		}
	}
}

func TestDeriveOverall(t *testing.T) {
	head := func(id string, st CheckState) Check { return Check{ID: id, Name: id, State: st, Claim: true} }
	base := func(id string, st CheckState) Check {
		return Check{ID: "b" + id, Name: strings.ToUpper(id), State: st, Leg: "base", Receipts: []Receipt{{Leg: "base", Action: "click", AfterPath: evidenceFile(t)}}}
	} // same name as head(id), any case, with a screenshot receipt like every ended check
	same := func(c Check) Check { c.SameFault = true; return c }
	for i, c := range []struct {
		checks []Check
		want   CheckState
	}{
		{[]Check{head("x", CheckPass), base("x", CheckFail)}, CheckPass},                                                  // the base failing is the bug being fixed
		{[]Check{head("x", CheckPass), base("x", CheckFail), head("y", CheckPass)}, CheckPass},                            // y was never compared; the difference on x carries it
		{[]Check{head("x", CheckPass)}, CheckPartial},                                                                     // no base result
		{[]Check{head("x", CheckPass), base("x", CheckPass)}, CheckPartial},                                               // base is the same: no difference shown
		{[]Check{head("x", CheckPass), base("x", CheckPartial)}, CheckPass},                                               // an evidenced partial claim twin shows a difference
		{[]Check{{ID: "c1", Name: "x", State: CheckPass, Claim: true, Receipts: []Receipt{{Leg: "base"}}}}, CheckPartial}, // base observed, no outcome
		{[]Check{head("x", CheckPartial), base("x", CheckFail)}, CheckPartial},                                            // a partial head never passes
		{[]Check{head("x", CheckPass), base("x", CheckFail), head("y", CheckPartial)}, CheckPartial},
		{[]Check{head("x", CheckFail), base("x", CheckPass), head("y", CheckPartial)}, CheckFail},    // any head fail wins
		{[]Check{head("x", CheckPass), base("x", CheckFail), head("y", CheckSkipped)}, CheckPartial}, // an unreached head check caps the result
		{[]Check{head("x", CheckSkipped), head("y", CheckPass), base("y", CheckFail)}, CheckPartial},
		{[]Check{head("x", CheckFail), head("y", CheckSkipped)}, CheckFail},                                                   // a skip does not override a failure
		{[]Check{head("x", CheckPass), base("x", CheckFail), base("y", CheckSkipped)}, CheckPass},                             // only head skips cap the result
		{[]Check{head("x", CheckPass), head("y", CheckSkipped)}, CheckPartial},                                                // nothing differs
		{[]Check{head("x", CheckSkipped)}, CheckUntested},                                                                     // nothing was reached
		{[]Check{base("x", CheckFail)}, CheckUntested},                                                                        // a base-only check never decides
		{[]Check{head("x", CheckPass), base("x", CheckSkipped)}, CheckPartial},                                                // the base could not be reached either
		{[]Check{head("x", CheckPartial), base("x", CheckPartial), head("y", CheckPass), base("y", CheckFail)}, CheckPartial}, // matching partial labels stay partial
		{[]Check{head("x", CheckFail), base("x", CheckFail), head("y", CheckPass), base("y", CheckFail)}, CheckPartial},       // an unproven matching failure stays in the finding but does not block
		{[]Check{head("x", CheckFail), {ID: "bx", Name: "X", State: CheckFail, Leg: "base"}}, CheckFail},                      // main's failure has no evidence and cannot make the head failure partial
		{[]Check{head("x", CheckFail), base("x", CheckPass), head("y", CheckPass), base("y", CheckFail)}, CheckFail},          // a base pass makes it the PR's regression
		{[]Check{head("x", CheckPartial), base("x", CheckSkipped)}, CheckPartial},                                             // the base was not reached: not pre-existing
		{[]Check{head("x", CheckFail), base("x", CheckFail)}, CheckPartial},                                                   // an unproven matching failure does not block
		{[]Check{head("x", CheckPass), {ID: "bx", Name: "x", State: CheckFail, Leg: "base"}}, CheckPartial},                   // nor one with no receipt at all
		{[]Check{head("x", CheckFail), base("x", CheckSkipped), head("y", CheckPass), base("y", CheckFail)}, CheckFail},       // a head fail beside a base skip is not pre-existing
		{[]Check{head("x", CheckFail), base("x", CheckPartial), head("y", CheckPass), base("y", CheckFail)}, CheckFail},       // nor beside a base partial
		{[]Check{head("x", CheckPass), base("x", CheckAbsent)}, CheckPass},                                                    // a feature main lacks is a difference in the PR's favour
		{[]Check{head("x", CheckFail), base("x", CheckAbsent)}, CheckFail},                                                    // a head fail beside an absent main is the PR's own
		{[]Check{head("x", CheckFail), base("x", CheckAbsent), head("y", CheckPass), base("y", CheckFail)}, CheckFail},        // it is never pre-existing, whatever else passes
		{[]Check{head("x", CheckPartial), base("x", CheckAbsent)}, CheckPartial},                                              // a head partial still counts against the PR
		{[]Check{head("x", CheckPartial), base("x", CheckAbsent), head("y", CheckPass), base("y", CheckFail)}, CheckPartial},  // and is not pre-existing
		{[]Check{head("x", CheckAbsent), base("x", CheckAbsent)}, CheckFail},                                                  // absence on the PR build is a failure even without a completed claim
		{[]Check{head("x", CheckAbsent)}, CheckFail},                                                                          // absence on the PR build is a failure even without a completed claim
		{[]Check{head("x", CheckAbsent), head("y", CheckPass), base("y", CheckFail)}, CheckFail},                              // absence on the PR build counts as a failure
		{[]Check{head("x", CheckAbsent), base("x", CheckPass), head("y", CheckPass), base("y", CheckAbsent)}, CheckFail},      // a head absent beside a base pass is a failure
		{[]Check{head("x", CheckPass), base("x", CheckAbsent), head("y", CheckPass)}, CheckPass},                              // y was never compared; the difference on x carries it
		{[]Check{head("x", CheckFail), same(base("x", CheckFail)), head("y", CheckPass), base("y", CheckFail)}, CheckPartial},
		{[]Check{head("x", CheckPartial), same(base("x", CheckPartial)), head("y", CheckPass), base("y", CheckFail)}, CheckPartial},
		{[]Check{head("x", CheckFail), same(base("x", CheckFail))}, CheckPartial},
		{[]Check{head("x", CheckPass), {Name: "x", State: CheckFail, Leg: "base", Receipts: []Receipt{{Action: "click", Leg: "base"}}}}, CheckPartial},
		{[]Check{base("x", CheckAbsent)}, CheckUntested}, // a base-only check never decides
	} {
		if got, _ := derive(c.checks); got != c.want {
			t.Errorf("case %d: %s, want %s", i, got, c.want)
		}
	}
	for i, c := range []struct { // the reason says why a headline stayed partial
		checks []Check
		want   string
	}{
		{[]Check{head("x", CheckSkipped), head("y", CheckSkipped)}, reasonUntested},
		{[]Check{base("x", CheckFail)}, reasonUntested},
		{[]Check{head("x", CheckPass), head("y", CheckSkipped)}, "no check passed on the head while failing on the base"},
		{[]Check{head("x", CheckFail), base("x", CheckFail)}, "x x: fails on main too; not shown to be the same fault"},
		{[]Check{head("x", CheckAbsent), base("x", CheckAbsent)}, "the PR build does not have this"},
		{[]Check{head("x", CheckPass), base("x", CheckPass), head("y", CheckAbsent)}, "the PR build does not have this"},
	} {
		if _, reasons := derive(c.checks); !slices.ContainsFunc(reasons, func(r string) bool { return strings.Contains(r, c.want) }) {
			t.Errorf("reason case %d: %q lacks %q", i, reasons, c.want)
		}
	}
	for title, want := range map[string]bool{"fix(chat): x": true, "Update stuff": true, "CI(x): y": false} {
		if claimsChange(Run{Title: title}) != want {
			t.Errorf("claimsChange(%q) = %t", title, !want)
		}
	}
}

func TestDeriveSkippedHeadCheckCapsPassingChecksAtPartial(t *testing.T) {
	checks := []Check{
		{ID: "c1", Name: "update action", State: CheckSkipped, Claim: true, Note: "update notice never appeared"},
		{ID: "c2", Name: "menu opens", State: CheckPass, Claim: true},
		{ID: "c3", Name: "menu opens", State: CheckFail, Leg: "base", Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}},
		{ID: "c4", Name: "settings opens", State: CheckPass, Claim: true},
	}
	overall, reasons := derive(checks)
	want := []string{"c1 update action: not checked: update notice never appeared"}
	if overall != CheckPartial || !reflect.DeepEqual(reasons, want) {
		t.Fatalf("derive = %s, %q; want %s, %q", overall, reasons, CheckPartial, want)
	}
}

func TestDeriveSkipsDoNotCapWhenTheyAreNotClaims(t *testing.T) {
	checks := []Check{
		{ID: "c1", Name: "update action", State: CheckSkipped, Note: "optional context"},
		{ID: "c2", Name: "menu opens", State: CheckPass, Claim: true},
		{ID: "c3", Name: "menu opens", State: CheckFail, Leg: "base", Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}},
	}
	if overall, _ := derive(checks); overall != CheckPass {
		t.Fatalf("derive = %s, want pass", overall)
	}
}

func TestDeriveWithoutEndedClaimIsUntested(t *testing.T) {
	checks := []Check{{ID: "c1", Name: "menu opens", State: CheckSkipped, Claim: true, Note: "not reached"}}
	if overall, reasons := derive(checks); overall != CheckUntested || !slices.Contains(reasons, reasonUntested) {
		t.Fatalf("derive = %s, %q, want untested with %q", overall, reasons, reasonUntested)
	}
}

func TestDeriveKeepsRecordedHeadProblems(t *testing.T) {
	baseFail := Check{ID: "b1", Name: "claim", Leg: "base", State: CheckFail, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}}
	for _, tc := range []struct {
		name   string
		checks []Check
		want   CheckState
	}{
		{"nonclaim fail before a claim ends", []Check{{ID: "c1", Name: "regression", State: CheckFail}, {ID: "c2", Name: "claim", Claim: true, State: CheckSkipped}}, CheckFail},
		{"absent claim", []Check{{ID: "c1", Name: "claim", Claim: true, State: CheckAbsent}}, CheckFail},
		{"nonclaim partial caps a pass", []Check{{ID: "c1", Name: "claim", Claim: true, State: CheckPass}, baseFail, {ID: "c2", Name: "regression", State: CheckPartial}}, CheckPartial},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := derive(tc.checks); got != tc.want {
				t.Fatalf("derive = %s, want %s", got, tc.want)
			}
		})
	}
}

// submitSvc is a run with evidence on disk: head check c1 with its base twin c2, and head check c3 with no base.
func submitSvc(t *testing.T) (*Service, *fakeMedia, string, string) {
	t.Helper()
	svc, now := newSvc(t)
	fm := &fakeMedia{}
	svc.media = fm
	is(t, "")(svc.Intake(ctx, "s1", prURL))
	runDir := filepath.Join(svc.store.root, "run_s1")
	var actions []byte
	rcpt := func(seq int, leg string) Receipt {
		p := filepath.Join(runDir, "receipts", fmt.Sprintf("%d-after.png", seq))
		is(t, "")(nil, os.WriteFile(p, []byte("png"), 0o600))
		r := Receipt{Seq: seq, Leg: leg, Action: "click", Args: []string{"@e1"}, AfterPath: p, At: now.Add(time.Duration(seq*10) * time.Second)}
		line, _ := json.Marshal(actionRec{Seq: seq, Leg: leg, Action: "click", Args: r.Args, After: p, At: r.At, ConsoleTail: "ok\nUncaught TypeError: boom"})
		actions = append(actions, append(line, '\n')...)
		return r
	}
	is(t, "")(nil, svc.store.SaveChecks("run_s1", []Check{
		{ID: "c1", Name: "menu opens", Expect: "list | shown", State: CheckPass, Claim: true, Receipts: []Receipt{rcpt(1, "head")}},
		{ID: "c2", Name: "menu opens", Expect: "list shown", State: CheckFail, Leg: "base", Receipts: []Receipt{rcpt(2, "base")}},
		{ID: "c3", Name: "last model", Expect: "scrolls", State: CheckPass, Claim: true, Receipts: []Receipt{rcpt(3, "head")}},
	}))
	is(t, "")(nil, os.WriteFile(filepath.Join(runDir, "actions.jsonl"), actions, 0o600))
	is(t, "")(nil, os.MkdirAll(filepath.Join(runDir, "head", "logs"), 0o750))
	is(t, "")(nil, os.WriteFile(filepath.Join(runDir, "head", "logs", "dev.log"), []byte("boot\nlistening on :3200\n"), 0o600))
	_, err := svc.update("run_s1", func(r *Run) { r.Legs, r.Evidence = []Leg{{Name: "head", State: "running"}}, EvidenceOff }) // evidence has tests of its own
	is(t, "")(nil, err)
	addSkippedRisks(t, svc, "run_s1")
	return svc, fm, runDir, filepath.Join(svc.store.dataDir(), "artifacts", "s1")
}

// skipLastModelOnBase gives submitSvc's c3 (a head pass with no base result) the base result a running base needs.
func skipLastModelOnBase(t *testing.T, svc *Service) {
	t.Helper()
	checks, err := svc.store.Checks("run_s1")
	is(t, "")(nil, err)
	is(t, "")(nil, svc.store.SaveChecks("run_s1", append(checks, Check{ID: "c4", Name: "last model", Expect: "scrolls", State: CheckSkipped, Leg: "base", Note: "main has no such setting"})))
}

func exists(t *testing.T, p string) {
	t.Helper()
	if st, err := os.Stat(p); err != nil || st.Size() == 0 {
		t.Errorf("%s missing or empty: %v", p, err)
	}
}

func TestValidateVerdictLinks(t *testing.T) {
	runDir := t.TempDir()
	is(t, "")(nil, os.MkdirAll(filepath.Join(runDir, "receipts"), 0o750))
	for _, name := range []string{"1-after.png", "space (1).png", "(1).png", "a&b.png"} {
		is(t, "")(nil, os.WriteFile(filepath.Join(runDir, "receipts", name), []byte("png"), 0o600))
	}
	for _, tc := range []struct {
		name, md, missing string
	}{
		{"link", "[shot](receipts/1-after.png)", ""},
		{"image", "![shot](receipts/1-after.png#detail)", ""},
		{"encoded path", "![shot](receipts/space%20%281%29.png?raw=1#detail)", ""},
		{"escaped path", `[shot](receipts/\(1\).png)`, ""},
		{"path with spaces", "[shot](<receipts/space (1).png>)", ""},
		{"entity", "[shot](receipts/a&amp;b.png)", ""},
		{"reference", "![shot][s]\n\n[s]: receipts/1-after.png", ""},
		{"anchors and URLs", "[here](#checks) [web](https://example.com/missing) ![remote](//example.com/missing.png) [mail](mailto:a@example.com) [absolute](/missing.png)", ""},
		{"code", "`[example](missing.png)`\n\n```md\n![example](missing.png)\n```", ""},
		{"missing link", "[shot](receipts/missing.png)", "receipts/missing.png"},
		{"missing image", "![shot](receipts/missing.png)", "receipts/missing.png"},
		{"missing reference", "[shot][s]\n\n[s]: receipts/missing.png", "receipts/missing.png"},
		{"missing table link", "| shot |\n| --- |\n| [shot](receipts/missing.png) |", "receipts/missing.png"},
		{"directory", "[folder](receipts/)", "receipts/"},
		{"outside run", "[shot](../missing.png)", "../missing.png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateVerdictLinks(runDir, tc.md, "", nil)
			if tc.missing == "" {
				is(t, "")(nil, err)
			} else {
				is(t, "TESTING_VERDICT_LINKS_MISSING")(nil, err)
				if !strings.Contains(err.Error(), tc.missing) {
					t.Fatalf("error does not name broken link %q: %v", tc.missing, err)
				}
			}
		})
	}
}

func TestDeadlineNeutralizesBrokenInlineAndReferenceLinks(t *testing.T) {
	runDir := t.TempDir()
	for _, md := range []string{
		"[shot](receipts/missing.png)",
		"![shot][missing]\n\n[missing]: receipts/missing.png",
	} {
		got, count := neutralizeVerdictLinks(runDir, md)
		if count == 0 || strings.Contains(got, "receipts/missing.png") || strings.Contains(got, "[missing]") {
			t.Fatalf("neutralized %q as %q with count %d", md, got, count)
		}
		if err := validateVerdictLinks(runDir, got, "", nil); err != nil {
			t.Fatalf("neutralized markdown still failed validation: %v", err)
		}
	}
}

func TestSubmitRefusesBrokenVerdictLinks(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("blocked=%t", blocked), func(t *testing.T) {
			svc, media, runDir, art := submitSvc(t)
			svc.runner = &videoProbeRunner{duration: "70"}
			is(t, "")(nil, os.WriteFile(recordingPath(runDir, "head"), []byte("mp4"), 0o600))
			before, err := svc.update("run_s1", func(r *Run) { r.Legs[0].RecordStartedAt = svc.clock() })
			is(t, "")(nil, err)
			checks, err := svc.store.Checks("run_s1")
			is(t, "")(nil, err)
			checks[0].Note = "![missing shot](receipts/missing.png)"
			is(t, "")(nil, svc.store.SaveChecks("run_s1", checks))
			submit := func(summary string) (Verdict, error) {
				if blocked {
					return svc.SubmitBlocked(ctx, "s1", summary)
				}
				return svc.Submit(ctx, "s1", summary, "")
			}
			_, err = submit("[missing evidence](scratch/missing.txt#detail)")
			is(t, "TESTING_VERDICT_LINKS_MISSING")(nil, err)
			field := "summary"
			if blocked {
				field = "blocked reason"
			}
			for _, path := range []string{"check c1 note: receipts/missing.png", field + ": scratch/missing.txt#detail", "fix the summary, or re-end that check with a corrected note, then submit again"} {
				if !strings.Contains(err.Error(), path) {
					t.Errorf("error does not name broken link %q: %v", path, err)
				}
			}
			after, err := svc.store.Get("run_s1")
			is(t, "")(nil, err)
			if after.State != before.State || !reflect.DeepEqual(after.Legs, before.Legs) || !after.TestingEndedAt.IsZero() || after.submission != nil {
				t.Fatalf("refused submit closed the run: %+v", after)
			}
			if len(media.calls) != 0 {
				t.Fatalf("refused submit encoded video: %v", media.calls)
			}
			for _, dir := range []string{runDir, art} {
				if _, err := os.Stat(filepath.Join(dir, "verdict.md")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("refused submit wrote verdict.md in %s: %v", dir, err)
				}
			}
			is(t, "")(svc.CheckEnd(ctx, "s1", "c1", checks[0].State, "![shot](receipts/1-after.png)", false, 0))
			is(t, "")(submit("[shot](receipts/1-after.png#detail) [checks](#checks) [web](https://example.com/missing)"))
			exists(t, filepath.Join(runDir, "verdict.md"))
		})
	}
}

func TestSubmitIgnoresLinksOutsideEditableFields(t *testing.T) {
	svc, _, runDir, _ := submitSvc(t)
	checks, err := svc.store.Checks("run_s1")
	is(t, "")(nil, err)
	for i := range checks {
		checks[i].Name += " [name](missing-name.txt)"
		checks[i].Expect = "[expect](missing-expect.txt)"
		checks[i].NoteHistory = []string{"[old note](missing-history.txt)"}
	}
	is(t, "")(nil, svc.store.SaveChecks("run_s1", checks))
	_, err = svc.update("run_s1", func(r *Run) {
		r.Gaps = []string{"[intake gap](missing-gap.txt)"}
		r.Error = "[run error](missing-error.txt)"
	})
	is(t, "")(nil, err)
	is(t, "")(svc.Submit(ctx, "s1", "checked", ""))
	exists(t, filepath.Join(runDir, "verdict.md"))
}

func TestSubmitRefusesBrokenSkipBaseReason(t *testing.T) {
	svc, _, _, _ := submitSvc(t)
	_, err := svc.Submit(ctx, "s1", "checked", "[missing evidence](scratch/missing.txt)")
	is(t, "TESTING_VERDICT_LINKS_MISSING")(nil, err)
	if !strings.Contains(err.Error(), "skip-base reason: scratch/missing.txt") {
		t.Fatalf("skip-base link was not named: %v", err)
	}
	if !strings.Contains(err.Error(), "edit the --skip-base --reason") {
		t.Fatalf("skip-base link error did not name the field to edit: %v", err)
	}
}

func TestSubmitRejectsLongSummaryAndReasons(t *testing.T) {
	for _, tc := range []struct {
		name  string
		call  func(*Service) (Verdict, error)
		code  string
		limit int
	}{
		{"summary", func(s *Service) (Verdict, error) {
			return s.Submit(ctx, "s1", strings.Repeat("x", maxSummaryChars+1), "")
		}, "TESTING_SUMMARY_TOO_LONG", maxSummaryChars},
		{"skip-base reason", func(s *Service) (Verdict, error) {
			return s.Submit(ctx, "s1", "", strings.Repeat("x", maxReasonChars+1))
		}, "TESTING_REASON_TOO_LONG", maxReasonChars},
		{"blocked reason", func(s *Service) (Verdict, error) {
			return s.SubmitBlocked(ctx, "s1", strings.Repeat("x", maxReasonChars+1))
		}, "TESTING_REASON_TOO_LONG", maxReasonChars},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, _ := submitSvc(t)
			_, err := tc.call(svc)
			is(t, tc.code)(nil, err)
			if !strings.Contains(err.Error(), fmt.Sprint(tc.limit)) {
				t.Fatalf("length error does not name %d-character limit: %v", tc.limit, err)
			}
		})
	}
}

func TestDeadlineSanitizesBrokenLinksInWholeVerdict(t *testing.T) {
	svc, _, runDir, _ := submitSvc(t)
	run, err := svc.store.Get("run_s1")
	is(t, "")(nil, err)
	run.Gaps = []string{"[gap](missing-gap.txt)"}
	checks, err := svc.store.Checks(run.ID)
	is(t, "")(nil, err)
	checks[0].Name = "[menu](missing-name.txt)"
	checks[0].Expect = "[list](missing-expect.txt)"
	_, err = svc.closeRunAtDeadline(ctx, run, checks, "", "", "")
	is(t, "")(nil, err)
	md := readRunFile(t, svc, run, "verdict.md")
	if links := brokenRelativeLinks(runDir, md); len(links) > 0 {
		t.Fatalf("deadline verdict has broken links: %q", links)
	}
	if !strings.Contains(md, "broken relative link(s) were shown as plain text") {
		t.Fatalf("deadline verdict omitted broken-link warning: %s", md)
	}
}

func TestSubmitWritesVerdictAndPublishesArtifacts(t *testing.T) {
	svc, fm, runDir, art := submitSvc(t)
	v, err := svc.Submit(ctx, "s1", "looks right on head", "")
	if err != nil || v.Overall != CheckPass { // c1 passes on head and fails on base; c3 has no base result and does not matter
		t.Fatalf("verdict %+v err %v", v, err)
	}
	if len(fm.calls) != 0 {
		t.Fatalf("video encoded without a recording: %v", fm.calls)
	}
	md, _ := os.ReadFile(filepath.Join(runDir, "verdict.md"))
	for _, want := range []string{"# Verdict: pass", "| check | purpose | expect | head result | base result | receipts |", `| c1 menu opens |  | list \| shown | pass | fail | [1](receipts/1-after.png) |`,
		"| c3 last model |  | scrolls | pass | not compared on base |", "Uncaught TypeError: boom", "listening on :3200", "## Limits", "The base leg was not launched",
		"- c3 last model: not compared on base", "- " + headlineRule,
		"- The PR title claims a behavior change.", "Video: none (recording was not available)", "looks right on head"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("verdict.md lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(string(md), "| c2 ") {
		t.Errorf("the base twin got its own row:\n%s", md)
	}
	for _, d := range []string{runDir, art} { // the run dir and the session's artifact dir hold the same evidence
		for _, f := range []string{"verdict.md", "receipts/1-after.png", "receipts/3-after.png"} {
			exists(t, filepath.Join(d, f))
		}
	}
	index, _ := os.ReadFile(filepath.Join(art, "index.md"))
	for _, want := range []string{`<img src="receipts/1-after.png"`} {
		if !strings.Contains(string(index), want) {
			t.Fatalf("index.md lacks %q:\n%s", want, index)
		}
	}
	is(t, "TESTING_RUN_CLOSED")(svc.Submit(ctx, "s1", "", ""))
}

func TestSubmitKeepsIntakeDiagnosticsOutOfThePublicComment(t *testing.T) {
	svc, _, runDir, _ := submitSvc(t)
	const diagnostic = "recipe parser: private detail /tmp/secret"
	is(t, "")(nil, os.WriteFile(filepath.Join(runDir, intakeLogFile), []byte(diagnostic+"\n"), 0o600))

	if _, err := svc.Submit(ctx, "s1", "looks right on head", ""); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	verdict, err := os.ReadFile(filepath.Join(runDir, "verdict.md"))
	if err != nil {
		t.Fatalf("read verdict.md: %v", err)
	}
	if want := "Private intake diagnostic: " + diagnostic; !strings.Contains(string(verdict), want) {
		t.Fatalf("verdict.md lacks %q:\n%s", want, verdict)
	}
	comment, err := os.ReadFile(filepath.Join(runDir, "comment.md"))
	if err != nil {
		t.Fatalf("read comment.md: %v", err)
	}
	if strings.Contains(string(comment), diagnostic) {
		t.Fatalf("comment.md leaked private intake diagnostic:\n%s", comment)
	}
}

func TestSubmitRecordsIntakeDiagnosticsReadFailure(t *testing.T) {
	svc, _, runDir, _ := submitSvc(t)
	if err := os.Mkdir(filepath.Join(runDir, intakeLogFile), 0o700); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	_, err := svc.Submit(ctx, "s1", "looks right on head", "")
	if err != nil {
		t.Fatalf("Submit failed on a private diagnostic: %v", err)
	}
	if !strings.Contains(logs.String(), "could not read intake diagnostics") {
		t.Fatalf("diagnostics read failure was not logged: %s", logs.String())
	}
	if run, readErr := svc.store.Get("run_s1"); readErr != nil || run.State != RunSubmitted || run.TestingEndedAt.IsZero() {
		t.Fatalf("submit did not finish with the recorded testing end: run=%+v err=%v", run, readErr)
	}
	verdict, err := os.ReadFile(filepath.Join(runDir, "verdict.md"))
	if err != nil || !strings.Contains(string(verdict), "Private intake diagnostic: could not read intake diagnostics:") || !strings.Contains(string(verdict), "is a directory") {
		t.Fatalf("private verdict lost diagnostic read failure: %s, %v", verdict, err)
	}
	comment, err := os.ReadFile(filepath.Join(runDir, "comment.md"))
	if err != nil || strings.Contains(string(comment), "read intake diagnostics") || strings.Contains(string(comment), runDir) {
		t.Fatalf("public comment leaked private diagnostic: %s, %v", comment, err)
	}
}

func TestUnreadableIntakeLogDoesNotBlockVerdict(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline=%v", deadline), func(t *testing.T) {
			svc, _, runDir, _ := submitSvc(t)
			svc.kill = nil
			path := filepath.Join(runDir, intakeLogFile)
			is(t, "")(nil, os.WriteFile(path, []byte("private intake detail"), 0o600))
			is(t, "")(nil, os.Chmod(path, 0o000))
			t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
			if _, err := os.ReadFile(path); err == nil {
				t.Skip("mode 000 files are readable by this process")
			} else if !errors.Is(err, os.ErrPermission) {
				t.Fatalf("expected permission failure for intake.log: %v", err)
			}
			if deadline {
				run, err := svc.store.Get("run_s1")
				is(t, "")(nil, err)
				svc.clock = func() time.Time { return run.Deadline.Add(time.Second) }
				sweep(svc, nil)
			} else {
				is(t, "")(svc.Submit(ctx, "s1", "looks right on head", ""))
			}
			run, err := svc.store.Get("run_s1")
			if err != nil || run.State != RunSubmitted {
				t.Fatalf("unreadable log prevented verdict: run=%+v, err=%v", run, err)
			}
			verdict, err := os.ReadFile(filepath.Join(runDir, "verdict.md"))
			if err != nil || !strings.Contains(string(verdict), "Private intake diagnostic: could not read intake diagnostics:") || !strings.Contains(string(verdict), "permission denied") {
				t.Fatalf("private verdict lost permission error: %s, %v", verdict, err)
			}
			comment, err := os.ReadFile(filepath.Join(runDir, "comment.md"))
			if err != nil || strings.Contains(string(comment), "intake.log") || strings.Contains(string(comment), "permission denied") || strings.Contains(string(comment), "private intake detail") {
				t.Fatalf("public comment leaked intake diagnostic: %s, %v", comment, err)
			}
		})
	}
}

func TestCloseRunReturnsChecksAttributedAgainstLateSameFaultBase(t *testing.T) {
	svc, _, _, _ := submitSvc(t)
	run, err := svc.store.Get("run_s1")
	is(t, "")(nil, err)
	seed, err := svc.store.Checks(run.ID)
	is(t, "")(nil, err)
	checks := []Check{
		{ID: "c1", Name: "proven change", Claim: true, State: CheckPass, Receipts: seed[0].Receipts},
		{ID: "c2", Name: "proven change", Claim: true, Leg: "base", State: CheckFail, Receipts: seed[1].Receipts},
		{ID: "c3", Name: "existing-folder setup", Purpose: "risk", State: CheckPartial, Attribution: "pr", Receipts: seed[0].Receipts},
		{ID: "c4", Name: "existing-folder setup", Purpose: "risk", Leg: "base", State: CheckPartial, SameFault: true, Receipts: seed[1].Receipts},
	}
	v, err := svc.closeRun(ctx, run, checks, "", "", "")
	is(t, "")(nil, err)
	if v.Overall != CheckPass {
		t.Fatalf("late same-fault base result capped the verdict: %+v", v)
	}
	if got := v.Checks[2].Attribution; got != "pre-existing" {
		t.Fatalf("returned verdict kept the raw head attribution %q, want pre-existing", got)
	}
	if got := checks[2].Attribution; got != "pr" {
		t.Fatalf("normalizing the verdict mutated the saved tester label to %q", got)
	}
}

func TestSubmitCutsEachLegFromItsOwnRecordingAndSurvivesFFmpegFailure(t *testing.T) {
	svc, fm, runDir, art := submitSvc(t)
	svc.runner = &videoProbeRunner{duration: "120"}
	headMP4, baseMP4 := recordingPath(runDir, "head"), recordingPath(runDir, "base")
	for _, p := range []string{headMP4, baseMP4} {
		is(t, "")(nil, os.MkdirAll(filepath.Dir(p), 0o750))
		is(t, "")(nil, os.WriteFile(p, []byte("webm"), 0o600)) // what `record stop` left behind
	}
	t0 := svc.clock() // actions: head +10 s, base +20 s, head +30 s; the base recording began 15 s after the head's
	_, err := svc.update("run_s1", func(r *Run) {
		r.Legs = []Leg{{Name: "head", State: "running", RecordStartedAt: t0, RecordStoppedAt: t0.Add(time.Minute)}, {Name: "base", State: "running", RecordStartedAt: t0.Add(15 * time.Second), RecordStoppedAt: t0.Add(time.Minute)}}
	})
	is(t, "")(nil, err)
	skipLastModelOnBase(t, svc)
	is(t, "")(svc.Submit(ctx, "s1", "", ""))
	findCall := func(name string) string {
		for _, call := range fm.calls {
			if strings.HasSuffix(call[len(call)-1], name) {
				return strings.Join(call, " ")
			}
		}
		return ""
	}
	head, base := findCall(cutPath(runDir, "head")), findCall(cutPath(runDir, "base"))
	if head == "" || base == "" {
		t.Fatalf("want one cut per leg: %v", fm.calls)
	}
	if !strings.Contains(head, "-ss 9.000 -t 2.000 -i "+headMP4+" -ss 29.000 -t 2.000 -i "+headMP4+" -i ") || !strings.Contains(head, "concat=n=2") || !strings.HasSuffix(head, " "+cutPath(runDir, "head")) {
		t.Errorf("head cut: %s", head)
	}
	if !strings.Contains(base, "-ss 4.000 -t 2.000 -i "+baseMP4+" -i ") || !strings.Contains(base, "concat=n=1") || !strings.HasSuffix(base, " "+cutPath(runDir, "base")) {
		t.Errorf("base cut: %s", base)
	}
	headSRT, _ := os.ReadFile(filepath.Join(runDir, "head-cut-captions.srt"))
	baseSRT, _ := os.ReadFile(filepath.Join(runDir, "base-cut-captions.srt"))
	if string(headSRT) != "1\n00:00:00,000 --> 00:00:02,000\nStep 1: click an element\n\n2\n00:00:02,000 --> 00:00:04,000\nStep 3: click an element\n\n" ||
		string(baseSRT) != "1\n00:00:00,000 --> 00:00:02,000\nStep 2: click an element\n\n" {
		t.Fatalf("captions:\n%s\n%s", headSRT, baseSRT)
	}
	for _, f := range []string{"head.mp4", "base.mp4", "head-cut.mp4", "base-cut.mp4"} { // both videos reach the session's artifact dir
		exists(t, filepath.Join(art, f))
	}
	if _, err := os.Stat(filepath.Join(art, "run.mp4")); !os.IsNotExist(err) { // the single mislabelled video is gone
		t.Errorf("run.mp4 in the artifact dir: %v", err)
	}
	if md, _ := os.ReadFile(filepath.Join(runDir, "verdict.md")); !strings.Contains(string(md), "Video: head: recorded (head.mp4; head-cut.mp4 keeps a 2 s window around each action); base: recorded (base.mp4; base-cut.mp4") {
		t.Fatalf("verdict.md:\n%s", md)
	}
	if index, _ := os.ReadFile(filepath.Join(art, "index.md")); !strings.Contains(string(index), "Video: [head.mp4](head.mp4), [head-cut.mp4](head-cut.mp4), [base.mp4](base.mp4), [base-cut.mp4](base-cut.mp4).") {
		t.Fatalf("index.md:\n%s", index)
	}

	svc, fm, runDir, art = submitSvc(t) // a failing ffmpeg never blocks a verdict
	fm.err = errors.New("no ffmpeg")
	is(t, "")(svc.Submit(ctx, "s1", "", ""))
	if md, _ := os.ReadFile(filepath.Join(runDir, "verdict.md")); !strings.Contains(string(md), "Video: none") {
		t.Fatalf("verdict.md:\n%s", md)
	}
	exists(t, filepath.Join(art, "verdict.md"))
}

// Submit, cleanup and launch exclude each other, whichever started first, so a recording is never stopped before
// its launch has stored it.
func TestLaunchAndCloseRefuseEachOther(t *testing.T) {
	svc, _, _, _ := submitSvc(t)
	_, launching, err := svc.begin("s1", "launch base")
	is(t, "")(nil, err)
	is(t, "TESTING_RUN_BUSY")(svc.Submit(ctx, "s1", "", ""))
	is(t, "TESTING_RUN_BUSY")(svc.SubmitBlocked(ctx, "s1", "x"))
	is(t, "TESTING_RUN_BUSY")(svc.Cleanup(ctx, "s1"))
	_, other, err := svc.begin("s1", "launch head") // the two legs may still launch at once
	is(t, "")(nil, err)
	other()
	launching()

	for _, verb := range []string{"submit", "cleanup"} {
		svc.mu.Lock()
		closing, err := svc.markBusy("run_s1", verb)
		svc.mu.Unlock()
		is(t, "")(nil, err)
		is(t, "TESTING_RUN_BUSY")(svc.Launch(ctx, "s1", "base"))
		is(t, "TESTING_RUN_BUSY")(svc.Launch(ctx, "s1", "head"))
		closing()
	}
	is(t, "")(svc.Submit(ctx, "s1", "", "")) // nothing stays marked busy
}

func TestNotesAreKeptWholeAndOversizeNotesAreRefused(t *testing.T) {
	svc, _ := newSvc(t)
	is(t, "")(svc.Intake(ctx, "s1", prURL))
	is(t, "")(svc.CheckAdd(ctx, "s1", "reach the menu", "head shows the c2 receipt", "", "", "", CheckClassification{Purpose: "explore"}))
	is(t, "")(svc.CheckStart(ctx, "s1", "c1", CheckClassification{Purpose: "explore"}))
	long := "the base has none, the snapshot lacks it. " + strings.Repeat("word ", 50) // under the limit, with the tester's words
	got, err := svc.CheckEnd(ctx, "s1", "c1", CheckSkipped, long, false, 0)
	if err != nil || got.Note != strings.TrimSpace(long) {
		t.Fatalf("note not kept whole: %q, %v", got.Note, err)
	}
	if saved, _ := svc.store.Checks("run_s1"); saved[0].Note != strings.TrimSpace(long) {
		t.Errorf("checks.json holds %d characters of the note, want %d", len(saved[0].Note), len(strings.TrimSpace(long)))
	}
	body := renderComment(Run{Repo: "o/r", PRNumber: 1, BaseRef: "main"}, Verdict{Overall: CheckPartial, Checks: []Check{got}}, evidence{}, time.Now())
	if !strings.Contains(body, "the base has none") || strings.Contains(body, "the main has none") || strings.Contains(body, "main has none") {
		t.Errorf("the comment did not keep the tester's words:\n%s", body)
	}
	if strings.Contains(body, "...") || strings.Count(body, "word") < 50 {
		t.Errorf("the comment did not keep the long note:\n%s", body)
	}
	is(t, "")(svc.CheckAdd(ctx, "s1", "x", strings.Repeat("a", 500), "", "", "", CheckClassification{Purpose: "explore"}))
}

func TestCheckClaimPersistsAndOversizeNoteNamesItsLimit(t *testing.T) {
	svc, _ := newSvc(t)
	is(t, "")(svc.Intake(ctx, "s1", prURL))
	is(t, "")(svc.CheckAdd(ctx, "s1", "reach the menu", "the menu opens", "", "", "", CheckClassification{Purpose: "claim", ClaimSentence: "The row now stops that click."}))
	got, err := svc.CheckStart(ctx, "s1", "c1", CheckClassification{Purpose: "claim", ClaimSentence: "The row now stops that click."})
	is(t, "")(got, err)
	if !got.Claim {
		t.Fatal("started claimed check did not persist claim")
	}
	_, err = svc.CheckEnd(ctx, "s1", "c1", CheckSkipped, strings.Repeat("x", maxCheckNoteChars+1), false, 0)
	is(t, "TESTING_CHECK_NOTE_TOO_LONG")(nil, err)
	if !strings.Contains(err.Error(), fmt.Sprint(maxCheckNoteChars)) {
		t.Fatalf("oversize note error does not name the limit: %v", err)
	}
	checks, _ := svc.store.Checks("run_s1")
	if checks[0].State != CheckRunning || !checks[0].Claim {
		t.Fatalf("rejected note changed check: %+v", checks[0])
	}
}

func TestSkippedCheckNeedsNoReceiptAndLeavesUnclaimedRunUntested(t *testing.T) {
	svc, _ := newSvc(t)
	svc.media = &fakeMedia{}
	is(t, "")(svc.Intake(ctx, "s1", prURL))
	is(t, "")(svc.CheckAdd(ctx, "s1", "reach the menu", "opens", "", "", "", CheckClassification{Purpose: "explore"}))
	is(t, "")(svc.CheckAdd(ctx, "s1", "last model", "scrolls", "", "", "", CheckClassification{Purpose: "explore"}))
	is(t, "")(svc.CheckStart(ctx, "s1", "c1", CheckClassification{Purpose: "explore"}))
	is(t, "")(svc.CheckEnd(ctx, "s1", "c1", CheckSkipped, "needs a paid plan", false, 0))
	is(t, "")(svc.CheckStart(ctx, "s1", "c2", CheckClassification{Purpose: "explore"}))
	is(t, "TESTING_CHECK_NO_RECEIPT")(svc.CheckEnd(ctx, "s1", "c2", CheckPass, "", false, 0)) // only a skip is exempt
	checks, _ := svc.store.Checks("run_s1")
	checks[1].Receipts = []Receipt{{Seq: 1, Action: "click", AfterPath: evidenceFile(t, filepath.Join(svc.store.root, "run_s1"))}}
	is(t, "")(nil, svc.store.SaveChecks("run_s1", checks))
	is(t, "")(svc.CheckEnd(ctx, "s1", "c2", CheckPass, "", false, 0))
	_, err := svc.Submit(ctx, "s1", "", "")
	is(t, "TESTING_NO_CLAIM_CHECKS")(nil, err)
}

func TestCompareDiffsTheBaseShotAgainstHead(t *testing.T) {
	svc, f, _, runDir := driveSvc(t)
	shot := func(n string) string { return filepath.Join(runDir, "receipts", n) }
	is(t, "")(nil, svc.store.SaveChecks("run_s1", []Check{
		{ID: "c1", Name: "menu", State: CheckPass, Receipts: []Receipt{{Seq: 1, AfterPath: shot("1-after.png")}, {Seq: 4, AfterPath: shot("4-after.png")}}},
		{ID: "c2", Name: "Menu", Leg: "base", Receipts: []Receipt{{Seq: 2, Leg: "base", AfterPath: shot("2-after.png")}}},
		{ID: "c3", Name: "other", Receipts: []Receipt{{Seq: 3, AfterPath: shot("3-after.png")}}},
	}))
	is(t, "TESTING_COMPARE_INCOMPLETE")(svc.Compare(ctx, "s1", "c3")) // no base shot
	is(t, "TESTING_CHECK_NOT_FOUND")(svc.Compare(ctx, "s1", "c9"))
	is(t, "TESTING_ARGS_INVALID")(svc.Compare(ctx, "s1", " "))
	if len(f.calls) != 0 {
		t.Fatalf("a refused compare reached the engine: %v", f.calls)
	}
	f.out["diff"] = "3.2% differ"
	res, err := svc.Compare(ctx, "s1", "c1")
	if err != nil || res["diff"] != shot("diff-c1.png") || res["output"] != "3.2% differ" {
		t.Fatalf("compare %v err %v", res, err)
	}
	if c := f.find("diff"); !reflect.DeepEqual(c, []string{"diff", "screenshot", "--baseline", shot("2-after.png"), "--output", shot("diff-c1.png")}) {
		t.Fatalf("engine ran %v", c)
	}
	if got, _ := svc.store.Checks("run_s1"); got[0].Diff != shot("diff-c1.png") {
		t.Fatalf("diff path not kept on the check: %+v", got[0])
	}
}

func TestCompareKeepsNoDiffPathForAnImageTheEngineDidNotWrite(t *testing.T) {
	svc, f, _, runDir := driveSvc(t)
	shot := func(n string) string { return filepath.Join(runDir, "receipts", n) }
	is(t, "")(nil, svc.store.SaveChecks("run_s1", []Check{
		{ID: "c1", Name: "menu", Receipts: []Receipt{{Seq: 1, AfterPath: shot("1-after.png")}}},
		{ID: "c2", Name: "menu", Leg: "base", Receipts: []Receipt{{Seq: 2, Leg: "base", AfterPath: shot("2-after.png")}}},
	}))
	is(t, "")(nil, os.MkdirAll(shot(""), 0o700))
	is(t, "")(nil, os.WriteFile(shot("diff-c1.png"), []byte("old"), 0o600)) // a leftover must not pass for this run's image
	f.noImg = true
	res, err := svc.Compare(ctx, "s1", "c1")
	if err != nil || res["diff"] != "" || res["warning"] == nil {
		t.Fatalf("compare %v err %v", res, err)
	}
	if got, _ := svc.store.Checks("run_s1"); got[0].Diff != "" {
		t.Fatalf("a diff path was kept for a file that is not there: %q", got[0].Diff)
	}
}

// leaveLeg gives the head leg of submitSvc's run a live process group, a registered clone and an owned.json, as a launch would.
func leaveLeg(t *testing.T, svc *Service) (*fakeSys, *fakeRunner, chan string) {
	t.Helper()
	t0 := time.Date(2026, 10, 11, 8, 0, 0, 0, time.UTC)
	f, r, killed := &fakeSys{started: map[int]time.Time{100: t0}}, &fakeRunner{}, make(chan string, 4)
	svc.sys, svc.runner = f.ops(), r
	svc.closeEngine = func(id string) { f.log = append(f.log, "engine "+id) }
	svc.WithKiller(func(_ context.Context, id string) error { killed <- id; return nil })
	clone := t.TempDir()
	is(t, "")(nil, os.MkdirAll(filepath.Join(clone, ".git"), 0o750))
	_, err := svc.update("run_s1", func(r *Run) { r.Legs = []Leg{{Name: "head", State: "running", Worktree: clone}} })
	is(t, "")(nil, err)
	dir := svc.store.legDir("run_s1", "head")
	is(t, "")(nil, os.MkdirAll(filepath.Join(dir, "data", "worktrees"), 0o750))
	is(t, "")(nil, writeOwned(dir, Owned{RunID: "run_s1", Leg: "head", PGID: Proc{100, t0}, State: "running"}))
	return f, r, killed
}

func TestEverySubmitCleansItsLegAndLeavesTheCloneClean(t *testing.T) {
	endGrace = 0
	t.Cleanup(func() { endGrace = 10 * time.Second })
	for name, submit := range map[string]func(*Service) (Verdict, error){
		"pass":    func(s *Service) (Verdict, error) { return s.Submit(ctx, "s1", "", "") },
		"blocked": func(s *Service) (Verdict, error) { return s.SubmitBlocked(ctx, "s1", "seed failed") },
	} {
		t.Run(name, func(t *testing.T) {
			svc, _, runDir, _ := submitSvc(t)
			f, r, killed := leaveLeg(t, svc)
			if _, err := submit(svc); err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(f.log, "|"); !strings.Contains(got, "engine run_s1") || !strings.Contains(got, "killGroup 100") {
				t.Fatalf("submit left the leg running: %q", got)
			}
			if _, err := os.Stat(filepath.Join(svc.store.legDir("run_s1", "head"), "data")); err == nil {
				t.Fatal("the leg's private data survived the submit")
			}
			if r.count("worktree prune") != 1 {
				t.Fatalf("the shared clone was not pruned: %q", r.calls)
			}
			if run, _ := svc.store.Get("run_s1"); run.State != RunSubmitted || run.Legs[0].State != "cleaned" {
				t.Fatalf("run %+v", run)
			}
			if md, _ := os.ReadFile(filepath.Join(runDir, "verdict.md")); strings.Contains(string(md), "Cleanup after submit failed") {
				t.Fatalf("a clean submit reported a cleanup failure:\n%s", md)
			}
			select { // the tester ends after its grace, through the daemon's kill
			case id := <-killed:
				if id != "s1" {
					t.Fatalf("killed %q", id)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("the tester was not stopped after its run closed")
			}
		})
	}
}

func TestSubmitSurvivesAFailedCleanupAndSaysSo(t *testing.T) {
	svc, _, runDir, _ := submitSvc(t)
	_, r, _ := leaveLeg(t, svc)
	r.gitErr = map[string]error{"worktree prune": errors.New("fatal: not a git repository")}
	v, err := svc.Submit(ctx, "s1", "", "")
	if err != nil || v.Overall == "" {
		t.Fatalf("verdict %+v err %v", v, err)
	}
	md, _ := os.ReadFile(filepath.Join(runDir, "verdict.md"))
	if !strings.Contains(string(md), "- Cleanup after submit failed, stop leftovers with `ao testing cleanup`: ") || !strings.Contains(string(md), "not a git repository") {
		t.Fatalf("verdict.md hides the cleanup failure:\n%s", md)
	}
	// Pruning failed after the leg was killed and wiped. Its state must reflect
	// that completed cleanup even though the verdict still reports the prune error.
	if run, _ := svc.store.Get("run_s1"); run.State != RunSubmitted || run.Legs[0].State != "cleaned" {
		t.Fatalf("run %+v", run)
	}
}

func TestVerdictLinksOnlyFilesThatExist(t *testing.T) {
	svc, _, runDir, art := submitSvc(t)
	is(t, "")(nil, os.Remove(filepath.Join(runDir, "receipts", "3-after.png")))
	checks, _ := svc.store.Checks("run_s1")
	checks[0].Diff = filepath.Join(runDir, "receipts", "diff-c1.png") // recorded, never written
	is(t, "")(nil, svc.store.SaveChecks("run_s1", checks))
	is(t, "")(svc.Submit(ctx, "s1", "", ""))
	md, _ := os.ReadFile(filepath.Join(runDir, "verdict.md"))
	index, _ := os.ReadFile(filepath.Join(art, "index.md"))
	for _, doc := range []string{string(md), string(index)} {
		if strings.Contains(doc, "3-after.png") || strings.Contains(doc, "diff-c1.png") {
			t.Errorf("a missing file is referenced:\n%s", doc)
		}
	}
	for _, want := range []string{"c3 receipt 3: its after-shot file is missing, so it is not linked.", "c1 pixel diff: the image file is missing, so it is not linked."} {
		if !strings.Contains(string(md), want) {
			t.Errorf("Limits lack %q:\n%s", want, md)
		}
	}
	if !strings.Contains(string(md), "(receipts/1-after.png)") {
		t.Errorf("the receipt that exists lost its link:\n%s", md)
	}
}

func TestSubmitBlockedClosesTheRunWithoutReceipts(t *testing.T) {
	svc, _ := newSvc(t)
	var reported []string
	fail := errors.New("report store down")
	svc.WithReporter(func(_ context.Context, id, note string) error { reported = append(reported, id+": "+note); return fail })
	svc.WithRepos(func(context.Context, ...string) (string, error) {
		return "", errors.New("no registered AO project matches x")
	})
	run, err := svc.Intake(ctx, "s1", prURL)
	is(t, "")(nil, err)
	is(t, "")(svc.CheckAdd(ctx, "s1", "menu opens", "list shown", "", "", "", CheckClassification{Purpose: "claim", ClaimSentence: "The row now stops that click."})) // planned, never driven: a normal submit would refuse it
	addSkippedRisks(t, svc, run.ID)
	is(t, "TESTING_CHECKS_INCOMPLETE")(svc.Submit(ctx, "s1", "", ""))
	is(t, "TESTING_REASON_REQUIRED")(svc.SubmitBlocked(ctx, "s1", "  "))
	if len(reported) != 0 {
		t.Fatalf("refused submits reported: %v", reported)
	}

	v, err := svc.SubmitBlocked(ctx, "s1", " prepare refused: no checkout ") // a failing report does not fail the submit
	if err != nil || v.Overall != CheckBlocked || !reflect.DeepEqual(v.Reasons, []string{"prepare refused: no checkout"}) || len(v.Checks) != 3 {
		t.Fatalf("verdict %+v err %v", v, err)
	}
	md := readRunFile(t, svc, run, "verdict.md")
	for _, want := range []string{"# Verdict: blocked", "## Blocked\n\nprepare refused: no checkout", "Daemon: blocked at prepare: no registered AO project matches x", "c1 menu opens"} {
		if !strings.Contains(md, want) {
			t.Errorf("verdict.md lacks %q:\n%s", want, md)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(sessionartifacts.Dir(svc.store.dataDir(), domain.SessionID("s1")), "verdict.md")); string(got) != md {
		t.Fatal("verdict.md was not copied to the session artifacts")
	}
	if saved, err := svc.store.Get(run.ID); err != nil || saved.State != RunSubmitted {
		t.Fatalf("saved %+v err %v", saved, err)
	}
	if want := "s1: Litmus verdict blocked for orchestratorinc/agent-orchestrator#6394: see verdict.md in the session artifacts."; !reflect.DeepEqual(reported, []string{want}) {
		t.Fatalf("reported %q", reported)
	}
	is(t, "TESTING_RUN_CLOSED")(svc.SubmitBlocked(ctx, "s1", "again"))
}

func TestSubmitBlockedKeepsRecordedFailure(t *testing.T) {
	svc, now := newSvc(t)
	svc.media = &fakeMedia{}
	run := Run{ID: "run_s1", SessionID: "s1", Repo: "o/r", PRNumber: 7, State: RunRunning, CreatedAt: *now, Deadline: now.Add(RunBudget), Comment: CommentOff, Evidence: EvidenceOff}
	is(t, "")(nil, svc.store.Save(run))
	checks := []Check{
		{ID: "c1", Name: "menu", State: CheckFail, Note: "click closed the menu", Receipts: []Receipt{{Action: "click", AfterPath: evidenceFile(t, filepath.Join(svc.store.root, "run_s1"))}}},
		{ID: "c2", Name: "unfinished", State: CheckRunning},
	}
	is(t, "")(nil, svc.store.SaveChecks(run.ID, checks))
	v, err := svc.SubmitBlocked(ctx, "s1", " recorder stopped ")
	if err != nil || v.Overall != CheckFail || !slices.Equal(v.Reasons, []string{"recorder stopped", "c1 menu: click closed the menu"}) || v.StoppedEarly != "recorder stopped" {
		t.Fatalf("verdict %+v, %v", v, err)
	}
	md := readRunFile(t, svc, run, "verdict.md")
	for _, want := range []string{"# Verdict: fail", "Stopped early: recorder stopped", "c1 menu", "click closed the menu"} {
		if !strings.Contains(md, want) {
			t.Errorf("verdict.md lacks %q:\n%s", want, md)
		}
	}
	if saved, err := svc.store.Get(run.ID); err != nil || saved.State != RunSubmitted {
		t.Fatalf("saved %+v, %v", saved, err)
	}
	for _, file := range []string{"comment.md", "comments/1.md"} {
		body := readRunFile(t, svc, run, file)
		for _, want := range []string{"**Result: something broke; do not merge yet", "click closed the menu", "recorder stopped"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lost %q:\n%s", file, want, body)
			}
		}
	}
}

func TestSubmitBlockedKeepsRiskPartialWhenClaimsAreUnreached(t *testing.T) {
	for _, claimState := range []CheckState{CheckPending, CheckSkipped} {
		t.Run(string(claimState), func(t *testing.T) {
			svc, now := newSvc(t)
			run := Run{ID: "run_s1", SessionID: "s1", Repo: "o/r", PRNumber: 7, State: RunRunning, CreatedAt: *now, Deadline: now.Add(RunBudget), Comment: CommentOff, Evidence: EvidenceOff}
			is(t, "")(nil, svc.store.Save(run))
			checks := []Check{
				{ID: "c1", Name: "existing folder", Purpose: "risk", State: CheckPartial, Note: "the folder sometimes remains"},
				{ID: "c2", Name: "claimed behavior", Purpose: "claim", Claim: true, ClaimSentence: "The claimed behavior works.", State: claimState, Note: "not reached"},
			}
			is(t, "")(nil, svc.store.SaveChecks(run.ID, checks))
			derived, _ := derive(checks)
			if derived != CheckUntested {
				t.Fatalf("derive = %s, want %s", derived, CheckUntested)
			}

			v, err := svc.SubmitBlocked(ctx, "s1", "agent stopped before finishing")
			if err != nil || v.Overall != CheckUntested || v.StoppedEarly != "agent stopped before finishing" {
				t.Fatalf("verdict %+v, %v", v, err)
			}
			if len(v.Reasons) == 0 || v.Reasons[0] != v.StoppedEarly || !slices.Contains(v.Reasons, reasonUntested) {
				t.Fatalf("reasons %q do not preserve the derived untested verdict", v.Reasons)
			}
			md := readRunFile(t, svc, run, "verdict.md")
			for _, want := range []string{"# Verdict: untested", "Stopped early: agent stopped before finishing", "Testing stopped early: agent stopped before finishing"} {
				if !strings.Contains(md, want) {
					t.Errorf("verdict.md lacks %q:\n%s", want, md)
				}
			}
		})
	}
}

func TestSubmitBlockedKeepsPerBugFailure(t *testing.T) {
	svc, now := newSvc(t)
	svc.media = &fakeMedia{}
	run := Run{ID: "run_s1", SessionID: "s1", Repo: "o/r", PRNumber: 7, State: RunRunning, CreatedAt: *now, Deadline: now.Add(RunBudget), Comment: CommentOff, Evidence: EvidenceOff}
	is(t, "")(nil, svc.store.Save(run))
	checks := []Check{
		{ID: "c1", Name: "menu", State: CheckFail, Claim: true, Note: "click closed the menu", Receipts: []Receipt{{Action: "click", AfterPath: evidenceFile(t, filepath.Join(svc.store.root, "run_s1"))}}},
		{ID: "c2", Name: "menu", Leg: "base", State: CheckPass},
		{ID: "c3", Name: "unfinished", State: CheckRunning},
	}
	is(t, "")(nil, svc.store.SaveChecks(run.ID, checks))
	v, err := svc.SubmitBlocked(ctx, "s1", "recorder stopped")
	if err != nil || v.Overall != CheckFail || v.StoppedEarly != "recorder stopped" {
		t.Fatalf("verdict %+v, %v", v, err)
	}
	for _, file := range []string{"comment.md", "comments/1.md"} {
		body := readRunFile(t, svc, run, file)
		for _, want := range []string{"Bug 1 of 1:", "click closed the menu", "Stopped early: recorder stopped.", litmusCommentMarker()} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lost %q:\n%s", file, want, body)
			}
		}
	}
}

func TestSubmitRefusesAnUnlaunchedBaseUnlessSkipped(t *testing.T) {
	head := Leg{Name: "head", State: "running"}
	prepared := []Leg{head, {Name: "base", State: "prepared"}}
	for _, c := range []struct {
		name, want, skip string
		legs             []Leg
		left             time.Duration
	}{
		{"prepared base, time left", "TESTING_BASE_NOT_RUN", "", prepared, 10 * time.Minute},
		{"exactly the guard window left", "", "", prepared, baseGuardLeft},
		{"skipped with a reason while time is left", "", "a comparison would not change the verdict", prepared, 10 * time.Minute},
		{"skipped with a reason, base running", "", "main is slow", []Leg{head, {Name: "base", State: "running"}}, 10 * time.Minute},
		{"skipped with a reason, under the guard window", "", "the main build\nis broken", prepared, baseGuardLeft - time.Second},
		{"skipped with a reason, base failed to launch", "", "the main build\nis broken", []Leg{head, {Name: "base", State: "failed"}}, 10 * time.Minute},
		{"skipped without a reason", "TESTING_REASON_REQUIRED", "  ", prepared, 10 * time.Minute},
		{"too little time left", "", "", prepared, baseGuardLeft - time.Second},
		{"no base leg", "", "", []Leg{head}, 10 * time.Minute},
		{"base launch failed, no reason", "TESTING_BASE_NOT_RUN", "", []Leg{head, {Name: "base", State: "failed"}}, 10 * time.Minute},
		{"base launch failed, no reason, under the guard window", "TESTING_BASE_NOT_RUN", "", []Leg{head, {Name: "base", State: "failed"}}, baseGuardLeft - time.Second},
	} {
		svc, _, runDir, _ := submitSvc(t)
		_, err := svc.update("run_s1", func(r *Run) { r.Legs, r.Deadline = c.legs, svc.clock().Add(c.left) })
		is(t, "")(nil, err)
		v, err := svc.Submit(ctx, "s1", "x", c.skip)
		is(t, c.want)(v, err)
		md, _ := os.ReadFile(filepath.Join(runDir, "verdict.md"))
		switch {
		case c.want != "":
			if len(md) != 0 {
				t.Errorf("%s: a refused submit wrote verdict.md", c.name)
			}
			way := "repeat each check there in the app and record its visible result"
			if strings.Contains(c.name, "failed") {
				way = "submit with `--skip-base --reason"
			}
			if c.want == "TESTING_BASE_NOT_RUN" && !strings.Contains(err.Error(), way) {
				t.Errorf("%s: message %q does not teach the way out", c.name, err)
			}
		case c.skip != "":
			// submitSvc's c1 has a base result and c3 has none: main ran for one check, so the line says which was not
			if want := "- base compared on 1 of 2 checks; not run there: c3: " + strings.Join(strings.Fields(c.skip), " "); !strings.Contains(string(md), want) || strings.Contains(string(md), "base not run:") || !strings.Contains(string(md), "## Summary\n\nx\n") {
				t.Errorf("%s: verdict.md:\n%s", c.name, md)
			}
		case strings.Contains(string(md), "base not run:"):
			t.Errorf("%s: verdict.md claims a skip nobody asked for", c.name)
		}
	}
	svc, _, _, _ := submitSvc(t) // a blocked submit is never held back by the base
	_, err := svc.update("run_s1", func(r *Run) { r.Legs = prepared })
	is(t, "")(nil, err)
	is(t, "")(svc.SubmitBlocked(ctx, "s1", "prepare refused"))
}

func TestSubmitRefusesHeadChecksNotComparedOnBase(t *testing.T) {
	legs := []Leg{{Name: "head", State: "running"}, {Name: "base", State: "running"}}
	rc := func(leg string) []Receipt { return []Receipt{{Seq: 9, Leg: leg, Action: "click", AfterPath: "x.png"}} }
	pass := Check{ID: "c1", Name: "a", State: CheckPass, Claim: true, Purpose: "claim", ClaimSentence: "A works.", Receipts: rc("head")}
	partial := Check{ID: "c2", Name: "b", State: CheckPartial, Receipts: rc("head")}
	baseSkip := func(id, name, note string) Check {
		return Check{ID: id, Name: name, State: CheckSkipped, Leg: "base", Note: note}
	}
	for _, c := range []struct {
		name, want, skip, ids string
		checks                []Check
		legs                  []Leg
		left                  time.Duration
	}{
		{"partial head check, no base result", "TESTING_BASE_INCOMPLETE", "", "checks c1, c2 have", []Check{pass, partial}, legs, 10 * time.Minute},
		{"passing head check, no base result", "TESTING_BASE_INCOMPLETE", "", "check c1 has", []Check{pass}, legs, 10 * time.Minute},
		{"base skipped without a note", "TESTING_BASE_INCOMPLETE", "", "check c1 has", []Check{pass, baseSkip("c2", "a", " ")}, legs, 10 * time.Minute},
		{"base skipped with a note", "", "", "", []Check{pass, baseSkip("c2", "A", "main has no such menu")}, legs, 10 * time.Minute},
		{"a head check the tester could not reach needs no base result", "", "", "", []Check{pass, baseSkip("c2", "a", "no menu on main"), {ID: "c3", Name: "z", State: CheckSkipped, Note: "needs a key"}}, legs, 10 * time.Minute},
		{"base check of the same name ended", "", "", "", []Check{pass, partial, {ID: "c3", Name: "B", State: CheckFail, Leg: "base", Receipts: rc("base")}, baseSkip("c4", "A", "not repeated")}, legs, 10 * time.Minute},
		{"base receipts on the head check", "", "", "", []Check{pass, {ID: "c2", Name: "b", State: CheckFail, Receipts: append(rc("head"), rc("base")...)}, baseSkip("c3", "a", "not repeated")}, legs, 10 * time.Minute},
		{"skip-base after the base failed to launch", "", "main would not start", "", []Check{pass, partial}, []Leg{legs[0], {Name: "base", State: "failed"}}, 10 * time.Minute},
		{"too little time left", "", "", "", []Check{pass, partial}, legs, baseGuardLeft - time.Second},
		{"exactly the guard window left", "", "", "", []Check{pass, partial}, legs, baseGuardLeft},
		{"no base leg", "", "", "", []Check{pass, partial}, legs[:1], 10 * time.Minute},
	} {
		svc, _, runDir, _ := submitSvc(t)
		is(t, "")(nil, svc.store.SaveChecks("run_s1", c.checks))
		addSkippedRisks(t, svc, "run_s1")
		_, err := svc.update("run_s1", func(r *Run) { r.Legs, r.Deadline = c.legs, svc.clock().Add(c.left) })
		is(t, "")(nil, err)
		v, err := svc.Submit(ctx, "s1", "x", c.skip)
		if c.want == "" && err != nil && strings.Contains(err.Error(), "TESTING_BASE_INCOMPLETE") {
			t.Errorf("%s: refused: %v", c.name, err)
			continue
		}
		is(t, c.want)(v, err)
		if c.want != "" {
			if want := c.ids + " no result on main; repeat it there or record it with --result skipped and a note saying why"; !strings.Contains(err.Error(), want) {
				t.Errorf("%s: message %q", c.name, err)
			}
			if md, _ := os.ReadFile(filepath.Join(runDir, "verdict.md")); len(md) != 0 {
				t.Errorf("%s: a refused submit wrote verdict.md", c.name)
			}
		}
	}
	svc, _, _, _ := submitSvc(t) // a blocked submit is never held back
	is(t, "")(nil, svc.store.SaveChecks("run_s1", []Check{partial}))
	_, err := svc.update("run_s1", func(r *Run) { r.Legs = legs })
	is(t, "")(nil, err)
	is(t, "")(svc.SubmitBlocked(ctx, "s1", "prepare refused"))
}

func TestPreExistingHeadFailureIsLimitedNotBlocking(t *testing.T) {
	long := strings.Repeat("n", 200)
	checks := []Check{
		{ID: "c1", Name: "x", Purpose: "risk", State: CheckFail, Note: long, Receipts: []Receipt{{Leg: "head"}}},
		{ID: "c2", Name: "X", Purpose: "risk", State: CheckFail, Leg: "base", SameFault: true, Receipts: []Receipt{{Leg: "base", Action: "click", AfterPath: evidenceFile(t)}}},
		{ID: "c3", Name: "y", State: CheckFail, Note: "regressed"},
		{ID: "c4", Name: "y", State: CheckPass, Leg: "base"},
	}
	if got := checkLimit(checks, checks[0]); got != "c1 x: pre-existing, same on base: "+long {
		t.Errorf("limit %q", got)
	}
	if got := checkLimit(checks, checks[2]); got != "" { // it passes on the base: the PR broke it
		t.Errorf("a regression got the limit %q", got)
	}
	checks[1].State = CheckPartial // a base partial is not the same thing: the head fail may be the PR's
	if got := checkLimit(checks, checks[0]); strings.Contains(got, "pre-existing") {
		t.Errorf("a head fail beside a base partial got the limit %q", got)
	}
	if !strings.Contains(headlineRule, "pre-existing") {
		t.Error("the result rule does not mention pre-existing checks")
	}
}

// --skip-base is honoured at any time. Where main ran on some checks, each check without a main result gets a skipped
// main side carrying the tester's reason; where main ran on none, the reason stays run-wide.
func TestSubmitSkipBaseRecordsTheMainSideOfUnrepeatedChecks(t *testing.T) {
	legs := []Leg{{Name: "head", State: "running"}, {Name: "base", State: "running"}}
	rc := func(leg string) []Receipt { return []Receipt{{Seq: 9, Leg: leg, Action: "click", AfterPath: "x.png"}} }
	a := Check{ID: "c1", Name: "a", State: CheckPass, Claim: true, Purpose: "claim", ClaimSentence: "A works.", Receipts: rc("head")}
	aMain := Check{ID: "c2", Name: "a", State: CheckFail, Leg: "base", Receipts: rc("base")}
	b := Check{ID: "c3", Name: "b", State: CheckPartial, Receipts: rc("head")}
	for _, c := range []struct {
		name         string
		checks       []Check
		wantChecks   int
		wantLimit    string
		wantTwinNote string
	}{
		{"main ran on one of two", []Check{a, aMain, b}, 4, "- base compared on 1 of 2 checks; not run there: c3: main took too long", "main took too long"},
		{"main ran on none", []Check{a, b}, 2, "- base not run: main took too long", ""},
	} {
		svc, _, runDir, _ := submitSvc(t)
		is(t, "")(nil, svc.store.SaveChecks("run_s1", c.checks))
		addSkippedRisks(t, svc, "run_s1")
		_, err := svc.update("run_s1", func(r *Run) { r.Legs, r.Deadline = legs, svc.clock().Add(20*time.Minute) }) // far from the deadline
		is(t, "")(nil, err)
		is(t, "TESTING_BASE_INCOMPLETE")(svc.Submit(ctx, "s1", "x", "")) // without the flag the missing main result is still refused
		v, err := svc.Submit(ctx, "s1", "x", "main took too long")
		is(t, "")(nil, err)
		if len(v.Checks) != c.wantChecks+2 {
			t.Fatalf("%s: %d checks, want %d: %+v", c.name, len(v.Checks), c.wantChecks+2, v.Checks)
		}
		if c.wantTwinNote != "" {
			twin := v.Checks[len(v.Checks)-1]
			if twin.Name != "b" || twin.Leg != "base" || twin.State != CheckSkipped || twin.Note != c.wantTwinNote || twin.ID != "c6" {
				t.Errorf("%s: twin %+v", c.name, twin)
			}
		}
		md, _ := os.ReadFile(filepath.Join(runDir, "verdict.md"))
		if !strings.Contains(string(md), c.wantLimit) {
			t.Errorf("%s: verdict.md lacks %q:\n%s", c.name, c.wantLimit, md)
		}
	}
}

// A head partial beside a base fail is not "pre-existing": the feature may exist on the PR build and not on main, so
// neither the Limits line nor the comment says it happens on main too.
func TestHeadPartialBesideBaseFailIsNeverPreExisting(t *testing.T) {
	rc := func(leg string) []Receipt { return []Receipt{{Seq: 1, Leg: leg, Action: "click", AfterPath: "x.png"}} }
	checks := []Check{
		{ID: "c1", Name: "picker", Expect: "the picker lists agents", State: CheckPartial, Claim: true, Note: "the list shows, the Agent row does nothing", Receipts: rc("head")},
		{ID: "c2", Name: "picker", Expect: "the picker lists agents", State: CheckFail, Leg: "base", Note: "there is no agent list", Receipts: rc("base")},
	}
	if preExisting(checks, checks[0]) {
		t.Fatal("a head partial beside a base fail is pre-existing")
	}
	if got := checkLimit(checks, checks[0]); strings.Contains(got, "pre-existing") {
		t.Errorf("limit %q", got)
	}
	overall, reasons := derive(checks)
	if overall != CheckPartial || slices.Contains(reasons, reasonPrePartial) || slices.Contains(reasons, reasonPreOnly) || slices.Contains(reasons, reasonPreBoth) {
		t.Errorf("derive = %s %q", overall, reasons)
	}
	got := renderComment(commentRun, commentVerdict(checks), evidence{}, commentRun.CreatedAt)
	for _, banned := range []string{"happens on main", "on main too", "on main either", "did not cause it"} {
		if strings.Contains(got, banned) {
			t.Errorf("the comment says %q:\n%s", banned, got)
		}
	}
}

func TestAbsentIsNeverTheEqualState(t *testing.T) {
	rc := func(leg string) []Receipt { return []Receipt{{Seq: 1, Leg: leg, Action: "click", AfterPath: "x.png"}} }
	for _, st := range []CheckState{CheckFail, CheckPartial, CheckAbsent, CheckPass} {
		checks := []Check{
			{ID: "c1", Name: "picker", State: st, Note: "no row", Receipts: rc("head")},
			{ID: "c2", Name: "picker", State: CheckAbsent, Leg: "base", Note: "no picker", Receipts: rc("base")},
		}
		if preExisting(checks, checks[0]) {
			t.Errorf("a head %s beside an absent base is pre-existing", st)
		}
		if got := checkLimit(checks, checks[0]); strings.Contains(got, "pre-existing") {
			t.Errorf("head %s: limit %q", st, got)
		}
	}
	// An absent main is not a failure that ran: only the difference rule reads it.
	if ranAndFailed(Check{State: CheckAbsent, Receipts: rc("base")}) {
		t.Error("absent ran and failed")
	}
	if !failedOnBase([]Check{{Name: "x"}, {Name: "x", State: CheckAbsent, Leg: "base", Receipts: rc("base")}}, Check{Name: "x"}) {
		t.Error("an absent base is no difference")
	}
	if !strings.Contains(headlineRule, "absent") {
		t.Error("verdict.md does not state the absent rule")
	}
}

func TestDeadlineNeutralizesComplexBrokenLinkLabels(t *testing.T) {
	dir := t.TempDir()
	for _, md := range []string{
		"[outer [inner]](missing.png)",
		"[shortcut]\n\n[shortcut]: missing.png",
		"[label][ref]\n\n[ref]:\n  missing.png",
	} {
		if len(brokenRelativeLinks(dir, md)) == 0 {
			t.Fatalf("fixture is not a parsed broken link: %q", md)
		}
		clean, warnings := neutralizeVerdictLinks(dir, md)
		if warnings == 0 || len(brokenRelativeLinks(dir, clean)) > 0 {
			t.Fatalf("deadline lost verdict to %q: clean %q warnings %d", md, clean, warnings)
		}
	}
}

func TestSameFaultHeadProblemsArePreExistingOnlyWithBaseAfterShot(t *testing.T) {
	for _, state := range []CheckState{CheckFail, CheckPartial} {
		t.Run(string(state), func(t *testing.T) {
			head := Check{ID: "c5", Name: "existing-folder setup", Purpose: "risk", State: state, Attribution: "unknown"}
			base := Check{ID: "c12", Name: head.Name, Purpose: "risk", Leg: "base", State: state, SameFault: true, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}}
			checks := []Check{head, base}

			attributed := checkAgainstBase(checks, head)
			if attributed.Attribution != "pre-existing" || !preExisting(checks, head) {
				t.Fatalf("same fault with base after-shot was not attributed as pre-existing: %+v", attributed)
			}

			base.Receipts[0].AfterPath = filepath.Join(t.TempDir(), "missing-after.png")
			checks[1] = base
			if got := checkAgainstBase(checks, head); got.Attribution != "unknown" || preExisting(checks, head) {
				t.Fatalf("same fault without a base after-shot became pre-existing: %+v", got)
			}
		})
	}
}

func TestSameFaultEvidenceOverridesConflictingHeadAttribution(t *testing.T) {
	for _, attribution := range []string{"pr", "environment"} {
		for _, state := range []CheckState{CheckFail, CheckPartial} {
			t.Run(attribution+"/"+string(state), func(t *testing.T) {
				head := Check{ID: "c5", Name: "existing-folder setup", Purpose: "risk", State: state, Attribution: attribution}
				base := Check{ID: "c12", Name: head.Name, Purpose: "risk", Leg: "base", State: state, SameFault: true, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}}
				checks := []Check{head, base}
				if got := checkAgainstBase(checks, head); got.Attribution != "pre-existing" || !preExisting(checks, got) {
					t.Fatalf("same-fault base evidence did not override %q attribution: %+v", attribution, got)
				}
			})
		}
	}
}

func TestEvidencedSameFaultRiskDoesNotCapVerdict(t *testing.T) {
	proofHead := Check{ID: "c1", Name: "manage agents", Claim: true, State: CheckPass}
	proofBase := Check{ID: "c7", Name: proofHead.Name, Claim: true, Leg: "base", State: CheckFail, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}}
	for _, state := range []CheckState{CheckFail, CheckPartial} {
		t.Run(string(state), func(t *testing.T) {
			riskHead := Check{ID: "c5", Name: "existing-folder setup", Purpose: "risk", State: state, Attribution: "unknown"}
			riskBase := Check{ID: "c12", Name: riskHead.Name, Purpose: "risk", Leg: "base", State: state, SameFault: true, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}}
			checks := []Check{proofHead, proofBase, riskHead, riskBase}
			if got, reasons := derive(checks); got != CheckPass {
				t.Fatalf("same-fault %s capped the proven change: got %s, reasons %q", state, got, reasons)
			}
			if got := checkLimit(checks, riskHead); !strings.Contains(got, "pre-existing, same on base") {
				t.Fatalf("same-fault %s lost its pre-existing note: %q", state, got)
			}

			riskBase.SameFault = false
			checks[3] = riskBase
			if state == CheckPartial {
				if got, _ := derive(checks); got != CheckPartial {
					t.Fatalf("unexplained partial stopped capping the verdict: got %s", got)
				}
			}
		})
	}
}

func TestSameFaultClaimCapsOtherPassingClaimsAtPartial(t *testing.T) {
	for _, state := range []CheckState{CheckFail, CheckPartial} {
		t.Run(string(state), func(t *testing.T) {
			checks := []Check{
				{ID: "c1", Name: "old behavior", Purpose: "claim", Claim: true, State: state, Attribution: "unknown"},
				{ID: "c2", Name: "old behavior", Purpose: "claim", Claim: true, Leg: "base", State: state, SameFault: true, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}},
				{ID: "c3", Name: "new behavior", Claim: true, State: CheckPass},
				{ID: "c4", Name: "new behavior", Claim: true, Leg: "base", State: CheckFail, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}},
			}
			if got, reasons := derive(checks); got != CheckPartial || !slices.ContainsFunc(reasons, func(reason string) bool {
				return strings.Contains(reason, "fails the same way on main and the PR build")
			}) {
				t.Fatalf("same-fault claim %s did not cap the passing claim: got %s, reasons %q", state, got, reasons)
			}
		})
	}
}

func TestEnvironmentalHeadPartialStillCapsVerdict(t *testing.T) {
	checks := []Check{
		{ID: "c1", Name: "new behavior", Claim: true, State: CheckPass},
		{ID: "c2", Name: "new behavior", Claim: true, Leg: "base", State: CheckFail, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}},
		{ID: "c3", Name: "slow response", Purpose: "risk", State: CheckPartial, Attribution: "environment", EnvironmentCause: "timeout"},
	}
	if got, reasons := derive(checks); got != CheckPartial {
		t.Fatalf("environmental partial was not a cap: got %s, reasons %q", got, reasons)
	}
}
