package litmus

import (
	"strings"
	"testing"
)

func TestTesterSummaryEscapesPublicMarkdownAndKeepsLineBreaks(t *testing.T) {
	summary := "First @octocat <img src=x> & [link](x)\n\nSecond @team."
	want := "Tester summary:\n\nFirst @\u200boctocat &lt;img src=x> &amp; \\[link](x)\n\nSecond @\u200bteam."

	checks := []Check{{ID: "c1", Name: "summary check", State: CheckPass}}
	v := Verdict{Overall: CheckPass, Summary: summary, Checks: checks}
	bug := bugComment{Check: checks[0], Number: 1, Total: 1}
	for name, body := range map[string]string{
		"full comment":    renderComment(commentRun, v, evidence{}, commentRun.CreatedAt),
		"summary comment": summaryCommentBody(commentRun, v, commentRun.CreatedAt, false),
		"bug comment":     bugCommentBody(commentRun, v, bug, "", "", false, true),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("%s did not escape or preserve the tester summary:\n%s", name, body)
		}
		if strings.Contains(body, " <img") || strings.Contains(body, " [link](x)") || strings.Contains(body, "@octocat") {
			t.Errorf("%s exposed raw public markdown or mention syntax:\n%s", name, body)
		}
	}
}
