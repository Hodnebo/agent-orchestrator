package litmus

import (
	"bytes"
	"context"
	"errors"
	htmlstd "html"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuin/goldmark"
)

func TestMdTextKeepsIssueReferencesAsCopyableCode(t *testing.T) {
	input := "#123 [link](https://example.test) > quote; owner/repo#456"
	got := mdText(input, len(input))
	if !strings.HasPrefix(got, "`#123` ") || !strings.Contains(got, "`owner/repo#456`") {
		t.Fatalf("issue references were not neutralized: %q", got)
	}
	if !strings.Contains(got, `\[link](https://example.test)`) {
		t.Fatalf("existing Markdown text changed unexpectedly: %q", got)
	}

	var rendered bytes.Buffer
	if err := goldmark.New().Convert([]byte(got), &rendered); err != nil {
		t.Fatal(err)
	}
	html := rendered.String()
	for _, ref := range []string{"<code>#123</code>", "<code>owner/repo#456</code>"} {
		if !strings.Contains(html, ref) {
			t.Errorf("rendered text lost %q: %s", ref, html)
		}
	}
	if strings.Contains(html, "<a ") || strings.Contains(html, "<h1>") || strings.Contains(html, "<blockquote>") {
		t.Fatalf("tester text created a link or Markdown block: %s", html)
	}
}

func TestMdTextIssueReferencesSurviveArbitraryBackticks(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{"prefix `#123 and owner/repo#456", "prefix `<code>#123</code> and <code>owner/repo#456</code>"},
		{"prefix ``#123 and owner/repo#456", "prefix ``<code>#123</code> and <code>owner/repo#456</code>"},
		{"prefix ```#123 and owner/repo#456", "prefix ```<code>#123</code> and <code>owner/repo#456</code>"},
		{"prefix #123` and owner/repo#456", "prefix <code>#123</code>` and <code>owner/repo#456</code>"},
		{"prefix owner/repo#456`", "prefix <code>owner/repo#456</code>`"},
		{"already `#123` then #456 and owner/repo#789", "already <code>#123</code> then <code>#456</code> and <code>owner/repo#789</code>"},
		{"already ``#123`` then #456 and owner/repo#789", "already <code>#123</code> then <code>#456</code> and <code>owner/repo#789</code>"},
		{"already ```#123``` then #456 and owner/repo#789", "already <code>#123</code> then <code>#456</code> and <code>owner/repo#789</code>"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got := mdText(tc.input, len(tc.input))
			var rendered bytes.Buffer
			if err := goldmark.New().Convert([]byte(got), &rendered); err != nil {
				t.Fatal(err)
			}
			visibleHTML := htmlstd.UnescapeString(rendered.String())
			if !strings.Contains(visibleHTML, tc.want) {
				t.Errorf("rendered copy changed or reference lost its code span; want %q in %s", tc.want, visibleHTML)
			}
		})
	}
}

func TestMdTextIssueReferencesIgnoreProsePunctuationBoundaries(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{"Issue:#123", "Issue:<code>#123</code>"},
		{"(#123)", "(<code>#123</code>)"},
		{"#123,#456", "<code>#123</code>,<code>#456</code>"},
		{"say foo#123!", "say foo<code>#123</code>!"},
	} {
		got := mdText(tc.input, len(tc.input))
		var rendered bytes.Buffer
		if err := goldmark.New().Convert([]byte(got), &rendered); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rendered.String(), tc.want) {
			t.Errorf("mdText(%q) rendered %s, want %q", tc.input, rendered.String(), tc.want)
		}
	}
	if got := mdText("https://example.test/#123", 80); strings.Contains(got, "`#123`") {
		t.Errorf("URL fragment became a code span: %q", got)
	}
}

func TestRecordingProblemKeepsRawErrorPrivate(t *testing.T) {
	svc, _, _, runDir := driveSvc(t)
	const secret = "recording failed in /private/path/with details"
	if err := os.MkdirAll(filepath.Join(runDir, "head"), 0o750); err != nil {
		t.Fatal(err)
	}
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	svc.recordingProblem(run.ID, "head", errors.New(secret))
	run, err = svc.store.Get(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Gaps) != 1 || run.Gaps[0] != "The PR build's recording was not available." || strings.Contains(run.Gaps[0], secret) {
		t.Fatalf("public recording gap = %q", run.Gaps)
	}
	log, err := os.ReadFile(filepath.Join(runDir, "head", "engine.log"))
	if err != nil || !strings.Contains(string(log), secret) {
		t.Fatalf("private engine log %q, error %v", log, err)
	}
}

