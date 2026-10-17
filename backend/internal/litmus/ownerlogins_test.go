package litmus

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func codexAuthForTest(accountID, email, access string) string {
	return `{"tokens":{"account_id":"` + accountID + `","email":"` + email + `","id_token":"` + access + `-id","access_token":"` + access + `","refresh_token":"` + access + `-refresh"}}`
}

func writeOwnerLoginTestFile(t *testing.T, path, value string) {
	t.Helper()
	is(t, "")(nil, os.MkdirAll(filepath.Dir(path), 0o700))
	is(t, "")(nil, os.WriteFile(path, []byte(value), 0o600))
}

func TestOwnerLoginFingerprintsAndStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("GH_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("OPENCODE_DATA_DIR", "")
	t.Setenv("XDG_DATA_HOME", "")

	codexPath := filepath.Join(home, ".codex", "auth.json")
	claudePath := filepath.Join(home, ".claude.json")
	ghPath := filepath.Join(home, ".config", "gh", "hosts.yml")
	openCodePath := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	writeOwnerLoginTestFile(t, codexPath, codexAuthForTest("codex-1", "one@example.com", "secret-a"))
	writeOwnerLoginTestFile(t, claudePath, `{"oauthAccount":{"accountUuid":"claude-1","emailAddress":"one@example.com"},"unrelated":"secret-a"}`)
	writeOwnerLoginTestFile(t, ghPath, "github.com:\n  user: octocat\n  oauth_token: secret-a\n  users:\n    other:\n      oauth_token: secret-b\n")
	writeOwnerLoginTestFile(t, openCodePath, `{"openai":{"type":"oauth","accountId":"open-1","access":"secret-a","refresh":"secret-b"},"anthropic":{"type":"api","key":"secret-c"}}`)

	svc, _ := newSvc(t)
	run, err := svc.claim("run_s1", "s1", "o/r", 1, CommentOff, EvidenceOff)
	is(t, "")(nil, err)
	var before ownerLogins
	is(t, "")(nil, svc.store.read(run.ID, ownerLoginsFile, &before))
	if before.Version != ownerLoginsVersion || len(before.CodexIdentity) != 64 ||
		len(before.ClaudeIdentity) != 64 || len(before.GitHubIdentity) != 64 || len(before.OpenCodeIdentity) != 64 {
		t.Fatalf("incomplete baseline: %+v", before)
	}
	baselineFile, err := os.ReadFile(filepath.Join(svc.store.root, run.ID, ownerLoginsFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, secretOrIdentity := range []string{"secret-a", "secret-b", "secret-c", "one@example.com", "codex-1", "claude-1", "open-1", "octocat"} {
		if strings.Contains(string(baselineFile), secretOrIdentity) {
			t.Fatalf("baseline exposed %q: %s", secretOrIdentity, baselineFile)
		}
	}

	// A refresh rewrites every credential without changing who owns it.
	writeOwnerLoginTestFile(t, codexPath, codexAuthForTest("codex-1", "renamed@example.com", "secret-refreshed"))
	writeOwnerLoginTestFile(t, claudePath, `{"oauthAccount":{"accountUuid":"claude-1","emailAddress":"renamed@example.com"},"unrelated":"secret-refreshed"}`)
	writeOwnerLoginTestFile(t, ghPath, "github.com:\n  users:\n    other:\n      oauth_token: secret-new\n  oauth_token: secret-refreshed\n  user: octocat\n")
	writeOwnerLoginTestFile(t, openCodePath, `{"anthropic":{"type":"api","key":"secret-refreshed"},"openai":{"type":"oauth","accountId":"open-1","access":"secret-refreshed","refresh":"secret-new"}}`)
	if got := readOwnerLogins(context.Background()); got != before {
		t.Fatalf("refresh changed identities: got %+v, want %+v", got, before)
	}
	if got := svc.ownerLoginStatus(ctx, run); got != "owner logins: unchanged" {
		t.Fatalf("refresh status = %q", got)
	}

	// A different account on the same provider warns without blocking the run.
	writeOwnerLoginTestFile(t, codexPath, codexAuthForTest("codex-2", "two@example.com", "secret-new"))
	writeOwnerLoginTestFile(t, claudePath, `{"oauthAccount":{"accountUuid":"claude-2","emailAddress":"two@example.com"}}`)
	writeOwnerLoginTestFile(t, ghPath, "github.com:\n  user: new-user\n  oauth_token: secret-new\n")
	writeOwnerLoginTestFile(t, openCodePath, `{"openai":{"type":"oauth","accountId":"open-2","access":"secret-new"},"anthropic":{"type":"api","key":"secret-new"}}`)
	wantChanges := []string{
		"owner login changed: Codex account",
		"owner login changed: Claude account",
		"owner login changed: GitHub account",
		"owner login changed: OpenCode account",
	}
	if got := ownerLoginChangeReasons(before, readOwnerLogins(context.Background())); !reflect.DeepEqual(got, wantChanges) {
		t.Fatalf("change reasons = %#v, want %#v", got, wantChanges)
	}
	// Retrying intake must keep the original baseline.
	is(t, "")(svc.claim("run_s1", "s1", "o/r", 1, CommentOff, EvidenceOff))
	if got := svc.warnOwnerLogin(ctx, run.SessionID); got != ownerLoginWarningPrefix+"Codex account, Claude account, GitHub account, OpenCode account" {
		t.Fatalf("change lost on retry: %s", got)
	}

	is(t, "")(svc.Cleanup(ctx, "s1"))
	log, err := os.ReadFile(filepath.Join(svc.store.root, run.ID, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, reason := range wantChanges {
		if !strings.Contains(string(log), strings.TrimPrefix(reason, ownerLoginChanged)) {
			t.Fatalf("close log missing %q: %s", reason, log)
		}
	}
	for _, secretOrIdentity := range []string{"secret-new", "two@example.com", "codex-2", "claude-2", "open-2", "new-user"} {
		if strings.Contains(string(log), secretOrIdentity) {
			t.Fatalf("close log exposed %q: %s", secretOrIdentity, log)
		}
	}
}

func TestOwnerLoginLogoutAndMissingAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("GH_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("OPENCODE_DATA_DIR", "")
	t.Setenv("XDG_DATA_HOME", "")
	codexPath := filepath.Join(home, ".codex", "auth.json")
	claudePath := filepath.Join(home, ".claude.json")
	ghPath := filepath.Join(home, ".config", "gh", "hosts.yml")
	openCodePath := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	writeOwnerLoginTestFile(t, codexPath, codexAuthForTest("codex-1", "one@example.com", "secret"))
	writeOwnerLoginTestFile(t, claudePath, `{"oauthAccount":{"accountUuid":"claude-1"}}`)
	writeOwnerLoginTestFile(t, ghPath, "github.com:\n  user: octocat\n  oauth_token: secret\n")
	writeOwnerLoginTestFile(t, openCodePath, `{"openai":{"type":"oauth","accountId":"open-1","access":"secret"}}`)
	before := readOwnerLogins(context.Background())
	writeOwnerLoginTestFile(t, codexPath, `{"tokens":{}}`)
	writeOwnerLoginTestFile(t, claudePath, `{"userID":"install-id","oauthAccount":{}}`)
	writeOwnerLoginTestFile(t, ghPath, "github.com:\n  users:\n    octocat:\n      oauth_token: secret\n")
	writeOwnerLoginTestFile(t, openCodePath, `{}`)
	after := readOwnerLogins(context.Background())
	if after.CodexIdentity != "" || after.ClaudeIdentity != "" || after.GitHubIdentity != "" || after.OpenCodeIdentity != "" {
		t.Fatalf("missing accounts retained identities: %+v", after)
	}
	if len(ownerLoginChangeReasons(before, after)) != 4 {
		t.Fatalf("logout changes = %#v", ownerLoginChangeReasons(before, after))
	}
}

func TestOwnerLoginCustomHomes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "custom-codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "custom-claude"))
	t.Setenv("GH_CONFIG_DIR", filepath.Join(home, "custom-gh"))
	t.Setenv("OPENCODE_DATA_DIR", filepath.Join(home, "custom-opencode"))
	writeOwnerLoginTestFile(t, filepath.Join(home, "custom-codex", "auth.json"), codexAuthForTest("custom-codex", "custom@example.com", "secret"))
	writeOwnerLoginTestFile(t, filepath.Join(home, "custom-claude", ".claude.json"), `{"oauthAccount":{"accountUuid":"custom-claude"}}`)
	writeOwnerLoginTestFile(t, filepath.Join(home, "custom-gh", "hosts.yml"), "github.com:\n  user: custom-gh\n")
	writeOwnerLoginTestFile(t, filepath.Join(home, "custom-opencode", "auth.json"), `{"openai":{"type":"oauth","accountId":"custom-open"}}`)
	before := readOwnerLogins(context.Background())
	if before.CodexIdentity == "" || before.ClaudeIdentity == "" || before.GitHubIdentity == "" || before.OpenCodeIdentity == "" {
		t.Fatalf("custom homes were ignored: %+v", before)
	}
	writeOwnerLoginTestFile(t, filepath.Join(home, ".codex", "auth.json"), codexAuthForTest("default", "default@example.com", "secret"))
	writeOwnerLoginTestFile(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"accountUuid":"default"}}`)
	writeOwnerLoginTestFile(t, filepath.Join(home, ".config", "gh", "hosts.yml"), "github.com:\n  user: default\n")
	writeOwnerLoginTestFile(t, filepath.Join(home, ".local", "share", "opencode", "auth.json"), `{"openai":{"type":"oauth","accountId":"default"}}`)
	if got := readOwnerLogins(context.Background()); got != before {
		t.Fatalf("default home overrode custom paths: %+v", got)
	}
}

func TestOwnerLoginLegacyBaselineIsUnverified(t *testing.T) {
	before := ownerLogins{Version: 2}
	after := ownerLogins{Version: ownerLoginsVersion, CodexIdentity: "new"}
	if got := ownerLoginChangeReasons(before, after); len(got) != 0 {
		t.Fatalf("legacy baseline cannot be compared: %#v", got)
	}
}

func TestOwnerLoginEmailFallback(t *testing.T) {
	home := t.TempDir()
	codexPath := filepath.Join(home, "codex.json")
	claudePath := filepath.Join(home, "claude.json")
	writeOwnerLoginTestFile(t, codexPath, `{"email":"one@example.com","tokens":{"id_token":"secret-a"}}`)
	writeOwnerLoginTestFile(t, claudePath, `{"oauthAccount":{"emailAddress":"one@example.com"}}`)
	codexBefore, claudeBefore := codexIdentity(context.Background(), codexPath), claudeIdentity(claudePath)
	if codexBefore == "" || claudeBefore == "" {
		t.Fatal("email-only accounts were not fingerprinted")
	}
	writeOwnerLoginTestFile(t, codexPath, `{"email":"two@example.com","tokens":{"id_token":"secret-b"}}`)
	writeOwnerLoginTestFile(t, claudePath, `{"oauthAccount":{"emailAddress":"two@example.com"}}`)
	if codexIdentity(context.Background(), codexPath) == codexBefore || claudeIdentity(claudePath) == claudeBefore {
		t.Fatal("email-only account switch was missed")
	}
}

func TestOwnerLoginChangeWarnsAndKeepsRunOpen(t *testing.T) {
	for _, result := range []CheckState{CheckSkipped, CheckFail} {
		t.Run(string(result), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("CODEX_HOME", "")
			path := filepath.Join(os.Getenv("HOME"), ".codex", "auth.json")
			writeOwnerLoginTestFile(t, path, codexAuthForTest("before", "one@example.com", "secret-before"))
			svc, _ := newSvc(t)
			run, err := svc.claim("run_s1", "s1", "o/r", 1, CommentOff, EvidenceOff)
			is(t, "")(nil, err)
			is(t, "")(nil, svc.store.SaveChecks(run.ID, []Check{{ID: "c1", Name: "claim", Claim: true, State: CheckRunning}}))
			writeOwnerLoginTestFile(t, path, codexAuthForTest("after", "two@example.com", "secret-after"))
			// A skipped check can close without evidence. A failed check below is saved
			// directly because its screenshot evidence is unrelated to login detection.
			is(t, "")(svc.CheckEnd(ctx, "s1", "c1", CheckSkipped, "unreachable", false, 0))
			current, err := svc.store.Get(run.ID)
			if err != nil || current.State.Closed() || current.Error != "" {
				t.Fatalf("login change stopped run: %+v, %v", current, err)
			}
			if result == CheckFail {
				is(t, "")(nil, svc.store.SaveChecks(run.ID, []Check{{ID: "c1", Name: "claim", Claim: true, State: CheckFail}}))
			}
			// Token refresh and a repeated check must not duplicate the warning.
			writeOwnerLoginTestFile(t, path, codexAuthForTest("after", "two@example.com", "secret-refreshed"))
			warning := svc.warnOwnerLogin(ctx, "s1")
			if warning != ownerLoginWarningPrefix+"Codex account" {
				t.Fatalf("warning = %q", warning)
			}
			// Even switching back retains the warning for the run.
			writeOwnerLoginTestFile(t, path, codexAuthForTest("before", "one@example.com", "secret-original"))
			var v Verdict
			if result == CheckFail {
				v, err = svc.SubmitBlocked(ctx, "s1", "test stopped")
			} else {
				addSkippedRisks(t, svc, run.ID)
				v, err = svc.Submit(ctx, "s1", "", "")
			}
			if result == CheckSkipped && v.Overall != CheckUntested {
				t.Fatalf("skipped claim should stay untested: %+v", v)
			}
			is(t, "")(nil, err)
			if result == CheckFail && v.Overall != CheckFail {
				t.Fatalf("failed claim was replaced: %+v", v)
			}
			if v.StoppedEarly == warning || strings.Contains(strings.Join(v.Reasons, " "), "Owner login") {
				t.Fatalf("warning changed verdict: %+v", v)
			}
			for _, file := range []string{"verdict.md", "comment.md", "run.log"} {
				data, err := os.ReadFile(filepath.Join(svc.store.root, run.ID, file))
				if err != nil || strings.Count(string(data), warning) != 1 || strings.Contains(string(data), "secret-") || strings.Contains(string(data), "two@example.com") {
					t.Fatalf("%s = %s, %v", file, data, err)
				}
			}
		})
	}
}
