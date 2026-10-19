package triage

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/processenv"
	chatregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type diagnosticTestDriver struct {
	ports.ChatDriver
	policy ports.ChatDiagnosticPolicy
	start  func(ports.ChatStartConfig) (ports.ChatConversation, error)
}

func (*diagnosticTestDriver) Harness() domain.AgentHarness                   { return domain.HarnessCodex }
func (d *diagnosticTestDriver) DiagnosticPolicy() ports.ChatDiagnosticPolicy { return d.policy }
func (d *diagnosticTestDriver) Start(_ context.Context, cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
	return d.start(cfg)
}

func TestDiagnosticLaunchKeepsProviderHostsOutsideNormalReconciliation(t *testing.T) {
	catalog := catalogFixture(t)
	if err := catalog.Configure(context.Background(), []Choice{configuredCodexFixture()}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AO_SESSION_ID", "unrelated-normal-session")
	t.Setenv("GH_TOKEN", "publisher-only")
	t.Setenv("GITHUB_TOKEN", "publisher-only")
	t.Setenv("AO_GITHUB_TOKEN", "publisher-only")
	for _, name := range []string{"ao.db", "config.yaml", "running.json"} {
		if err := os.WriteFile(filepath.Join(catalog.dataDir, name), []byte("invalid daemon state"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	conv := fakeConversation()
	driver := &diagnosticTestDriver{policy: ports.ChatDiagnosticPolicy{ReadOnly: true, DisableToolNetwork: true}, start: func(cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
		if cfg.DataDir != filepath.Join(catalog.dataDir, "reports", "triage", "provider-hosts") || cfg.WorkspacePath != filepath.Join(catalog.dataDir, "reports", "triage", "runs", "test-run", "workspace") {
			t.Fatalf("provider uses normal session storage: %#v", cfg)
		}
		if cfg.ReadOnly || cfg.DisableNetwork || len(cfg.MCPServers) != 0 || cfg.Model != "gpt-6.1-sol" || cfg.Effort != "xhigh" {
			t.Fatal("normal settings or exact selection were changed")
		}
		for _, key := range []string{"AO_SESSION_ID", "GH_TOKEN", "GITHUB_TOKEN", "AO_GITHUB_TOKEN"} {
			if cfg.Env[key] != "" {
				t.Fatalf("diagnostic provider inherited %s", key)
			}
		}
		if !strings.Contains(cfg.SystemPrompt, "Keep the report and investigation separate") {
			t.Fatal("bundled reporting rules missing")
		}
		raw, err := os.ReadFile(filepath.Join(cfg.WorkspacePath, "diagnostics.json"))
		if err != nil || strings.Contains(string(raw), "ghp_") {
			t.Fatalf("private snapshot not scrubbed: %s, %v", raw, err)
		}
		return conv, nil
	}}
	run := Run{ID: "test-run", Request: StartRequest{Selection: Selection{Harness: "codex", Model: "gpt-6.1-sol", Effort: "xhigh", AccountID: "device-codex"}}, Evidence: []Evidence{{Text: "token=ghp_fake_credential_value"}}}
	got, err := startDiagnosticConversation(context.Background(), catalog.dataDir, catalog, chatregistry.New(driver), run)
	if err != nil || got != conv {
		t.Fatalf("independent launch = %v, %v", got, err)
	}
}

func TestDiagnosticRegistryAcceptsEveryExistingChatDriver(t *testing.T) {
	registry := chatregistry.Build(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	for _, harness := range registry.Harnesses() {
		if _, err := diagnosticDriver(registry, string(harness)); err != nil {
			t.Fatalf("registered %s was excluded by triage: %v", harness, err)
		}
	}
	if _, err := diagnosticDriver(registry, "amp"); !errors.Is(err, ports.ErrChatUnsupported) {
		t.Fatalf("missing conversation transport = %v", err)
	}
}

type diagnosticCapabilitiesConversation struct {
	*fakeTriageConversation
	caps ports.ChatCapabilities
}

func (c *diagnosticCapabilitiesConversation) Capabilities() ports.ChatCapabilities { return c.caps }

func TestDiagnosticNativeProfileIsOptionalAndDoesNotChangeNormalPermissions(t *testing.T) {
	catalog := catalogFixture(t)
	if err := catalog.Configure(context.Background(), []Choice{configuredCodexFixture()}); err != nil {
		t.Fatal(err)
	}
	// Hiding the optional interface models ordinary native ACP drivers.
	driver := struct{ ports.ChatDriver }{&diagnosticTestDriver{start: func(cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
		if cfg.ReadOnly || cfg.DisableNetwork || ports.NormalizePermissionMode(cfg.Permissions) != ports.PermissionModeDefault || len(cfg.MCPServers) != 0 {
			t.Fatal("ordinary driver was assigned unsupported restrictions or bypass permissions")
		}
		// Saving an interrupted report does not require provider resume.
		return &diagnosticCapabilitiesConversation{fakeTriageConversation: fakeConversation(), caps: ports.ChatCapabilities{
			ports.ChatCapabilityStreaming: true, ports.ChatCapabilityApprovals: true, ports.ChatCapabilityInterrupt: true,
		}}, nil
	}}}
	run := Run{ID: "ordinary-agent", Request: StartRequest{Selection: Selection{Harness: "codex", Model: "gpt-6.1-sol", AccountID: "device-codex"}}}
	if _, err := startDiagnosticConversation(context.Background(), catalog.dataDir, catalog, chatregistry.New(driver), run); err != nil {
		t.Fatalf("ordinary registered driver could not triage: %v", err)
	}
}

func TestDiagnosticNativeNoApprovalModeDoesNotForceBypass(t *testing.T) {
	catalog := catalogFixture(t)
	if err := catalog.Configure(context.Background(), []Choice{configuredCodexFixture()}); err != nil {
		t.Fatal(err)
	}
	conv := fakeConversation()
	driver := &diagnosticTestDriver{start: func(cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
		if cfg.Permissions == ports.PermissionModeBypassPermissions {
			t.Fatal("native no-popup mode was replaced with bypass")
		}
		return &diagnosticCapabilitiesConversation{fakeTriageConversation: conv, caps: ports.ChatCapabilities{
			ports.ChatCapabilityStreaming: true, ports.ChatCapabilityInterrupt: true,
		}}, nil
	}}
	run := Run{ID: "no-approval-channel", Request: StartRequest{Selection: Selection{Harness: "codex", Model: "gpt-6.1-sol", AccountID: "device-codex"}}}
	_, err := startDiagnosticConversation(context.Background(), catalog.dataDir, catalog, chatregistry.New(driver), run)
	if err != nil || conv.turns.Load() != 0 {
		t.Fatalf("native permission admission = %v, turns = %d", err, conv.turns.Load())
	}
	select {
	case <-conv.terminated:
		t.Fatal("native provider was incorrectly terminated")
	default:
	}
}

type namedDiagnosticTestDriver struct {
	*diagnosticTestDriver
	harness domain.AgentHarness
}

func (d namedDiagnosticTestDriver) Harness() domain.AgentHarness { return d.harness }

func TestDiagnosticLaunchPreservesConfiguredPiAndUnrealModes(t *testing.T) {
	for _, test := range []struct {
		harness    string
		permission domain.PermissionMode
	}{{"pi", domain.PermissionModeDefault}, {"unreal-agent", domain.PermissionModeBypassPermissions}, {"unreal-agent", domain.PermissionModeDefault}} {
		t.Run(test.harness+"/"+string(test.permission), func(t *testing.T) {
			catalog := catalogFixture(t)
			catalog.SetSupport(func(string) (bool, string) { return true, "" })
			catalog.resolve[test.harness] = func(context.Context) (string, error) { return "/fixture/selected", nil }
			if err := catalog.Configure(context.Background(), []Choice{{Harness: test.harness, Permissions: test.permission, Models: []ModelChoice{{ID: "", Default: true}}}}); err != nil {
				t.Fatal(err)
			}
			driver := namedDiagnosticTestDriver{harness: domain.AgentHarness(test.harness), diagnosticTestDriver: &diagnosticTestDriver{start: func(cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
				if cfg.Permissions != test.permission || cfg.Model != "" || cfg.ReadOnly != (test.harness == "unreal-agent") {
					t.Fatalf("configured mode changed: %#v", cfg)
				}
				return &diagnosticCapabilitiesConversation{fakeTriageConversation: fakeConversation(), caps: ports.ChatCapabilities{ports.ChatCapabilityStreaming: true, ports.ChatCapabilityInterrupt: true}}, nil
			}}}
			run := Run{ID: "normal-mode", Request: StartRequest{Selection: Selection{Harness: test.harness}}}
			conv, err := startDiagnosticConversation(context.Background(), catalog.dataDir, catalog, chatregistry.New(driver), run)
			if err != nil {
				t.Fatal(err)
			}
			_ = conv.Close()
		})
	}
}

func TestDiagnosticEnvUsesSelectedAccountAndDropsDelegation(t *testing.T) {
	got := diagnosticEnv([]string{"PATH=/bin", "AO_SESSION_ID=normal", "AO_MCP_TOKEN=secret", "GH_TOKEN=secret", "ANTHROPIC_API_KEY=native"}, map[string]string{"ANTHROPIC_API_KEY": "", "CLAUDE_CONFIG_DIR": "/selected/account"})
	if got["PATH"] != "/bin" || got["CLAUDE_CONFIG_DIR"] != "/selected/account" || got["ANTHROPIC_API_KEY"] != "" {
		t.Fatal("selected account environment was not preserved")
	}
	if len(got) != 6 {
		t.Fatalf("unexpected inherited variables: %v", got)
	}
}

func TestDiagnosticEnvClearsCredentialsAfterDriverEnvironmentMerge(t *testing.T) {
	t.Setenv("AO_MCP_TOKEN", "private-delegation-value")
	t.Setenv("GH_TOKEN", "private-publisher-value")
	for _, entry := range processenv.Merge(diagnosticEnv(os.Environ(), nil)) {
		if strings.Contains(entry, "private-delegation-value") || strings.Contains(entry, "private-publisher-value") {
			t.Fatal("driver environment merge restored a cleared credential")
		}
	}
}

func TestDiagnosticRuntimeDoesNotSubstituteProviderOnFailure(t *testing.T) {
	catalog := catalogFixture(t)
	if err := catalog.Configure(context.Background(), []Choice{configuredCodexFixture()}); err != nil {
		t.Fatal(err)
	}
	want := errors.New("selected provider failed")
	driver := &diagnosticTestDriver{policy: ports.ChatDiagnosticPolicy{ReadOnly: true, DisableToolNetwork: true}, start: func(ports.ChatStartConfig) (ports.ChatConversation, error) { return nil, want }}
	_, err := startDiagnosticConversation(context.Background(), catalog.dataDir, catalog, chatregistry.New(driver), Run{ID: "failed-run", Request: StartRequest{Selection: Selection{Harness: "codex", Model: "gpt-6.1-sol", AccountID: "device-codex"}}})
	if !errors.Is(err, want) {
		t.Fatalf("provider failure replaced: %v", err)
	}
}

func TestDiagnosticReconciliationIsPrivateBoundedAndPropagatesFailure(t *testing.T) {
	dataDir := t.TempDir()
	want := errors.New("owned provider could not be stopped")
	called := false
	err := reconcileDiagnosticHosts(context.Background(), dataDir, func(ctx context.Context, root string, keep map[string]struct{}) error {
		called = true
		if root != filepath.Join(dataDir, "reports", "triage", "provider-hosts") || len(keep) != 0 {
			t.Fatalf("reconciliation escaped diagnostic ownership: %q %#v", root, keep)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second {
			t.Fatal("diagnostic reconciliation has no bounded deadline")
		}
		return want
	})
	if !called || !errors.Is(err, want) {
		t.Fatalf("cleanup failure was hidden: %v", err)
	}
}
