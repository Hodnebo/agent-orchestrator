package litmus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

type fakeComment struct {
	ID          int64
	Login, Body string
}

// commentGH serves the comment endpoints from memory, records every argv, and reads each --input file when it is
// used. The listing comes back one JSON array per comment, as `gh --paginate` prints pages.
type commentGH struct {
	t             *testing.T
	login, author string
	private       string // "true" or "false": the repo's visibility
	comments      []fakeComment
	fail          string // an argv containing this fails
	notFound      int    // the next this many reads of an uploaded video answer 404
	calls, sent   []string
	argv          [][]string // every call's argv as gh got it
	uploads       int
}

const upload = "api --method POST -H Content-Type: video/mp4 https://uploads.github.com/"

func (g *commentGH) Run(ctx context.Context, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil { // like gh, which is killed with its context
		return nil, err
	}
	line := strings.Join(args, " ")
	g.calls = append(g.calls, line)
	g.argv = append(g.argv, args)
	var in struct{ Body string }
	if i := slices.Index(args, "--input"); i >= 0 && !strings.HasPrefix(line, upload) { // a video upload sends a file, not a JSON body
		raw, err := os.ReadFile(args[i+1])
		if err != nil {
			g.t.Fatal(err)
		}
		g.sent = append(g.sent, string(raw))
		_ = json.Unmarshal(raw, &in)
	}
	if g.fail != "" && strings.Contains(line, g.fail) {
		return nil, errors.New("HTTP 403: Resource not accessible\nsecond line")
	}
	switch {
	case line == "api user --jq .login":
		return []byte(g.login + "\n"), nil
	case strings.HasPrefix(line, "repo view "):
		return []byte(g.private + "\n"), nil
	case line == "auth setup-git":
		return nil, nil
	case line == "api repos/o/r --jq .id":
		return []byte("1234\n"), nil
	case strings.HasPrefix(line, upload):
		g.uploads++
		return []byte(fmt.Sprintf(`{"url":"https://github.com/user-attachments/assets/%08d-aaaa-4bbb-8ccc-dddddddddddd"}`, g.uploads)), nil
	case strings.HasPrefix(line, "api -i https://github.com/user-attachments/assets/"):
		if g.notFound > 0 {
			g.notFound--
			return nil, errors.New("gh: Not Found (HTTP 404)")
		}
		return []byte("HTTP/2.0 200 OK\r\nContent-Length: 3\r\nContent-Type: video/mp4\r\n\r\nmp4"), nil
	case strings.HasPrefix(line, "pr view ") && strings.Contains(line, " --json author "):
		return []byte(g.author + "\n"), nil
	case strings.HasSuffix(line, "/comments --paginate"):
		out := "[]"
		if len(g.comments) > 0 {
			out = ""
		}
		for _, c := range g.comments {
			page, _ := json.Marshal([]map[string]any{{"id": c.ID, "body": c.Body, "user": map[string]string{"login": c.Login}}})
			out += string(page)
		}
		return []byte(out), nil
	case strings.HasPrefix(line, "api --method POST ") && strings.Contains(line, "/issues/") && strings.Contains(line, "/comments --input "):
		g.comments = append(g.comments, fakeComment{900, g.login, in.Body})
		return []byte(`{"id":900,"html_url":"https://github.com/o/r/pull/7#issuecomment-900"}`), nil
	case strings.HasPrefix(line, "api --method PATCH ") && strings.Contains(line, "/issues/comments/"):
		_, rest, _ := strings.Cut(line, "/issues/comments/")
		id := strings.Fields(rest)[0]
		return []byte(`{"html_url":"https://github.com/o/r/pull/7#issuecomment-` + id + `"}`), nil
	case strings.HasPrefix(line, "api --method POST ") && strings.Contains(line, "/reviews --input "):
		return []byte(`{"id":5}`), nil
	}
	g.t.Fatalf("unexpected gh call %q", line)
	return nil, nil
}

func (g *commentGH) has(prefix string) bool {
	return slices.ContainsFunc(g.calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

var commentRun = Run{ID: "run_s1", SessionID: "s1", Repo: "o/r", PRNumber: 7, BaseSHA: "0ca80cfc370b21443c4a2b9d22199c047b8c74e7", HeadSHA: "e2204e0f361867a28310e71421a4227ab1d09544",
	State: RunSubmitted, CreatedAt: time.Date(2026, 10, 11, 8, 56, 0, 0, time.UTC), Comment: CommentPost}

// commentChecks: c1 passes on the head and fails on the base, c3 has no base result, c4 could not be reached.
func commentChecks(t *testing.T) []Check {
	r := func(leg string) []Receipt {
		return []Receipt{{Seq: 1, Leg: leg, Action: "click", AfterPath: evidenceFile(t)}}
	}
	return []Check{
		{ID: "c1", Name: "menu opens", Expect: "the menu stays open", State: CheckPass, Claim: true, Note: "Head: the menu stayed open and grew to 14 rows (ref @e12 | focus kept) <b> ![x](u) &commat;", Receipts: append(r("head"), r("head")...)},
		{ID: "c2", Name: "menu opens", Expect: "the menu stays open", State: CheckFail, Claim: true, Leg: "base", Note: "Base: closed the submenu.", Receipts: r("base")},
		{ID: "c3", Name: "last model", Expect: "the last model is remembered", State: CheckPass, Claim: true, Note: strings.Repeat("long note ", 20), Receipts: r("head")},
		{ID: "c4", Name: "effort slider", Expect: "the effort slider moves", State: CheckSkipped, Claim: true, Note: "needs a paid key"},
	}
}

func commentVerdict(checks []Check) Verdict {
	overall, reasons := derive(checks)
	return Verdict{Overall: overall, Checks: checks, Reasons: reasons}
}

// slop is what a comment must never say: our own words (legs, receipts, ids, engine refs, file names) and the
// machine-written tells. It is checked on every line but the image links and the marker.
var slop = regexp.MustCompile(`(?i)\b(legs?|heads?|base|receipts?|cuts?|mp4|run_\w+|c[0-9]+|@e[0-9]+|observables?|exercised|artifacts?|headline|snapshots?|refs?|ao session|additionally|ensuring|leverage|robust|seamless|not just)\b|\.(png|gif)\b|\x{2014}|[\x{201c}\x{201d}\x{2018}\x{2019}]`)

func noSlop(t *testing.T, body string) {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "![") && !strings.HasPrefix(line, "<!--") && slop.MatchString(line) {
			t.Errorf("%q in %q", slop.FindString(line), line)
		}
	}
}

// noSlopSummary checks generated result and coverage wording only. Tester notes
// and check names may contain words such as head, base, or c1 and stay intact.
func noSlopSummary(t *testing.T, run Run, v Verdict) {
	t.Helper()
	tally := count(v.Checks)
	noSlop(t, verdictLine(v.Overall, tally, run.baseName())+"\n"+coverageLine(tally, run.baseName()))
}

// The builds finish before the tester's clock starts: the first line gives the build time and the testing time apart,
// and the testing time is what the budget is measured against.
func TestCommentSplitsBuildTimeFromTestingTime(t *testing.T) {
	run := commentRun
	run.Deadline = run.CreatedAt.Add(3*time.Minute + RunBudget) // prepare finished 3 minutes in
	run.TestingEndedAt = run.CreatedAt.Add(10 * time.Minute)    // submit finished before verdict rendering
	now := run.CreatedAt.Add(10 * time.Minute)
	if got := renderComment(run, commentVerdict(commentChecks(t)), evidence{}, now); !strings.HasPrefix(got, "Litmus tried #7 on the PR build and on main (builds 3 minutes, testing 7 minutes).") {
		t.Errorf("comment: %q", got[:min(len(got), 120)])
	}
	if md := renderVerdict(run, commentVerdict(commentChecks(t)), false, nil, t.TempDir(), now, nil); !strings.Contains(md, "Duration: builds 3m0s, testing 7m0s of a 25m budget, finalizing 0s.") {
		t.Errorf("verdict.md: %q", md[:min(len(md), 300)])
	}
}