func TestThemeCaptureProblemDoesNotReachPublicComment(t *testing.T) {
	svc, _, _, _ := driveSvc(t)
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	e := svc.engineFor(run.ID, run.Legs[0])
	page, _, _ := fakeThemePageWithPreference(t, false, "light")
	request := page.request
	const secret = "private theme capture failure"
	page.request = func(ctx context.Context, method string, params, out any) error {
		if method == "Page.captureScreenshot" {
			return errors.New(secret)
		}
		return request(ctx, method, params, out)
	}
	e.connectPage = func(context.Context) (*pageCDP, error) { return page, nil }
	_, note := svc.themePair(ctx, e, "c1")
	if note != themePairFailed {
		t.Fatalf("theme note = %q", note)
	}
	check := Check{ID: "c1", Name: "menu", State: CheckFail, Receipts: []Receipt{{Leg: "head", ThemeNote: note}}}
	body := renderComment(run, Verdict{Overall: CheckFail, Checks: []Check{check}}, evidence{}, run.CreatedAt)
	if !strings.Contains(body, themePairFailed) || strings.Contains(body, secret) {
		t.Fatalf("public comment exposed theme error:\n%s", body)
	}
	log, err := os.ReadFile(filepath.Join(svc.store.legDir(run.ID, "head"), "engine.log"))
	if err != nil || !strings.Contains(string(log), secret) {
		t.Fatalf("private engine log %q, error %v", log, err)
	}
}

func TestEvidenceFailureKeepsRawErrorPrivate(t *testing.T) {
	svc := newTestService(t, NewStore(t.TempDir()), func() time.Time { return time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC) })
	run := Run{ID: "run_task42", SessionID: "s1", Repo: "owner/repo", PRNumber: 123, State: RunSubmitted, CreatedAt: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)}
	if err := svc.store.Save(run); err != nil {
		t.Fatal(err)
	}
	const secret = "git push failed at /private/path"
	ev := svc.evidenceFailed(run, errors.New(secret))
	body := commentBody(run, Verdict{Overall: CheckPass}, ev, run.CreatedAt, commentFits[0])
	if !strings.Contains(body, "I could not publish the screenshots and recordings.") || strings.Contains(body, secret) {
		t.Fatalf("public comment exposed evidence error:\n%s", body)
	}
	if ev.state != "failed:"+secret {
		t.Fatalf("internal evidence state = %q", ev.state)
	}
	log, err := os.ReadFile(filepath.Join(svc.store.root, run.ID, "comment.log"))
	if err != nil || !strings.Contains(string(log), secret) {
		t.Fatalf("private comment log %q, error %v", log, err)
	}
}

func TestImageFailureKeepsRawErrorPrivate(t *testing.T) {
	svc := newTestService(t, NewStore(t.TempDir()), func() time.Time { return time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC) })
	run := Run{ID: "run_task42", SessionID: "s1", Repo: "owner/repo", PRNumber: 123, State: RunSubmitted, CreatedAt: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)}
	if err := svc.store.Save(run); err != nil {
		t.Fatal(err)
	}
	checks := []Check{
		{ID: "head", Name: "menu opens", State: CheckFail},
		{ID: "base", Name: "menu opens", Leg: "base", State: CheckPass},
	}
	docs, err := svc.renderCommentDrafts(run, Verdict{Overall: CheckFail, Checks: checks}, run.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || !strings.Contains(docs[0].Body, "A comparison image could not be made.") || strings.Contains(docs[0].Body, "common light or dark theme") {
		t.Fatalf("public image failure text = %#v", docs)
	}
	log, err := os.ReadFile(filepath.Join(svc.store.root, run.ID, "comment.log"))
	if err != nil || !strings.Contains(string(log), "the PR and base shots have no recorded common light or dark theme") {
		t.Fatalf("private comment log %q, error %v", log, err)
	}
}
