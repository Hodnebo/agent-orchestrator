package litmus

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	htmlrenderer "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

func TestPublicCommentEscapesReplacementReasons(t *testing.T) {
	const reason = `PR says @octocat <b> & [link](target)`
	const escaped = `PR says @` + "\u200b" + `octocat &lt;b> &amp; \[link](target)`
	for _, completed := range []bool{false, true} {
		old := Check{ID: "c1", Name: "old", Claim: true, ClaimSentence: "claim", ReplaceReason: reason}
		replacement := Check{ID: "c2", Name: "new", Claim: true, ClaimSentence: "claim", Replaces: old.ID, State: CheckRunning}
		if completed {
			old.ReplacedBy, replacement.State, replacement.ReplaceReason = replacement.ID, CheckPass, reason
		}
		v := Verdict{Overall: CheckPartial, Checks: []Check{old, replacement}}
		for name, body := range map[string]string{
			"full":    renderComment(commentRun, v, evidence{}, commentRun.CreatedAt),
			"summary": summaryCommentBody(commentRun, v, commentRun.CreatedAt, true),
		} {
			if !strings.Contains(body, escaped) || strings.Contains(body, reason) {
				t.Errorf("%s completed=%v exposed replacement prose:\n%s", name, completed, body)
			}
		}
	}
}

func TestPublicCommentEscapesOtherTesterText(t *testing.T) {
	const prose = `@octocat <b> & [link](target)`
	const escaped = `@` + "\u200b" + `octocat &lt;b> &amp; \[link](target)`
	check := Check{ID: "c1", Name: prose, State: CheckPass, Note: "Worked. Worth a look: " + prose}
	v := Verdict{Overall: CheckPass, SkipBase: "not repeated", OwnerLoginWarning: prose, Checks: []Check{
		{ID: "c2", Name: "compared", State: CheckPass},
		{ID: "c3", Name: "compared", State: CheckPass, Leg: "base"},
		check,
	}}
	consoleCheck := Check{ID: "c4", Name: "console", State: CheckFail, Note: "Failed.", Receipts: []Receipt{{ConsoleTail: "```\n" + prose}}}
	for name, body := range map[string]string{
		"worth a look":  strings.Join(worthALookRows(commentRun, []Check{check}), "\n"),
		"base coverage": renderComment(commentRun, v, evidence{}, commentRun.CreatedAt),
		"login warning": renderComment(commentRun, Verdict{OwnerLoginWarning: prose}, evidence{}, commentRun.CreatedAt),
		"name fallback": checkPara(commentRun, Verdict{}, Check{Name: prose}, 80, 0),
		"fixed SHA":     fixedCommentBody(prose, true),
		"console tail":  bugCommentBody(commentRun, Verdict{}, bugComment{Check: consoleCheck, Number: 1}, "", "", true),
	} {
		if !strings.Contains(body, escaped) || strings.Contains(body, prose) {
			t.Errorf("%s exposed tester/PR prose:\n%s", name, body)
		}
	}
}

func TestPublicEvidenceCaptionEscapesCheckName(t *testing.T) {
	svc, _, runDir, _ := submitSvc(t)
	evidenceBug(t, svc)
	checks, err := svc.store.Checks("run_s1")
	is(t, "")(nil, err)
	const name = `@octocat <b> & [link](target)`
	checks[0].Name, checks[1].Name = name, name
	run, err := svc.store.Get("run_s1")
	is(t, "")(nil, err)
	v := Verdict{Overall: CheckFail, Checks: checks}
	items, _, err := svc.stageEvidence(ctx, run, v, runDir)
	is(t, "")(nil, err)
	var found bool
	for _, item := range items {
		if item.kind == kindBug {
			found = true
			body := renderComment(run, v, evidence{items: []evItem{item}}, run.CreatedAt)
			if strings.Contains(body, name) || !strings.Contains(body, `Bug 1 of 1: @`+"\u200b"+`octocat &lt;b> &amp; \[link](target)`) {
				t.Fatalf("public image caption exposed check name:\n%s", body)
			}
		}
	}
	if !found {
		t.Fatal("no comparison image caption was rendered")
	}
}

func TestTesterTextCannotCreateMarkdownBlocks(t *testing.T) {
	md := goldmark.New()
	for _, input := range []string{
		"# heading", "> quote", "---", "- item", "+ item", "* item", "1. item", "123456789. item", "1) item",
		"```go\nunclosed", "~~~~\nunclosed", "Heading\n===", "Heading\n---", "___", "***", "- - -", "  # indented",
	} {
		t.Run(input, func(t *testing.T) {
			source := []byte(mdTextLines(input))
			doc := md.Parser().Parse(text.NewReader(source))
			if err := ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
				if entering {
					switch node.Kind() {
					case ast.KindHeading, ast.KindBlockquote, ast.KindList, ast.KindFencedCodeBlock, ast.KindCodeBlock, ast.KindThematicBreak:
						t.Errorf("tester text created %s from %q", node.Kind(), source)
					}
				}
				return ast.WalkContinue, nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnclosedTesterFenceLeavesCommentNotesAndMarkerOutsideCode(t *testing.T) {
	check := Check{ID: "c1", Name: "menu", State: CheckFail, Note: "The menu stayed closed."}
	v := Verdict{Overall: CheckFail, Summary: "```\nunclosed tester fence", Checks: []Check{check}}
	for name, body := range map[string]string{
		"full":    renderComment(commentRun, v, evidence{}, commentRun.CreatedAt),
		"summary": summaryCommentBody(commentRun, v, commentRun.CreatedAt, true),
		"bug":     bugCommentBody(commentRun, v, bugComment{Check: check, Number: 1}, "", "", true, true),
	} {
		var rendered bytes.Buffer
		if err := goldmark.New(goldmark.WithRendererOptions(htmlrenderer.WithUnsafe())).Convert([]byte(body), &rendered); err != nil {
			t.Fatal(err)
		}
		html := rendered.String()
		if strings.Contains(html, "<pre>") || strings.Contains(html, "<code>") || !strings.Contains(html, "<details><summary>Full check notes</summary>") || !strings.Contains(html, "The menu stayed closed.") || !strings.Contains(html, "<!-- litmus:comment") {
			t.Errorf("%s fence consumed comment structure:\n%s", name, html)
		}
	}
}