func TestRenderCommentGolden(t *testing.T) {
	now := commentRun.CreatedAt.Add(4*time.Minute + 3*time.Second)
	got := renderComment(commentRun, commentVerdict(commentChecks(t)), evidence{}, now)
	for _, want := range []string{
		"Litmus tried #7 on the PR build and on main (4 minutes).",
		"**Result: partial: not everything could be tested.**",
		"Main reproduces the original problem in 1 claim check that passes on the PR build.",
		"2 completed checks on the PR build; 1 check compared on main; 1 check with different results.",
		"Passed: menu opens (main does not do this); last model (main: not compared)",
		"Not tested: effort slider (needs a paid key; main: not compared)",
		"<details><summary>Full check notes</summary>",
		"- menu opens. Expected: the menu stays open.",
		"The PR build: pass. Head: the menu stayed open",
		"Main: fail. Base: closed the submenu.",
		"- last model. Expected: the last model is remembered.",
		"- effort slider. Expected: the effort slider moves.",
		"The PR build: skipped. needs a paid key",
		"</details>\n\n<!-- litmus:comment o/r#7 -->",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("comment missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Checked:\n") || strings.Contains(got, "Litmus couldn't test this:\n") {
		t.Fatalf("comment retained the old check sections:\n%s", got)
	}
}

func TestRenderCommentKeepsFullCheckNotesInOneDetailsBlock(t *testing.T) {
	long := strings.Repeat("n", 300)
	var checks []Check
	for i := 1; i <= 6; i++ {
		checks = append(checks, Check{ID: fmt.Sprint("c", i), Name: "check " + fmt.Sprint(i), State: CheckPartial, Claim: true, Note: long, Receipts: []Receipt{{Seq: i}}})
	}
	v := commentVerdict(checks)
	for i := 0; i < 30; i++ { // far more Limits text than 6,000 characters
		v.Reasons = append(v.Reasons, fmt.Sprintf("reason %d %s", i, long))
	}
	got := renderComment(commentRun, v, evidence{}, commentRun.CreatedAt)
	if !strings.HasSuffix(got, "</details>\n\n"+commentMarker("o/r", 7)) || !strings.HasPrefix(got, "Litmus tried #7 on the PR build (under a minute).\n\n**Result: partial: something misbehaved.**\n") {
		t.Fatalf("comment did not end with the full notes and marker: %q", got[max(len(got)-120, 0):])
	}
	if strings.Count(got, "<details><summary>Full check notes</summary>") != 1 {
		t.Fatalf("expected one full check notes block:\n%s", got)
	}
	start := strings.Index(got, "<details><summary>Full check notes</summary>")
	end := strings.Index(got[start:], "</details>") + start
	if strings.Count(got[start:end], long) != 6 {
		t.Fatalf("full notes lost or repeated observations: %d of 6\n%s", strings.Count(got[start:end], long), got)
	}
	for i := 1; i <= 6; i++ {
		if !strings.Contains(got, fmt.Sprintf("- check %d: partial", i)) {
			t.Errorf("partial check %d is missing from the compact rows:\n%s", i, got)
		}
	}

	for i := 0; i < 300; i++ { // required rows and notes outrank the soft prose cap
		checks = append(checks, checks[0])
	}
	got = renderComment(commentRun, commentVerdict(checks), evidence{}, commentRun.CreatedAt)
	start = strings.Index(got, "<details><summary>Full check notes</summary>")
	end = strings.Index(got[start:], "</details>") + start
	if !strings.HasSuffix(got, "</details>\n\n"+commentMarker("o/r", 7)) || strings.Count(got[start:end], long) != len(checks) || strings.Count(got, "<details><summary>Full check notes</summary>") != 1 {
		t.Fatalf("required check notes or marker lost: len %d, notes %d of %d", len(got), strings.Count(got[start:end], long), len(checks))
	}
}

// After a prepare timeout the comment says which build was ready, from the legs that finished.
func TestBlockedCommentNamesTheBuildThatWasReady(t *testing.T) {
	v := Verdict{Overall: CheckBlocked, Reasons: []string{"the builds were not ready after 20 minutes"}}
	run := commentRun
	run.Ready = []string{"head"}
	if got := renderComment(run, v, evidence{}, run.CreatedAt); !strings.Contains(got, "The PR build was ready; main was not. Nothing was checked.") || strings.Contains(got, "Nothing was built") {
		t.Errorf("head ready:\n%s", got)
	}
	run.Ready = []string{"base"}
	if got := renderComment(run, v, evidence{}, run.CreatedAt); !strings.Contains(got, "Main was ready; the PR build was not. Nothing was checked.") {
		t.Errorf("base ready:\n%s", got)
	}
}

func TestBlockedCommentCarriesTheReason(t *testing.T) {
	v := Verdict{Overall: CheckBlocked, Reasons: []string{"prepare refused: no checkout"}}
	got := renderComment(commentRun, v, evidence{}, commentRun.CreatedAt) // no leg and no receipt: nothing was built
	if !strings.HasPrefix(got, "Litmus could not start on #7 (prepare refused: no checkout). Nothing was built or checked.\n\n**Result: could not test this one.**\n") || strings.Contains(got, "I could not run the checks") || strings.Contains(got, "| what was checked |") {
		t.Fatalf("blocked comment:\n%s", got)
	}
	built := commentRun
	built.Legs = []Leg{{Name: "head", State: "cleaned"}}
	got = renderComment(built, v, evidence{}, commentRun.CreatedAt)
	if !strings.HasPrefix(got, "Litmus tried #7 on the PR build (under a minute).\n\n**Result: could not test this one.**\n") || !strings.Contains(got, "\nI could not run the checks. Prepare refused: no checkout.\n") {
		t.Fatalf("blocked comment after a build:\n%s", got)
	}
}

func TestBlockedRunKeepsTheReasonsTheFirstLineLeavesOut(t *testing.T) {
	long := strings.Repeat("word ", 40)
	v := Verdict{Overall: CheckBlocked, Reasons: []string{"prepare refused: no checkout", strings.TrimSpace(long)}}
	got := renderComment(commentRun, v, evidence{}, commentRun.CreatedAt) // no leg and no receipt: nothing was built
	if !strings.HasPrefix(got, "Litmus could not start on #7 (prepare refused: no checkout). Nothing was built or checked.\n\n") || strings.Count(got, "I could not run the checks.") != 1 || !strings.Contains(got, "\nI could not run the checks. Word word ") {
		t.Fatalf("second reason is not the one Limits bullet:\n%s", got)
	}
	v.Reasons = []string{strings.TrimSpace(long)} // a first reason now stays whole on the first line
	got = renderComment(commentRun, v, evidence{}, commentRun.CreatedAt)
	if !strings.Contains(got, "Litmus could not start on #7 ("+strings.TrimSpace(long)+")") || strings.Count(got, strings.TrimSpace(long)) != 1 {
		t.Fatalf("first reason was cut or duplicated:\n%s", got)
	}
}

func TestCommentSaysTheGapsAsLimitsWithoutChangingTheResult(t *testing.T) {
	now := commentRun.CreatedAt.Add(4 * time.Minute)
	plain := renderComment(commentRun, commentVerdict(commentChecks(t)), evidence{}, now)
	run := commentRun
	run.Gaps = []string{"I could not read the linked issue #123 (not found).", "The PR's patch is longer than 5.0 MB; I was shown the first 5.0 MB of it and read the rest with git (files.txt lists every file)."}
	got := renderComment(run, commentVerdict(commentChecks(t)), evidence{}, now)
	result := func(s string) string { return strings.SplitN(s, "\n", 4)[2] }
	gap1, gap2 := mdText(run.Gaps[0], math.MaxInt), mdText(run.Gaps[1], math.MaxInt)
	if result(got) != result(plain) || !strings.HasPrefix(result(got), "**Result: ") {
		t.Fatalf("the result line changed: %q vs %q", result(got), result(plain))
	}
	if !strings.Contains(got, "\n"+gap1+"\n"+gap2+"\n\n<details><summary>Full check notes</summary>") {
		t.Fatalf("gaps or final notes block are missing:\n%s", got)
	}
	noSlopSummary(t, run, commentVerdict(commentChecks(t)))

	v := commentVerdict(commentChecks(t)) // with Limits text far over the cap, the gaps are not what gets folded
	for i := 0; i < 30; i++ {
		v.Reasons = append(v.Reasons, fmt.Sprintf("reason %d %s", i, strings.Repeat("n", 300)))
	}
	got = renderComment(run, v, evidence{}, now)
	if !strings.Contains(got, gap1) || !strings.Contains(got, gap2) || !strings.Contains(got, "\nAnd ") {
		t.Fatalf("the cap folded the gaps:\n%s", got)
	}
}

// A gap is never clipped or folded into "And N more." at any fit, the tightest included: the other limits give way.
func TestCommentKeepsEveryGapWholeAtEveryFit(t *testing.T) {
	run := commentRun
	long := "I could not read the PR's file list with git (" + strings.Repeat("fatal: the object is missing ", 6) + "), so the areas it changes are unknown."
	run.Gaps = []string{long, "I could not read the linked issue #123 (not found)."}
	v := commentVerdict(commentChecks(t))
	for i := 0; i < 5; i++ {
		v.Reasons = append(v.Reasons, fmt.Sprintf("reason %d %s", i, strings.Repeat("n", 200)))
	}
	for _, f := range commentFits {
		got := commentBody(run, v, evidence{}, run.CreatedAt, f)
		if !strings.Contains(got, "\n"+mdText(long, len(long))+"\n"+mdText(run.Gaps[1], math.MaxInt)) {
			t.Errorf("fit %+v lost or clipped a gap:\n%s", f, got)
		}
		if f.bullets <= len(run.Gaps) && !strings.Contains(got, "\nAnd 5 more.\n\n<details><summary>Full check notes</summary>") {
			t.Errorf("fit %+v: the other limits are not folded into one line:\n%s", f, got[max(len(got)-400, 0):])
		}
	}
}

func TestBlockedRunSkipsTheCheckParagraphs(t *testing.T) {
	run := commentRun
	run.Areas = []Area{{Name: "Tests", Files: 4}}
	checks := []Check{{ID: "c1", Name: "artifactversions-tests", Expect: "the artifactversions package tests pass: files are captured after two stable ticks", Receipts: nil}}
	v := Verdict{Overall: CheckBlocked, Checks: checks, Reasons: []string{"prepare refused: no checkout"}}
	got := renderComment(run, v, evidence{}, run.CreatedAt.Add(7*time.Minute))
	for _, want := range []string{
		"Litmus could not start on #7 (prepare refused: no checkout). Nothing was built or checked.",
		"**Result: could not test this one.**",
		"Not tested: artifactversions tests (No result recorded; main: not compared)",
		"Not checked by this run: tests (4 files).",
		checks[0].Expect,
		"The PR build: not recorded.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("blocked comment lost %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "I checked that") || strings.Contains(got, "| what was checked |") {
		t.Fatalf("blocked check has a prose paragraph: %s", got)
	}
	noSlopSummary(t, run, v)

	v.Overall = CheckPartial // not blocked: a check with nothing seen says so in one sentence, not an empty note
	if got := checkPara(run, v, checks[0], 80, 0); got != "Artifactversions tests could not be run on the PR build." {
		t.Fatalf("unobserved check: %q", got)
	}
}

func TestSubmitCommentModesAndFailure(t *testing.T) {
	svc, _, runDir, _ := submitSvc(t) // checks: c1 head pass, c2 base fail, c3 head pass: a PASS
	g := &commentGH{t: t, login: "litmus-bot"}
	svc.gh = g
	v, err := svc.Submit(ctx, "s1", "", "")
	run, _ := svc.store.Get("run_s1")
	body, _ := os.ReadFile(filepath.Join(runDir, "comment.md"))
	if err != nil || v.CommentState != "posted" || run.CommentState != "posted" || run.CommentURL != v.CommentURL || v.CommentURL == "" {
		t.Fatalf("posted: verdict %+v run %+v err %v", v, run, err)
	}
	if g.field(0, "body") != string(body) || !strings.HasPrefix(string(body), "Litmus tried #6394 on the PR build and on main (") {
		t.Fatalf("comment.md is not the posted body: %s", body)
	}

	for name, c := range map[string]struct {
		mode  string
		fail  string
		state string
	}{
		"off":         {CommentOff, "", "off"},
		"older run":   {"", "", "off"}, // a run from before comments existed never posts
		"login fails": {CommentPost, "api user", "failed:HTTP 403: Resource not accessible"},
		"list fails":  {CommentPost, "--paginate", "failed:HTTP 403: Resource not accessible"},
		"write fails": {CommentPost, "--method POST", "failed:HTTP 403: Resource not accessible"},
	} {
		t.Run(name, func(t *testing.T) {
			svc, _, runDir, _ := submitSvc(t)
			g := &commentGH{t: t, login: "litmus-bot", fail: c.fail}
			svc.gh = g
			_, err := svc.update("run_s1", func(r *Run) { r.Comment = c.mode })
			is(t, "")(nil, err)
			v, err := svc.Submit(ctx, "s1", "", "") // a failed post is not a failed submit
			run, _ := svc.store.Get("run_s1")
			if err != nil || v.CommentState != c.state || run.CommentState != c.state || run.State != RunSubmitted || v.CommentURL != "" {
				t.Fatalf("verdict %+v run %+v err %v", v, run, err)
			}
			exists(t, filepath.Join(runDir, "comment.md"))
			log, _ := os.ReadFile(filepath.Join(runDir, "comment.log"))
			log = regexp.MustCompile(`(?m)^\S+ pruned: .*\n`).ReplaceAll(log, nil) // the retention pass logs there too
			if (c.fail == "") != (len(g.calls) == 0 && len(log) == 0) || (c.fail != "" && !strings.Contains(string(log), "Resource not accessible")) {
				t.Fatalf("off must touch neither gh nor the log; failed must log: calls %q log %q", g.calls, log)
			}
		})
	}

	blocked, _, runDir, _ := submitSvc(t)
	bg := &commentGH{t: t, login: "litmus-bot"}
	blocked.gh = bg
	v, err = blocked.SubmitBlocked(ctx, "s1", "prepare refused")
	if body, _ = os.ReadFile(filepath.Join(runDir, "comment.md")); err != nil || v.CommentState != "off" || len(bg.calls) != 0 || !strings.Contains(string(body), "**Result: could not test this one.**") {
		t.Fatalf("blocked: %+v %v\n%s", v, err, body)
	}
}

func TestStartNoCommentSetsTheRunsMode(t *testing.T) {
	ids := []string{"s8", "s9"}
	svc, _ := newSvc(t)
	svc.WithSpawner(func(context.Context, SpawnRequest) (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	})
	for _, c := range []struct{ session, mode string }{{"s8", CommentPost}, {"s9", CommentOff}} {
		started, err := svc.Start(ctx, StartRequest{PRURL: prURL, NoComment: c.mode == CommentOff})
		run, ierr := svc.Intake(ctx, started.SessionID, prURL)
		if err != nil || ierr != nil || started.SessionID != c.session || run.Comment != c.mode {
			t.Fatalf("%s: started %+v run %+v errs %v %v", c.session, started, run, err, ierr)
		}
	}
}

// field is one string of the i-th request body that was sent.
func (g *commentGH) field(i int, name string) string {
	var m map[string]string
	_ = json.Unmarshal([]byte(g.sent[i]), &m)
	return m[name]
}

func TestMdTextDoublesBackslashes(t *testing.T) {
	// a trailing backslash must not escape the pipe that cell() adds, nor a note's own backslash the markup after it
	if got, want := mdText(`a\ | b\[x]`, 50), `a\\ \| b\\\[x]`; got != want {
		t.Fatalf("mdText = %q, want %q", got, want)
	}
}

func TestMdTextShortensOnlyAtSentenceBoundaries(t *testing.T) {
	for _, tc := range []struct {
		text string
		max  int
		want string
	}{
		{"First observation. Second observation continues beyond this limit.", 30, "First observation."},
		{"One long observation with no sentence stop inside the limit", 20, "One long observation with no sentence stop inside the limit"},
		{"First check! Another check goes on too long", 22, "First check!"},
	} {
		if got := mdText(tc.text, tc.max); got != tc.want {
			t.Errorf("mdText(%q, %d) = %q, want %q", tc.text, tc.max, got, tc.want)
		}
	}
}

func TestVerdictLineSentences(t *testing.T) {
	for _, c := range []struct {
		overall CheckState
		t       tally
		want    string
	}{
		{CheckPass, tally{}, "pass"},
		{CheckPass, tally{worth: 1}, "pass, 1 thing worth a look"},
		{CheckPass, tally{worth: 2}, "pass, 2 things worth a look"},
		{CheckPartial, tally{headProblems: 1}, "partial: something misbehaved"},
		{CheckPartial, tally{completed: 2, compared: 2}, "partial: no difference from main shown"},
		{CheckPartial, tally{completed: 2, compared: 1, notRun: 1}, "partial: not everything could be tested"},
		{CheckPartial, tally{}, "partial: not everything could be tested"},
		{CheckPartial, tally{skippedClaims: []Check{{Name: "login"}}}, "partial: not everything could be tested; skipped claim checks: login on the PR build"},
		{CheckFail, tally{}, "something broke; do not merge yet"},
		{CheckUntested, tally{}, "the PR's stated behavior was not checked"},
		{CheckBlocked, tally{}, "could not test this one"},
	} {
		if got := verdictLine(c.overall, c.t, "main"); got != c.want {
			t.Errorf("%s %+v: got %q, want %q", c.overall, c.t, got, c.want)
		}
	}
}

func TestAreaLineSaysWhichChangedAreasTheChecksDidNotCover(t *testing.T) {
	// run 11: three e2e specs, two unit tests and one component. The component's file name shares the stem "nav" with
	// "navigate", and it is the only area that is not tests, so it counts as covered either way. Tests are never run.
	pr := []Area{{Name: "Tests", Files: 5}, {Name: "Renderer components", Files: 1, Stems: []string{"TitlebarNav"}}}
	navigate := []Check{{Name: "history", Expect: "back and forward navigate", Note: "the arrows stayed", State: CheckPass}}
	if got, want := areaLine(pr, navigate), "Not checked by this run: tests (5 files)."; got != want {
		t.Fatalf("line %q, want %q", got, want)
	}
	if got, want := areaLine(pr, []Check{{Name: "colors", Expect: "the page is red", State: CheckFail}}), "Not checked by this run: tests (5 files)."; got != want {
		t.Fatalf("the only area besides tests is about what the checks claim: %q, want %q", got, want)
	}
	checks := []Check{{Name: "history", Expect: "going back works", Note: "the sidebar shows the arrows", State: CheckPass}, {Name: "x", Note: "the latest build happens often; Project files changed", State: CheckPartial}}
	areas := []Area{{Name: "Shell sidebar", Files: 3, Stems: []string{"Sidebar", "useHistory"}}, {Name: "Tests", Files: 1}, {Name: "Project files", Files: 2, Stems: []string{"package", "lockfile"}}, {Name: "App", Files: 4, Stems: []string{"index"}}}
	want := "Not checked by this run: tests (1 file), project files (2 files), app (4 files)."
	if got := areaLine(areas, checks); got != want { // a file word must start a word the checks use; "index" and "use" are too common to count
		t.Fatalf("line %q, want %q", got, want)
	}
	if got, want := areaLine([]Area{{Name: "Shell sidebar", Files: 2}, {Name: "Chat", Files: 1}}, checks), "Not checked by this run: chat (1 file)."; got != want { // older runs kept no file names: the area's own words stand in
		t.Fatalf("older run: %q, want %q", got, want)
	}
	if got := areaLine([]Area{{Name: "Shell sidebar", Files: 2}}, checks); got != "" {
		t.Fatalf("everything covered, no line: %q", got)
	}
	if got := areaLine(nil, checks); got != "" {
		t.Fatalf("no areas, no line: %q", got)
	}
	var many []Area
	for i := 1; i <= 8; i++ {
		many = append(many, Area{Name: fmt.Sprint("Area", i), Files: i})
	}
	if got := areaLine(many[:7], nil); !strings.HasSuffix(got, " area6 (6 files), and 1 more area.") || strings.Contains(got, "area7") {
		t.Fatalf("seven areas: %q", got)
	}
	if got := areaLine(many, nil); !strings.HasSuffix(got, ", and 2 more areas.") {
		t.Fatalf("eight areas: %q", got)
	}
}

func TestAreaIsCoveredOnlyWhenACheckNamingItRan(t *testing.T) {
	areas := []Area{{Name: "Shell sidebar", Files: 3, Stems: []string{"Sidebar"}}, {Name: "Chat", Files: 2, Stems: []string{"Chat"}}}
	about := func(state CheckState, name string) Check {
		return Check{Name: name, Expect: "the " + name + " works", Note: "seen", State: state}
	}
	want := "Not checked by this run: shell sidebar (3 files)."
	if got := areaLine(areas, []Check{about(CheckSkipped, "sidebar"), about(CheckPass, "chat")}); got != want { // could not be done: not checked
		t.Errorf("skipped check: %q, want %q", got, want)
	}
	for _, state := range []CheckState{CheckPass, CheckFail, CheckPartial} {
		if got := areaLine(areas, []Check{about(state, "sidebar"), about(CheckPass, "chat")}); got != "" {
			t.Errorf("%s check: %q, want no line", state, got)
		}
	}
	// a skipped check alone covers nothing, even for the only non-test area
	if got, want := areaLine(areas[:1], []Check{about(CheckSkipped, "sidebar")}), "Not checked by this run: shell sidebar (3 files)."; got != want {
		t.Errorf("only area, skipped: %q, want %q", got, want)
	}
	// a check on main alone does not cover the PR build's area
	base := about(CheckPass, "sidebar")
	base.Leg = "base"
	if got := areaLine(areas[:1], []Check{base}); got == "" {
		t.Errorf("a main-only check covered an area")
	}
}

func TestCommentSaysWholeSentencesInTheReadersWords(t *testing.T) {
	long := "With sidebar expanded and brand shown, Collapse, Go back and Go forward buttons are all visible"
	over := strings.Repeat("the sidebar keeps every button ", 8) + "and then some"
	rc := func(leg string) []Receipt { return []Receipt{{Seq: 1, Leg: leg}} }
	checks := []Check{
		{ID: "c1", Name: "history-visible", Expect: long, State: CheckPass, Claim: true, Note: "Head shows the arrows", Receipts: rc("head")},
		{ID: "c2", Name: "history-visible", Expect: long, State: CheckFail, Claim: true, Leg: "base", Note: "Base: expanded sidebar shows only Collapse", Receipts: rc("base")},
		{ID: "c3", Name: "history-navigates", Expect: "Back/forward navigate and stay visible", State: CheckPartial, Claim: true, Note: "Forward did not land. C2 needs the arrows to navigate, so it cannot run there (see c1).", Receipts: rc("head")},
		{ID: "c4", Name: "long", Expect: over, State: CheckPass, Claim: true, Note: "fine", Receipts: rc("head")},
	}
	v := commentVerdict(checks)
	v.SkipBase = "C2 has no arrows; base has none when expanded, head is slow"
	got := renderComment(commentRun, v, evidence{}, commentRun.CreatedAt)
	for _, want := range []string{
		"Passed: history visible (main does not do this); long (main: not compared)",
		"- history navigates: partial (main: not compared). Forward did not land.",
		"Expected: " + long,
		"Expected: Back/forward navigate and stay visible",
		"Expected: " + over,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "....") || strings.Contains(got, "Worth a look") || strings.Contains(got, "could not compare this") {
		t.Errorf("clipped sentence or old closing line:\n%s", got)
	}
	noSlopSummary(t, commentRun, v)

	checks[1].State = CheckSkipped // nothing ran on main: the reason stands for the whole run, said once
	v = commentVerdict(checks)
	v.SkipBase = "C2 has no arrows; base has none when expanded, head is slow"
	got = renderComment(commentRun, v, evidence{}, commentRun.CreatedAt)
	if !strings.Contains(got, "Passed: history visible (not tested on main)") || !strings.Contains(got, "I did not run main: c2 has no arrows; base has none when expanded, head is slow") {
		t.Errorf("the skipped twin state or run-wide reason was lost:\n%s", got)
	}
	noSlopSummary(t, commentRun, v)
}

// A run that compared some checks on main says which, and never claims it did not run main.
func TestCommentSaysWhichChecksWereComparedOnMain(t *testing.T) {
	checks := commentChecks(t) // c1 compared, c3 has no main result, c4 could not be reached
	v := commentVerdict(checks)
	v.SkipBase = "main took too long to start"
	got := renderComment(commentRun, v, evidence{}, commentRun.CreatedAt)
	if want := "\n\nI compared 1 check on main; 1 was not run there: last model.\n\n"; !strings.Contains(got, want) {
		t.Errorf("no %q in\n%s", want, got)
	}
	if strings.Contains(got, "I did not run") {
		t.Errorf("the comment says main was not run:\n%s", got)
	}
	noSlopSummary(t, commentRun, v)

	checks = append(checks, Check{ID: "c5", Name: "last model", Expect: "the last model is remembered", State: CheckSkipped, Leg: "base", Note: "main took too long to start"}, Check{ID: "c6", Name: "pick", Expect: "the picker opens", State: CheckPass, Claim: true, Receipts: []Receipt{{Seq: 3, Leg: "head"}}})
	v = commentVerdict(checks)
	v.SkipBase = "main took too long to start"
	got = renderComment(commentRun, v, evidence{}, commentRun.CreatedAt)
	if want := "I compared 1 check on main; 2 were not run there: last model, pick."; !strings.Contains(got, want) {
		t.Errorf("no %q in\n%s", want, got)
	}
	if !strings.Contains(got, "Passed: menu opens (main does not do this); last model (not tested on main); pick") || !strings.Contains(got, "Not tested: effort slider (needs a paid key; main: not compared)") {
		t.Errorf("compact rows lost the head checks or skipped reason:\n%s", got)
	}
	if !strings.Contains(got, "- last model. Expected: the last model is remembered.") || !strings.Contains(got, "Main: skipped. main took too long to start") {
		t.Errorf("the full notes lost the skipped twin reason:\n%s", got)
	}
}

func TestVerdictLineSaysHowManyChecksWereComparedOnMain(t *testing.T) {
	if got := count(commentChecks(t)); got.completed != 2 || got.compared != 1 || got.notRun != 1 || got.differed != 1 {
		t.Errorf("counts = %+v, want 2 completed, 1 compared, 1 not run, 1 different", got)
	}
	for _, c := range []struct {
		t    tally
		want string
	}{
		{tally{completed: 7, compared: 5, differed: 2}, "7 completed checks on the PR build; 5 checks compared on main; 2 checks with different results."},
		{tally{completed: 2, compared: 1, differed: 1}, "2 completed checks on the PR build; 1 check compared on main; 1 check with different results."},
		{tally{completed: 2}, "2 completed checks on the PR build; 0 checks compared on main; 0 checks with different results."},
	} {
		if got := coverageLine(c.t, "main"); got != c.want {
			t.Errorf("coverageLine(%+v) = %q, want %q", c.t, got, c.want)
		}
	}
}

func TestHeroCaptionKeepsBothNotesWhole(t *testing.T) {
	both := hero{pr: &shot{}, base: &shot{}}
	if got := heroCaption(commentRun, both); got != "Main on the left, PR build on the right." {
		t.Errorf("no notes: %q", got)
	}
	both.baseNote, both.prNote = "No Go back or Go forward buttons", "Expanded sidebar shows Collapse, Go back, Go forward (both disabled on Home) and Orchestrator.inc brand; still present after toggling a project row"
	want := "Main on the left: no Go back or Go forward buttons. PR build on the right: expanded sidebar shows Collapse, Go back, Go forward (both disabled on Home) and Orchestrator.inc brand; still present after toggling a project row."
	if got := heroCaption(commentRun, both); got != want {
		t.Errorf("caption %q, want %q", got, want)
	}
	both.baseNote = ""
	if got := heroCaption(commentRun, both); got != "Main on the left, PR build on the right." {
		t.Errorf("one note missing: %q", got)
	}
}

func TestPlainTesterPreservesTheTestersVocabulary(t *testing.T) {
	checks := []Check{{ID: "c1", Name: "history-visible"}, {ID: "c3", Name: "menu opens"}}
	run := commentRun
	for _, in := range []string{
		"Head: both arrows show",
		"head leg shows it, the headline and database stay",
		"Base has none; BASE too",
		"C1 needs it, shown by c3, and c9 too",
		"nothing to change here",
	} {
		if got := plainTester(in, run, checks); got != in {
			t.Errorf("plainTester(%q) = %q, want the tester's words unchanged", in, got)
		}
	}
	run.BaseRef = "develop"
	if got := plainTester("base differs", run, checks); got != "base differs" {
		t.Errorf("tester text was changed to the configured branch name: %q", got)
	}
}

func TestPlainTesterLeavesPathAndCodeTokensAlone(t *testing.T) {
	run := commentRun
	checks := []Check{{ID: "c1", Name: "menu opens"}}
	for _, in := range []string{"/head/base/c1", `C:\head\base\c1`, "path.head", "`head`", "`head and base`", "``echo head && cat base``", "```head and c1```", "head.md", "base/c1", "http://host/head"} {
		if got := plainTester(in, run, checks); got != in {
			t.Errorf("plainTester(%q) = %q, want it unchanged", in, got)
		}
	}
	if got := plainTester("head and c1", run, checks); got != "head and c1" {
		t.Errorf("plainTester rewrote tester words: %q", got)
	}
}

func TestCommentGroupsNonClaimHeadPasses(t *testing.T) {
	checks := []Check{
		{ID: "c1", Name: "claim", Expect: "the claim works", Claim: true, State: CheckPass, Receipts: []Receipt{{Seq: 1}}},
		{ID: "c2", Name: "menu opens", Expect: "the menu opens", State: CheckPass, Note: "The menu opened."},
		{ID: "c3", Name: "sidebar opens", Expect: "the sidebar opens", Purpose: "risk", Risk: "layout changes hide sidebar controls", State: CheckPass, Note: "The sidebar opened."},
		{ID: "c4", Name: "settings persist", Expect: "settings persist", Purpose: "risk", Risk: "settings moves lose saved choices", State: CheckPass, Note: "Settings retained the saved choice."},
	}
	v := commentVerdict(checks)
	compact := compactChecks(commentRun, v)
	if !strings.Contains(compact, "Passed: claim (main: not compared); menu opens (main: not compared); sidebar opens (main: not compared); settings persist (main: not compared)") {
		t.Fatalf("all passing checks should use names in one line:\n%s", compact)
	}
	if strings.Contains(compact, "Risk:") || strings.Contains(compact, "layout changes hide") || strings.Contains(compact, "Settings moves lose") {
		t.Fatalf("compact rows exposed full risks:\n%s", compact)
	}
	full := fullCheckNotes(commentRun, v)
	for _, detail := range []string{"Risk: layout changes hide sidebar controls", "Risk: settings moves lose saved choices", "The menu opened.", "The sidebar opened.", "Settings retained the saved choice."} {
		if !strings.Contains(full, detail) {
			t.Errorf("full notes lost %q:\n%s", detail, full)
		}
	}
}

func TestCommentRendersRiskProblemsAsNonClaimChecks(t *testing.T) {
	checks := []Check{
		{ID: "c1", Name: "claim", Claim: true, State: CheckPass},
		{ID: "c2", Name: "push", Expect: "push succeeds", Purpose: "risk", Risk: "moving HOME loses git credentials", State: CheckFail, Attribution: "pr", Note: "push rejected credentials"},
		{ID: "b2", Name: "push", Leg: "base", Expect: "push succeeds", State: CheckPass, Note: "push worked"},
		{ID: "c3", Name: "old chats", Expect: "old chat opens", Purpose: "risk", Risk: "moving HOME hides old transcripts", State: CheckPartial, Attribution: "pr", Note: "only recent chats opened"},
		{ID: "b3", Name: "old chats", Leg: "base", Expect: "old chat opens", State: CheckPass, Note: "old chats opened"},
		{ID: "c4", Name: "menu", Expect: "the menu opens", State: CheckFail, Attribution: "pr", Note: "the menu stayed closed"},
		{ID: "b4", Name: "menu", Leg: "base", Expect: "the menu opens", State: CheckPass, Note: "the menu opened"},
	}
	v := commentVerdict(checks)
	compact := compactChecks(commentRun, v)
	for _, want := range []string{"- push: fail (works on main). push rejected credentials", "- old chats: partial (works on main). only recent chats opened", "- menu: fail (works on main). the menu stayed closed"} {
		if !strings.Contains(compact, want) {
			t.Errorf("failure row missing its first sentence %q:\n%s", want, compact)
		}
	}
	if strings.Contains(compact, "Risk:") || strings.Contains(compact, "moving HOME") {
		t.Fatalf("full risk text leaked into compact rows:\n%s", compact)
	}
	full := fullCheckNotes(commentRun, v)
	for _, want := range []string{"Expected: push succeeds.", "Risk: moving HOME loses git credentials", "push rejected credentials", "Expected: old chat opens.", "Risk: moving HOME hides old transcripts", "only recent chats opened"} {
		if !strings.Contains(full, want) {
			t.Errorf("full details missing %q:\n%s", want, full)
		}
	}
}

func TestEnvironmentCheckIsNotABugComment(t *testing.T) {
	head := Check{ID: "c1", Name: "menu opens", Expect: "the menu opens", Claim: true, State: CheckFail, Attribution: "environment", EnvironmentCause: "the browser could not launch", Receipts: []Receipt{{Seq: 1}}}
	base := Check{Name: head.Name, Leg: "base", Claim: true, State: CheckSkipped, Note: "the browser could not launch"}
	v := Verdict{Overall: CheckUntested, Checks: []Check{head, base}}
	if bugs := failingBugComments(v.Checks); len(bugs) != 0 {
		t.Fatalf("environment check became a bug: %#v", bugs)
	}
	body := renderComment(commentRun, v, evidence{}, commentRun.CreatedAt)
	if !strings.Contains(body, "Environment: the browser could not launch.") {
		t.Fatalf("environment cause missing from comment:\n%s", body)
	}
	if strings.Contains(body, "Bug 1 of 1") {
		t.Fatalf("environment check became a bug comment:\n%s", body)
	}
}

func TestEveryCheckUsesCompactRowsAtAnyCount(t *testing.T) {
	var checks []Check
	for i := 1; i <= 5; i++ {
		checks = append(checks, Check{ID: fmt.Sprint("c", i), Name: fmt.Sprint("check ", i), Expect: fmt.Sprint("thing ", i, " works"), State: CheckPass, Claim: true, Note: "fine", Receipts: []Receipt{{Seq: i}}})
	}
	got := renderComment(commentRun, commentVerdict(checks), evidence{}, commentRun.CreatedAt)
	if !strings.Contains(got, "Passed: check 1 (main: not compared); check 2 (main: not compared); check 3 (main: not compared); check 4 (main: not compared); check 5 (main: not compared)") {
		t.Fatalf("five checks are not listed by name on the compact pass line:\n%s", got)
	}
	if strings.Contains(got, "| what was checked |") || strings.Count(got, "<details><summary>Full check notes</summary>") != 1 {
		t.Fatalf("check table or repeated details block appeared:\n%s", got)
	}
	for i := 1; i <= 5; i++ {
		if !strings.Contains(got, fmt.Sprintf("Expected: thing %d works", i)) {
			t.Errorf("full expectation %d missing:\n%s", i, got)
		}
	}
}

func TestPlainTextKeepsQuestionsAndEmailAddresses(t *testing.T) {
	if got := plainSentence("does it save?", 80); got != "does it save?" {
		t.Errorf("question: %q", got)
	}
	if got, want := mdText("mail a@e2e.com, ref @e12 and @e7", 80), "mail a@\u200be2e.com, ref @\u200be12 and @\u200be7"; got != want {
		t.Errorf("refs: %q, want %q", got, want)
	}
}

func TestPlainSentenceTrimsTrailingSeparators(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"shows the workaround;", "shows the workaround."},
		{"shows the workaround:", "shows the workaround."},
		{"shows the workaround,", "shows the workaround."},
		{"&commat;", "&amp;commat;."},
		{"&#64;", "&amp;#64;."},
	} {
		if got := plainSentence(tc.in, 80); got != tc.want {
			t.Errorf("plainSentence(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCommentFirstLineNamesTheClosedIssues(t *testing.T) {
	url := func(n int) string { return fmt.Sprintf("https://github.com/o/r/issues/%d", n) }
	run := commentRun
	run.PRNumber = 6403
	for _, c := range []struct {
		issues []IssueRef
		want   string
	}{
		{nil, "Litmus tried #6403 on the PR build and on main (1 minute)."},
		{[]IssueRef{{Number: 6390, URL: url(6390)}}, "Litmus tried #6403 (for [#6390](" + url(6390) + ")) on the PR build and on main (1 minute)."},
		{[]IssueRef{{Number: 6390, URL: url(6390)}, {Number: 6391, URL: url(6391)}}, "Litmus tried #6403 (for [#6390](" + url(6390) + ") and [#6391](" + url(6391) + ")) on the PR build and on main (1 minute)."},
		{[]IssueRef{{Number: 1}, {Number: 2}, {Number: 3}}, "Litmus tried #6403 (for #1, #2 and #3) on the PR build and on main (1 minute)."},
	} {
		run.Issues = c.issues
		got := renderComment(run, commentVerdict(commentChecks(t)), evidence{}, run.CreatedAt.Add(time.Minute))
		if first, _, _ := strings.Cut(got, "\n"); first != c.want {
			t.Errorf("first line %q, want %q", first, c.want)
		}
		noSlopSummary(t, run, commentVerdict(commentChecks(t)))
	}
	run.Issues = []IssueRef{{Number: 6390, URL: url(6390)}}
	one := plainVerdict(run, commentVerdict(commentChecks(t)))
	for i := range one.Checks {
		one.Checks[i].Leg = "head"
		one.Checks[i].Receipts = nil
	}
	if first, _, _ := strings.Cut(commentBody(run, one, evidence{}, run.CreatedAt, commentFits[0]), "\n"); first != "Litmus tried #6403 (for [#6390]("+url(6390)+")) on the PR build (under a minute)." {
		t.Errorf("head-only first line %q", first)
	}
}

// A run whose base build is main at the PR's branch point says so where the comment introduces the two builds; every
// other mention of main stays, and a PR build alone has no main to qualify.
func TestCommentFirstLineSaysMainAtTheBranchPoint(t *testing.T) {
	run := commentRun
	run.PRNumber = 6403
	run.Gaps = []string{forkGapPrefix + "main 9 commits ago; 'main' in this report is that branch point (fca1b72c), not today's main."}
	got := renderComment(run, commentVerdict(commentChecks(t)), evidence{}, run.CreatedAt.Add(time.Minute))
	if first, _, _ := strings.Cut(got, "\n"); first != "Litmus tried #6403 on the PR build and on main at the PR's branch point (1 minute)." {
		t.Errorf("first line %q", first)
	}
	if !strings.Contains(got, "not today's main") {
		t.Errorf("the gap is missing from the comment:\n%s", got)
	}
	run.Gaps = []string{"GitHub counts 3 changed files and git found 2 from the fork point."}
	got = renderComment(run, commentVerdict(commentChecks(t)), evidence{}, run.CreatedAt.Add(time.Minute))
	if first, _, _ := strings.Cut(got, "\n"); first != "Litmus tried #6403 on the PR build and on main (1 minute)." {
		t.Errorf("first line without the fork gap %q", first)
	}
}

func TestMainAbsentIsNewNotFailing(t *testing.T) {
	rc := func(leg string) []Receipt { return []Receipt{{Seq: 1, Leg: leg, Action: "click", AfterPath: "x.png"}} }
	for _, c := range []struct {
		name   string
		head   CheckState
		note   string
		result string // the result line
		banned []string
	}{
		{"head fail", CheckFail, "Clicking the Agent row closed the picker without switching.", "something broke; do not merge yet",
			[]string{"happens on main", "did not cause it", "nothing differed", "fails on main", "On main, it did not work"}},
		{"head partial", CheckPartial, "The list showed but the Agent row did nothing.", "partial: something misbehaved",
			[]string{"happens on main", "did not cause it", "fails on main"}},
		{"head pass", CheckPass, "The picker switched agents.", "pass",
			[]string{"happens on main", "did not cause it", "nothing differed", "fails on main", "own problem"}},
	} {
		checks := []Check{
			{ID: "c1", Name: "agent-row", Expect: "clicking the Agent row switches the agent", State: c.head, Claim: true, Note: c.note, Receipts: rc("head")},
			{ID: "c2", Name: "agent-row", Expect: "clicking the Agent row switches the agent", State: CheckAbsent, Claim: true, Leg: "base", Note: "no agent picker there", Receipts: rc("base")},
		}
		v := commentVerdict(checks)
		got := renderComment(commentRun, v, evidence{}, commentRun.CreatedAt)
		if !strings.Contains(got, "**Result: "+c.result+".") {
			t.Errorf("%s: result line lacks %q:\n%s", c.name, c.result, got)
		}
		want := fmt.Sprintf("- agent row: %s (not on main). %s", c.head, c.note)
		if c.head == CheckPass {
			want = "Passed: agent row (not on main)"
		}
		if !strings.Contains(compactChecks(commentRun, v), want) {
			t.Errorf("%s: compact row lacks %q:\n%s", c.name, want, compactChecks(commentRun, v))
		}
		for _, detail := range []string{"Expected: clicking the Agent row switches the agent.", c.note, "Main: absent. no agent picker there"} {
			if !strings.Contains(fullCheckNotes(commentRun, v), detail) {
				t.Errorf("%s: full notes lost %q:\n%s", c.name, detail, fullCheckNotes(commentRun, v))
			}
		}
		for _, banned := range c.banned {
			if strings.Contains(got, banned) {
				t.Errorf("%s: comment says %q:\n%s", c.name, banned, got)
			}
		}
		noSlopSummary(t, commentRun, commentVerdict(checks))
		if cellWord(string(CheckAbsent)) == "fails" {
			t.Errorf("%s: an absent cell reads as a failure", c.name)
		}
	}
}

func TestHeadAbsentAloneFailsAndSaysMissing(t *testing.T) {
	checks := []Check{
		{ID: "c1", Name: "agent-row", Expect: "the Agent row is there", State: CheckAbsent, Claim: true, Note: "The picker has no Agent row.", Receipts: []Receipt{{Seq: 1, Action: "click", AfterPath: "x.png"}}},
		{ID: "c2", Name: "agent-row", Expect: "the Agent row is there", State: CheckAbsent, Claim: true, Leg: "base", Note: "No picker.", Receipts: []Receipt{{Seq: 1, Leg: "base", Action: "click", AfterPath: "x.png"}}},
	}
	v := commentVerdict(checks)
	got := renderComment(commentRun, v, evidence{}, commentRun.CreatedAt)
	if !strings.Contains(got, "**Result: something broke; do not merge yet.**") || !strings.Contains(compactChecks(commentRun, v), "- agent row: absent (not on main). The picker has no Agent row.") || !strings.Contains(fullCheckNotes(commentRun, v), "Expected: the Agent row is there.") || strings.Contains(got, "This is new in the PR") {
		t.Errorf("head absence is not named with its main state and full note:\n%s", got)
	}
}

func TestEqualFailuresNeedSameFaultInComment(t *testing.T) {
	for _, state := range []CheckState{CheckFail, CheckPartial} {
		checks := []Check{
			{ID: "c1", Name: "menu", State: state, Claim: true, Purpose: "claim", Note: "row does nothing", Receipts: []Receipt{{Action: "click", AfterPath: evidenceFile(t)}}},
			{ID: "c2", Name: "menu", State: state, Leg: "base", Claim: true, Purpose: "claim", Receipts: []Receipt{{Action: "click", Leg: "base", AfterPath: evidenceFile(t)}}},
		}
		v := commentVerdict(checks)
		wantOverall := CheckPartial
		if v.Overall != wantOverall {
			t.Fatalf("equal %s labels: %+v", state, v)
		}
		got := renderComment(commentRun, v, evidence{}, commentRun.CreatedAt)
		want := fmt.Sprintf("- menu: %s (%s). row does nothing", state, mainStatus("main", state, "claim"))
		if !strings.Contains(compactChecks(commentRun, v), want) || strings.Contains(got, "Pre-existing") || strings.Contains(got, "the PR did not cause it") || (state == CheckFail && (strings.Contains(got, "do not merge yet") || strings.Contains(got, "fine to merge"))) {
			t.Errorf("unproven %s sameness:\n%s", state, got)
		}
		checks[1].SameFault = true
		v = commentVerdict(checks)
		if v.Overall != CheckPartial || preExisting(checks, checks[0]) || !strings.Contains(strings.Join(v.Reasons, " "), "fails the same way on main and the PR build") {
			t.Fatalf("same %s fault: %+v", state, v)
		}
		got = renderComment(commentRun, v, evidence{}, commentRun.CreatedAt)
		if strings.Contains(stripCommentDetails(got), "Pre-existing.") || !strings.Contains(fullCheckNotes(commentRun, v), "row does nothing") {
			t.Errorf("same %s claim fault was exempted or its note was lost:\n%s", state, got)
		}
	}
}

func TestBugCommentConsoleErrors(t *testing.T) {
	const section = "<details><summary>App console errors during this check</summary>"
	for _, tc := range []struct {
		name     string
		state    CheckState
		receipts []Receipt
		want     []string
		absent   []string
	}{
		{name: "distinct lines across receipts", state: CheckFail, receipts: []Receipt{
			{ConsoleTail: "  Error: menu failed  \n\nError: menu failed"},
			{Leg: "head", ConsoleTail: "Error: menu failed\nError: save failed"},
		}, want: []string{"Error: menu failed", "Error: save failed"}},
		{name: "no console tails", state: CheckFail, receipts: []Receipt{{}, {Leg: "head"}}},
		{name: "base receipts excluded", state: CheckFail, receipts: []Receipt{
			{Leg: "head", ConsoleTail: "Error: head failed"},
			{Leg: "base", ConsoleTail: "Error: base failed"},
		}, want: []string{"Error: head failed"}, absent: []string{"Error: base failed"}},
		{name: "only base console tail", state: CheckFail, receipts: []Receipt{{Leg: "base", ConsoleTail: "Error: base failed"}}, absent: []string{"Error: base failed"}},
		{name: "local paths reduced", state: CheckPartial, receipts: []Receipt{{ConsoleTail: "Error at /Users/x/.ao/data/run/app.js:12:3 and file:///private/tmp/source.js"}},
			want: []string{"Error at app.js:12:3 and source.js"}, absent: []string{"/Users/", "/private/"}},
		{name: "five lines at most", state: CheckFail, receipts: []Receipt{{ConsoleTail: "one\ntwo\nthree\nfour\nfive\nsix"}},
			want: []string{"one", "two", "three", "four", "five"}, absent: []string{"six"}},
		{name: "two hundred characters", state: CheckFail, receipts: []Receipt{{ConsoleTail: strings.Repeat("界", 201)}}, want: []string{strings.Repeat("界", 200)}, absent: []string{strings.Repeat("界", 201)}},
		{name: "remote URL kept", state: CheckFail, receipts: []Receipt{{ConsoleTail: "Failed https://example.com/api/request"}}, want: []string{"Failed https://example.com/api/request"}},
		{name: "windows local path", state: CheckFail, receipts: []Receipt{{ConsoleTail: `Error at C:\Users\x\app.js`}}, want: []string{"Error at app.js"}, absent: []string{`C:\Users`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head := Check{ID: "head", Name: "menu opens", State: tc.state, Note: "The menu closed.", Receipts: tc.receipts}
			checks := []Check{head, {Name: head.Name, Leg: "base", State: CheckPass, Claim: true, Receipts: []Receipt{{Leg: "base", ConsoleTail: "other base error"}}}}
			bug := bugComment{Check: head, Number: 1, Total: 1}
			body := bugCommentBody(commentRun, Verdict{Checks: checks}, bug, "bug.png", "clip.mp4", false)
			if len(tc.want) == 0 {
				if strings.Contains(body, section) {
					t.Fatalf("unexpected console section: %s", body)
				}
			} else {
				want := section + "\n\n" + strings.Join(tc.want, "\n\n") + "\n\n</details>"
				if strings.Count(body, section) != 1 || !strings.Contains(body, want) {
					t.Fatalf("console section mismatch: %s", body)
				}
				if strings.Index(body, section) < strings.Index(body, "The menu closed.") || strings.Index(body, section) > strings.Index(body, "![") {
					t.Fatalf("console section must follow bug text and precede picture: %s", body)
				}
			}
			for _, absent := range append(tc.absent, "other base error") {
				if strings.Contains(body, absent) {
					t.Errorf("unexpected %q in %s", absent, body)
				}
			}
		})
	}
}

func TestBugCommentConsoleErrorsDedupesVolatileValues(t *testing.T) {
	for name, lines := range map[string]string{
		"ISO timestamps":   "request_id=abc port=4111 at 2026-10-16T18:08:51Z http://127.0.0.1:4111\nrequest_id=def port=4222 at 2027-11-17T19:09:52Z http://127.0.0.1:4222",
		"clock timestamps": "[18:08:51.123] request_id=abc failed\n[19:09:52.456] request_id=def failed",
		"epoch timestamps": "timestamp=1792174131123 request_id=abc failed\ntimestamp=1792264492456 request_id=def failed",
	} {
		t.Run(name, func(t *testing.T) {
			c := Check{State: CheckFail, Receipts: []Receipt{{ConsoleTail: lines}}}
			if got := checkConsoleErrors(c); len(got) != 1 {
				t.Fatalf("volatile console lines were not normalized: %#v", got)
			}
		})
	}
}

func TestCommentKeepsNotesInTheirRecordedSections(t *testing.T) {
	run := commentRun
	run.Gaps = []string{"Cleanup after submit failed; stop the test repo before retrying.", "The linked issue could not be read.", "The test repo showed the wrong result.", "The test repo creation failed because the new UI crashed.", "Cleanup button crashes."}
	checks := []Check{{ID: "c1", Name: "menu", Expect: "the menu opens", Claim: true, State: CheckPass, Note: "Cleanup the test repo after the run. The test repo showed the wrong result. Cleanup button crashes."}}
	v := commentVerdict(checks)
	v.Summary = "Cleanup the test repo after the run. The test repo showed the wrong result."
	body := renderComment(run, v, evidence{}, run.CreatedAt)
	fullStart := strings.Index(body, "<details><summary>Full check notes</summary>")
	if fullStart < 0 {
		t.Fatalf("full check notes missing:\n%s", body)
	}
	visible := strings.ToLower(body[:fullStart])
	if !strings.Contains(visible, "cleanup after submit failed") || !strings.Contains(visible, "stop the test repo") {
		t.Fatalf("recorded run gap disappeared from PR comment:\n%s", body)
	}
	if !strings.Contains(visible, "tester summary:\n\ncleanup the test repo after the run. the test repo showed the wrong result.") {
		t.Fatalf("tester summary was not kept as a summary:\n%s", body)
	}
	if !strings.Contains(body[fullStart:], "Cleanup the test repo after the run.") {
		t.Fatalf("the tester note was not preserved in full notes:\n%s", body)
	}
	if !strings.Contains(body, "The linked issue could not be read.") {
		t.Fatalf("user-facing gap disappeared from PR comment:\n%s", body)
	}
	if !strings.Contains(body, "The test repo showed the wrong result.") {
		t.Fatalf("a finding that mentions a test repo disappeared from PR comment:\n%s", body)
	}
	for _, finding := range []string{"The test repo creation failed because the new UI crashed.", "Cleanup button crashes."} {
		if !strings.Contains(body, finding) {
			t.Fatalf("finding was misclassified as a resource: %q\n%s", finding, body)
		}
	}
	md := renderVerdict(run, v, false, nil, t.TempDir(), run.CreatedAt, nil)
	if strings.Contains(md, "## Resources\n") {
		t.Fatalf("verdict guessed a resource category from tester prose:\n%s", md)
	}
	if !strings.Contains(md, "## Summary\n\n"+v.Summary) {
		t.Fatalf("tester summary changed sections or wording:\n%s", md)
	}
	_, gapSection, ok := strings.Cut(md, "## Gaps\n")
	if !ok {
		t.Fatalf("verdict lost the Gaps section:\n%s", md)
	}
	gapSection, _, _ = strings.Cut(gapSection, "\n## ")
	for _, gap := range run.Gaps {
		if !strings.Contains(gapSection, gap) {
			t.Fatalf("recorded gap was moved or lost: %q\n%s", gap, gapSection)
		}
	}
	if !strings.Contains(md, "Note: "+checks[0].Note) {
		t.Fatalf("check note was moved or lost:\n%s", md)
	}
}

func TestCommentSaysMainReproducesAClaimFailureThatPassesOnThePR(t *testing.T) {
	head := Check{ID: "c1", Name: "manage agents", Expect: "Manage agents opens interactive Settings.", Claim: true, State: CheckPass}
	base := Check{ID: "c7", Name: head.Name, Expect: head.Expect, Claim: true, Leg: "base", State: CheckFail, Note: "The overlay blocked the search click.", Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}}
	checks := []Check{head, base}
	want := "Main reproduces the original problem in 1 claim check that passes on the PR build."
	for name, body := range map[string]string{
		"comment": renderComment(commentRun, commentVerdict(checks), evidence{}, commentRun.CreatedAt),
		"summary": summaryCommentBody(commentRun, commentVerdict(checks), commentRun.CreatedAt, false),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("%s omitted the main-build reproduction: %s", name, body)
		}
	}

	headDidNotPass := head
	headDidNotPass.State = CheckPartial
	for name, variant := range map[string][]Check{
		"head did not pass":              []Check{headDidNotPass, {ID: "c7", Name: head.Name, Claim: true, Leg: "base", State: CheckFail, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}}},
		"base claim passed":              []Check{head, {ID: "c7", Name: head.Name, Claim: true, Leg: "base", State: CheckPass, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}}},
		"base failure has no after-shot": []Check{head, {ID: "c7", Name: head.Name, Claim: true, Leg: "base", State: CheckFail}},
	} {
		if got := originalProblemLine(commentRun, variant); got != "" {
			t.Errorf("%s produced an unsupported reproduction claim: %q", name, got)
		}
	}
}

