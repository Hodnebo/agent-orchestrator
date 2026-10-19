package claudeacp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestDiagnosticSessionMetaUsesNativeSDKToolRestrictions(t *testing.T) {
	for _, permission := range []ports.PermissionMode{
		ports.PermissionModeDefault, ports.PermissionModeAuto,
		ports.PermissionModeAcceptEdits, ports.PermissionModeBypassPermissions,
	} {
		t.Run(string(permission), func(t *testing.T) {
			encoded, err := json.Marshal(claudeSessionMeta(acpdriver.LaunchConfig{
				ReadOnly: true, DisableNetwork: true, Permissions: permission,
				SystemPrompt: "Standing diagnostic instructions", Model: "sonnet",
			}))
			if err != nil {
				t.Fatal(err)
			}
			var meta struct {
				ClaudeCode struct {
					Options struct {
						Tools           []string       `json:"tools"`
						AllowedTools    []string       `json:"allowedTools"`
						DisallowedTools []string       `json:"disallowedTools"`
						SettingSources  []string       `json:"settingSources"`
						StrictMCPConfig bool           `json:"strictMcpConfig"`
						MCPServers      map[string]any `json:"mcpServers"`
						Skills          []string       `json:"skills"`
						Settings        struct {
							DisableAllHooks bool `json:"disableAllHooks"`
						} `json:"settings"`
					} `json:"options"`
				} `json:"claudeCode"`
			}
			if err := json.Unmarshal(encoded, &meta); err != nil {
				t.Fatal(err)
			}
			options := meta.ClaudeCode.Options
			reads := []string{"Read", "Glob", "Grep"}
			if !reflect.DeepEqual(options.Tools, reads) || !reflect.DeepEqual(options.AllowedTools, reads) {
				t.Fatalf("read tools = %v, allowed tools = %v", options.Tools, options.AllowedTools)
			}
			denied := []string{"Bash", "Write", "Edit", "NotebookEdit", "Agent", "Task", "Skill", "WebFetch", "WebSearch", "mcp__*"}
			if !reflect.DeepEqual(options.DisallowedTools, denied) {
				t.Fatalf("hard denials = %v", options.DisallowedTools)
			}
			if options.SettingSources == nil || len(options.SettingSources) != 0 ||
				!options.StrictMCPConfig || options.MCPServers == nil || len(options.MCPServers) != 0 ||
				options.Skills == nil || len(options.Skills) != 0 || !options.Settings.DisableAllHooks {
				t.Fatalf("inherited configuration was not disabled: %s", encoded)
			}
		})
	}
}

func TestDiagnosticPolicyIsAppliedWithoutStandingPrompt(t *testing.T) {
	t.Setenv(claudeTodoToolsEnv, "1")
	for _, cfg := range []acpdriver.LaunchConfig{{ReadOnly: true}, {DisableNetwork: true}} {
		meta := claudeSessionMeta(cfg)
		if meta["claudeCode"] == nil || meta["systemPrompt"] != nil {
			t.Fatalf("diagnostic metadata = %#v", meta)
		}
	}
	ordinary := claudeSessionMeta(acpdriver.LaunchConfig{SystemPrompt: "ordinary"})
	if ordinary["claudeCode"] != nil {
		t.Fatalf("ordinary session received diagnostic policy: %#v", ordinary)
	}
}

func TestDiagnosticPolicyCannotBeChangedByTurnOptions(t *testing.T) {
	options := claudeSessionOptions(ports.ChatTurnSettings{
		Model: "opus", Effort: "high", Approval: ports.PermissionModeBypassPermissions,
	})
	want := []acpdriver.SessionOption{{ID: "model", Value: "opus"}, {ID: "effort", Value: "high"}}
	if !reflect.DeepEqual(options, want) {
		t.Fatalf("turn options = %#v, want only model and effort", options)
	}
	// Mode selection remains independent from the native tools/disallowedTools
	// passed once at session creation. Even bypass cannot restore denied tools.
	if claudeSessionMode(ports.PermissionModeBypassPermissions) != "bypassPermissions" {
		t.Fatal("ordinary mode mapping unexpectedly changed")
	}
}

type diagnosticRecorder struct {
	ports.ChatDriver
	started, resumed bool
}

func (d *diagnosticRecorder) Start(context.Context, ports.ChatStartConfig) (ports.ChatConversation, error) {
	d.started = true
	return nil, ports.ErrChatAuthRequired
}

