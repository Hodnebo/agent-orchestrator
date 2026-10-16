package workerexec

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

func TestTaskNamingCredentialsAreIsolatedForEveryHarness(t *testing.T) {
	adapter := filepath.Join(t.TempDir(), "adapter")
	if err := os.WriteFile(adapter, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := defaultClaudeACPAdapter
	defaultClaudeACPAdapter = &claudeACPAdapter{lookPath: func(string) (string, error) { return adapter, nil }}
	t.Cleanup(func() { defaultClaudeACPAdapter = old })
	t.Setenv("AO_CLOUD_WORKER_TOKEN_FILE", "/private/worker-token")
	t.Setenv("ANTHROPIC_API_KEY", "inherited-provider-secret")
	t.Setenv("UNRELATED_SECRET", "other-secret")
	for _, tc := range []struct{ harness, kind, key, driver string }{
		{"claude-code", "oauth_token", "CLAUDE_CODE_OAUTH_TOKEN", "claude-code"},
		{"cursor", "api_key", "CURSOR_API_KEY", "cursor"},
		{"opencode", "openai_api_key", "OPENAI_API_KEY", "opencode-v2"},
		{"codex", "auth_json", "CODEX_HOME", "codex"},
	} {
		t.Run(tc.harness, func(t *testing.T) {
			b := HarnessBuilder{DataDir: t.TempDir(), Env: map[string]string{"PROJECT_SECRET": "project"}}
			cfg, cleanup, err := b.BuildTaskNaming(context.Background(), worker.LaunchContext{Harness: tc.harness, Model: "selected-model", SystemPrompt: "project rules"}, worker.CredentialResponse{Provider: tc.harness, CredentialType: tc.kind, Secret: "selected-secret"})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if cfg.Harness != tc.driver || cfg.Model != "selected-model" || cfg.Env[tc.key] == "" {
				t.Fatalf("configuration = %+v", cfg)
			}
			for _, key := range []string{"AO_CLOUD_WORKER_TOKEN_FILE", "UNRELATED_SECRET", "PROJECT_SECRET"} {
				if cfg.Env[key] != "" {
					t.Fatalf("leaked %s", key)
				}
			}
			if cfg.Env["ANTHROPIC_API_KEY"] != "" {
				t.Fatal("inherited another provider's credential")
			}
			if cfg.WorkspacePath == "" || !filepath.IsAbs(cfg.WorkspacePath) || cfg.Env["HOME"] == os.Getenv("HOME") {
				t.Fatal("not a disposable workspace/home")
			}
			if tc.harness == "codex" {
				body, err := os.ReadFile(filepath.Join(cfg.Env["CODEX_HOME"], "auth.json"))
				if err != nil || string(body) != "selected-secret" {
					t.Fatalf("auth = %q, %v", body, err)
				}
			}
			cleanup()
			if _, err := os.Stat(cfg.DataDir); !os.IsNotExist(err) {
				t.Fatal("disposable credentials retained")
			}
		})
	}
}

func TestTaskNamingCodexReusesCredentialLogin(t *testing.T) {
	called := false
	b := HarnessBuilder{DataDir: t.TempDir(), CodexLogin: func(binary, home, kind, secret string) error {
		called = binary == "codex" && kind == "api_key" && secret == "selected-secret" && filepath.IsAbs(home)
		return nil
	}}
	_, cleanup, err := b.BuildTaskNaming(context.Background(), worker.LaunchContext{Harness: "codex"}, worker.CredentialResponse{Provider: "codex", CredentialType: "api_key", Secret: "selected-secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !called {
		t.Fatal("existing credential login was not used")
	}
}

func TestTaskNamingRejectsUnsupportedAndMismatchedCredentialsBeforeCreatingFiles(t *testing.T) {
	for _, tc := range []struct{ harness, provider string }{{"missing", "missing"}, {"codex", "cursor"}} {
		data := t.TempDir()
		_, cleanup, err := (HarnessBuilder{DataDir: data}).BuildTaskNaming(context.Background(), worker.LaunchContext{Harness: tc.harness}, worker.CredentialResponse{Provider: tc.provider, CredentialType: "api_key", Secret: "secret"})
		if err == nil || cleanup != nil {
			t.Fatal("invalid provider accepted")
		}
		entries, err := os.ReadDir(data)
		if err != nil || len(entries) != 0 {
			t.Fatalf("invalid provider wrote files: %v, %v", entries, err)
		}
	}
}