func TestCompactClaimLineDoesNotDoublePunctuateExpectation(t *testing.T) {
	checks := []Check{{
		ID: "c1", Name: "worker agent management", Claim: true, State: CheckPass,
		Expect: "From cloned-project setup, Manage agents opens Settings and pointer input can focus and type in Search harnesses.",
	}}
	body := summaryCommentBody(commentRun, Verdict{Checks: checks}, commentRun.CreatedAt, false)
	if !strings.Contains(body, "Passed: worker agent management (main: not compared)") || !strings.Contains(body, "Expected: "+checks[0].Expect) || strings.Contains(body, "Search harnesses..") {
		t.Fatalf("claim expectation punctuation is malformed:\n%s", body)
	}
}

func TestCommentsNameSameFaultHeadProblemsAsPreExisting(t *testing.T) {
	for _, state := range []CheckState{CheckFail, CheckPartial} {
		t.Run(string(state), func(t *testing.T) {
			head := Check{ID: "c5", Name: "existing-folder setup", Expect: "existing-folder setup works.", Purpose: "risk", Risk: "existing folders", State: state, Attribution: "unknown"}
			base := Check{ID: "c12", Name: head.Name, Expect: head.Expect, Purpose: "risk", Leg: "base", State: state, SameFault: true, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}}
			checks := []Check{head, base}
			v := Verdict{Overall: CheckPass, Checks: checks}
			for name, body := range map[string]string{
				"full comment":    renderComment(commentRun, v, evidence{}, commentRun.CreatedAt),
				"summary comment": summaryCommentBody(commentRun, v, commentRun.CreatedAt, false),
				"compact checks":  compactChecks(commentRun, v),
			} {
				if !strings.Contains(body, "Pre-existing") {
					t.Errorf("%s omitted the pre-existing note for a %s check:\n%s", name, state, body)
				}
			}
		})
	}
}