func (d *diagnosticRecorder) Resume(context.Context, ports.ChatResumeConfig) (ports.ChatConversation, error) {
	d.resumed = true
	return nil, ports.ErrChatAuthRequired
}

func TestDiagnosticSessionsRejectSuppliedMCPBeforeLaunch(t *testing.T) {
	base := &diagnosticRecorder{}
	driver := &checkpointDriver{ChatDriver: base}
	mcp := []ports.ChatMCPServerConfig{{Name: "unsafe"}}
	if _, err := driver.Start(t.Context(), ports.ChatStartConfig{ReadOnly: true, DisableNetwork: true, MCPServers: mcp}); !errors.Is(err, ports.ErrChatCapabilityUnavailable) {
		t.Fatalf("Start = %v, want capability error", err)
	}
	if _, err := driver.Resume(t.Context(), ports.ChatResumeConfig{ReadOnly: true, MCPServers: mcp}); !errors.Is(err, ports.ErrChatCapabilityUnavailable) {
		t.Fatalf("Resume = %v, want capability error", err)
	}
	if base.started || base.resumed {
		t.Fatal("diagnostic session reached MCP process launch")
	}
	if _, err := driver.ForkIntoHost(t.Context(), "parent", "", ports.ChatStartConfig{ReadOnly: true}); !errors.Is(err, ports.ErrChatCapabilityUnavailable) {
		t.Fatalf("restricted fork = %v, want fresh-session requirement", err)
	}
	if _, err := driver.Start(t.Context(), ports.ChatStartConfig{MCPServers: mcp}); !errors.Is(err, ports.ErrChatAuthRequired) || !base.started {
		t.Fatalf("ordinary Start was not forwarded: %v", err)
	}
	if _, err := driver.Resume(t.Context(), ports.ChatResumeConfig{MCPServers: mcp}); !errors.Is(err, ports.ErrChatAuthRequired) || !base.resumed {
		t.Fatalf("ordinary Resume was not forwarded: %v", err)
	}
}

func TestDiagnosticLaunchRejectsUntestedBridgeOverride(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AO_CLAUDE_ACP_COMMAND", executable)
	_, err = New(reconnectPlugin{}, nil, nil).Start(t.Context(), ports.ChatStartConfig{
		SessionID: "diagnostic-override", DataDir: t.TempDir(), WorkspacePath: t.TempDir(),
		ReadOnly: true, DisableNetwork: true,
	})
	if !errors.Is(err, ports.ErrChatCapabilityUnavailable) || !strings.Contains(err.Error(), diagnosticACPVersion) {
		t.Fatalf("Start = %v, want pinned bridge requirement before resolving Claude", err)
	}
}

func TestDriverExposesDiagnosticPolicy(t *testing.T) {
	driver, ok := New(reconnectPlugin{}, nil, nil).(ports.ChatDiagnosticDriver)
	if !ok || driver.DiagnosticPolicy() != (ports.ChatDiagnosticPolicy{ReadOnly: true, DisableToolNetwork: true}) {
		t.Fatalf("diagnostic driver = %#v", driver)
	}
}

func TestDiagnosticRuntimeRequiresPinnedBridgeManifest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake packaged Node is a POSIX shell script")
	}
	directory := t.TempDir()
	node := filepath.Join(directory, "node", "bin", "node")
	adapter := filepath.Join(directory, "node_modules", "@agentclientprotocol", "claude-agent-acp")
	for path, contents := range map[string]string{
		node: "#!/bin/sh\nprintf 'v22.23.2\\n'\n",
		filepath.Join(adapter, "dist", "index.js"): "// unused provider entrypoint\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AO_CLAUDE_ACP_COMMAND", "")
	t.Setenv("AO_ACP_RUNTIME_DIR", directory)
	for _, test := range []struct {
		name, manifest string
		want           bool
	}{
		{"pinned", `{"name":"@agentclientprotocol/claude-agent-acp","version":"0.70.0"}`, true},
		{"different version", `{"name":"@agentclientprotocol/claude-agent-acp","version":"0.69.0"}`, false},
		{"different package", `{"name":"unverified-bridge","version":"0.70.0"}`, false},
		{"malformed manifest", `{`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(adapter, "package.json"), []byte(test.manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			launch, err := resolveRuntime(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if launch.diagnosticPolicy != test.want {
				t.Fatalf("diagnostic support = %t, want %t", launch.diagnosticPolicy, test.want)
			}
		})
	}
}
