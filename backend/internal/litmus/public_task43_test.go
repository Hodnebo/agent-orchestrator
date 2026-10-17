package litmus

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommentNeutralizesGitHubAutolinks(t *testing.T) {
	sha40 := "0123456789abcdef0123456789abcdef01234567"
	sha64 := strings.Repeat("a", 64)
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "issue number", in: "See #123 now.", want: "See `#123` now."},
		{name: "GH issue number", in: "See GH-123 now.", want: "See `GH-123` now."},
		{name: "owner repo issue", in: "See owner/repo#123 now.", want: "See `owner/repo#123` now."},
		{name: "commit URL", in: "https://github.com/owner/repo/commit/abcdef0", want: "`https://github.com/owner/repo/commit/abcdef0`"},
		{name: "issue URL", in: "https://github.com/owner/repo/issues/123", want: "`https://github.com/owner/repo/issues/123`"},
		{name: "pull request URL", in: "https://github.com/owner/repo/pull/123", want: "`https://github.com/owner/repo/pull/123`"},
		{name: "owner repo commit", in: "owner/repo@abcdef0", want: "`owner/repo@abcdef0`"},
		{name: "user commit", in: "octocat@abcdef0", want: "`octocat@abcdef0`"},
		{name: "commit and mention", in: "octocat@abcdef0 and @octocat", want: "`octocat@abcdef0` and @\u200boctocat"},
		{name: "seven character SHA", in: "abcdef0", want: "`abcdef0`"},
		{name: "forty character SHA", in: sha40, want: "`" + sha40 + "`"},
		{name: "long SHA", in: sha64, want: "`" + sha64 + "`"},
		{name: "URL punctuation", in: "See https://github.com/owner/repo/issues/123.", want: "See `https://github.com/owner/repo/issues/123`."},
		{name: "existing code", in: "Keep `#123 GH-123 abcdef0` as code.", want: "Keep `#123 GH-123 abcdef0` as code."},
		{name: "existing commit code", in: "`octocat@abcdef0` and @octocat", want: "`octocat@abcdef0` and @\u200boctocat"},
		{name: "double backtick code", in: "``owner/repo@abcdef0`` and @octocat", want: "``owner/repo@abcdef0`` and @\u200boctocat"},
		{name: "sha inside a word", in: "xabcdef0y", want: "xabcdef0y"},
		{name: "ordinary URL", in: "https://example.com/abcdef0", want: "https://example.com/abcdef0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mdText(tt.in, math.MaxInt); got != tt.want {
				t.Fatalf("mdText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPublicControlDetailUsesCauseAndRecordedDialogText(t *testing.T) {
	for _, tt := range []struct {
		name, cause, detail, want string
	}{
		{name: "native dialog", cause: "native_dialog", detail: `blocked by a native dialog: "Import #42?" buttons [GH-7] [abcdef0]`, want: `The app is blocked by a native dialog with title "Import #42?" and buttons GH-7, abcdef0.`},
		{name: "native dialog without recording", cause: "native_dialog", detail: "private native window error: /tmp/private", want: "The app is blocked by a native dialog."},
		{name: "app exited", cause: "app_exited", detail: "the head app exited (pid 41): /private/path", want: "The app exited."},
		{name: "app unresponsive", cause: "app_unresponsive", detail: "main process alive; probe failed: secret", want: "The app is not responding."},
		{name: "browser control unusable", cause: "browser_control_unusable", detail: "cannot diagnose the head app: secret", want: "Browser control is unavailable."},
		{name: "unknown cause", cause: "future_cause", detail: "private diagnostic", want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			control := LegControl{Cause: tt.cause, Detail: tt.detail}
			if got := publicControlDetail(control); got != tt.want {
				t.Fatalf("publicControlDetail() = %q, want %q", got, tt.want)
			}
			if tt.want != "" && strings.Contains(tt.want, tt.detail) {
				t.Fatalf("test fixture expected value includes raw Detail %q", tt.detail)
			}
		})
	}
}

func TestPublicControlDialogTextUsesCommentEscaping(t *testing.T) {
	control := LegControl{
		Cause:  "native_dialog",
		Detail: `blocked by a native dialog: "Import #42?" buttons [GH-7] [abcdef0]`,
	}
	want := `The app is blocked by a native dialog with title "Import ` + "`#42`" + `?" and buttons ` + "`GH-7`, `abcdef0`."
	if got := mdText(publicControlDetail(control), math.MaxInt); got != want {
		t.Fatalf("escaped public detail = %q, want %q", got, want)
	}
}

func TestPublicControlDetailPersistsSeparatelyFromRawDetail(t *testing.T) {
	control := LegControl{
		State:        "lost",
		Cause:        "browser_control_unusable",
		Detail:       "cannot diagnose the head app: private error at /tmp/secret",
		PublicDetail: "Browser control is unavailable.",
	}
	data, err := json.Marshal(control)
	if err != nil {
		t.Fatal(err)
	}
	var stored LegControl
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.PublicDetail != control.PublicDetail || stored.Detail != control.Detail {
		t.Fatalf("round trip = %+v, want public and private details retained", stored)
	}
}

func TestControlRawDetailsStayOutOfPublicVerdictAndComments(t *testing.T) {
	for _, tt := range []struct {
		cause, detail, public string
	}{
		{"app_exited", "private exit diagnostic at /tmp/exit-secret", "The PR build's app exited during action 23."},
		{"app_unresponsive", "private probe diagnostic at /tmp/hang-secret", "The PR build's app is not responding during action 23."},
		{"browser_control_unusable", "private browser diagnostic at /tmp/control-secret", "head app uncontrollable since action 23: browser control is unavailable; unreached claim checks: settings opens"},
	} {
		t.Run(tt.cause, func(t *testing.T) {
			checks := []Check{{ID: "c1", Name: "settings opens", Claim: true, State: CheckSkipped}}
			control := &LegControl{State: "lost", Since: 23, Cause: tt.cause, Detail: tt.detail}
			v, run, runDir := closeHeadControlArtifacts(t, control, checks, false, "")
			if run.Legs[0].Control == nil || run.Legs[0].Control.Detail != tt.detail {
				t.Fatalf("private diagnostic was not retained locally: %+v", run.Legs[0].Control)
			}
			if tt.cause == "browser_control_unusable" && (len(v.Reasons) != 1 || v.Reasons[0] != tt.public) {
				t.Fatalf("public control reason = %q, want %q", v.Reasons, tt.public)
			}
			var outputs []string
			outputs = append(outputs, v.Reasons...)
			outputs = append(outputs, run.Gaps...)
			for _, name := range []string{"verdict.md", "comment.md"} {
				body, err := os.ReadFile(filepath.Join(runDir, name))
				if err != nil {
					t.Fatal(err)
				}
				outputs = append(outputs, string(body))
			}
			for _, output := range outputs {
				if strings.Contains(output, tt.detail) || strings.Contains(output, "/tmp/") {
					t.Fatalf("raw control diagnostic reached public output: %s", output)
				}
			}
			if tt.cause != "browser_control_unusable" && (len(run.Gaps) != 1 || run.Gaps[0] != tt.public) {
				t.Fatalf("public control gap = %q, want %q", run.Gaps, tt.public)
			}
		})
	}
}

func TestNativeDialogTitleAndButtonsRenderThroughCommentEscaping(t *testing.T) {
	checks := []Check{{ID: "c1", Name: "settings opens", Claim: true, State: CheckSkipped}}
	control := &LegControl{
		State:  "lost",
		Since:  23,
		Cause:  "native_dialog",
		Detail: `blocked by a native dialog: "Import #42?" buttons [GH-7] [abcdef0]`,
	}
	_, _, runDir := closeHeadControlArtifacts(t, control, checks, false, "")
	body, err := os.ReadFile(filepath.Join(runDir, "comment.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"`#42`", "`GH-7`", "`abcdef0`"} {
		if !strings.Contains(string(body), ref) {
			t.Fatalf("comment did not code-span recorded dialog text %q: %s", ref, body)
		}
	}
}
