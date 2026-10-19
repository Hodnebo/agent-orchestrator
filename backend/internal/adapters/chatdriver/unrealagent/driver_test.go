//go:build darwin || linux

package unrealagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type authPlugin struct {
	status    ports.AgentAuthStatus
	envStatus ports.AgentAuthStatus
}

func (plugin authPlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return plugin.status, nil
}

func (plugin authPlugin) AuthStatusWithEnv(context.Context, map[string]string) (ports.AgentAuthStatus, error) {
	return plugin.envStatus, nil
}

func TestDriverRequiresExplicitBypassPermissions(t *testing.T) {
	driver := New(authPlugin{status: ports.AgentAuthStatusAuthorized}, nil)
	driver.executable = func() (string, error) { return "/path/to/ao", nil }
	caps, err := driver.Probe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if missing := ports.MissingCapabilitiesForPermissions(caps, ports.PermissionModeBypassPermissions); len(missing) != 0 {
		t.Fatalf("bypass production capabilities missing %v", missing)
	}
	if err := validateStart(t.TempDir(), ports.PermissionModeBypassPermissions, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := validateStart(t.TempDir(), ports.PermissionModeDefault, false, nil, nil); err == nil {
		t.Fatal("default permissions unexpectedly accepted without an approval channel")
	}
}

func TestDriverReadOnlyAcceptsDefaultWithoutChangingMutableValidation(t *testing.T) {
	for _, test := range []struct {
		mode              ports.PermissionMode
		readOnly, allowed bool
	}{
		{ports.PermissionModeDefault, true, true},
		{"", true, true},
		{ports.PermissionModeBypassPermissions, true, true},
		{ports.PermissionModeAcceptEdits, true, false},
		{ports.PermissionModeAuto, true, false},
		{ports.PermissionModeDefault, false, false},
		{"", false, false},
		{ports.PermissionModeAcceptEdits, false, false},
		{ports.PermissionModeAuto, false, false},
		{ports.PermissionModeBypassPermissions, false, true},
	} {
		err := validateStart(t.TempDir(), test.mode, test.readOnly, nil, nil)
		if (err == nil) != test.allowed {
			t.Errorf("mode %q readOnly=%t error=%v, allowed=%t", test.mode, test.readOnly, err, test.allowed)
		}
	}
	if err := validateStart(t.TempDir(), ports.PermissionModeDefault, true, []string{t.TempDir()}, nil); err == nil {
		t.Error("read-only mode accepted additional directories")
	}
	if err := validateStart(t.TempDir(), ports.PermissionModeDefault, true, nil, []ports.ChatMCPServerConfig{{Name: "external"}}); err == nil {
		t.Error("read-only mode accepted external MCP tools")
	}
}

func TestDriverReadOnlyPolicyPersistsThroughStartResumeAndTurns(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	driver := New(authPlugin{envStatus: ports.AgentAuthStatusAuthorized}, nil)
	driver.executable = func() (string, error) { return "/path/to/ao", nil }
	workspace, dataDir := t.TempDir(), t.TempDir()
	var fingerprints []string
	var providerID string
	commands := make(chan command, 4)
	driver.connectHost = func(_ context.Context, cfg persistenthost.Config) (*persistenthost.Transport, error) {
		prepared, err := cfg.Prepare(ctx)
		if err != nil {
			return nil, err
		}
		if len(prepared.Argv) != 3 || prepared.Argv[1] != "unreal-provider" {
			t.Fatalf("provider launch = %v", prepared.Argv)
		}
		persisted, err := readProviderConfig(prepared.Argv[2])
		if err != nil {
			return nil, err
		}
		if !persisted.ReadOnly || persisted.SystemPrompt != "investigate using supplied context" {
			t.Fatalf("persisted policy = %#v", persisted)
		}
		if providerID != "" && persisted.ProviderConversationID != providerID {
			t.Fatalf("resume changed provider ID to %q", persisted.ProviderConversationID)
		}
		providerID = persisted.ProviderConversationID
		fingerprints = append(fingerprints, cfg.OwnershipFingerprint)
		reconnected := len(fingerprints) == 2
		providerInput, hostInput := io.Pipe()
		hostOutput, providerOutput := io.Pipe()
		t.Cleanup(func() {
			_ = providerInput.Close()
			_ = hostInput.Close()
			_ = hostOutput.Close()
			_ = providerOutput.Close()
		})
		go func() {
			defer func() { _ = providerOutput.Close() }()
			encoder := json.NewEncoder(providerOutput)
			if !reconnected {
				if err := encoder.Encode(frame{Version: protocolVersion, Type: "ready"}); err != nil {
					return
				}
			}
			decoder := json.NewDecoder(providerInput)
			for {
				var cmd command
				if err := decoder.Decode(&cmd); err != nil {
					return
				}
				if cmd.Type == "turn" {
					commands <- cmd
				}
				if err := encoder.Encode(resultFrame(cmd.RequestID, nil)); err != nil {
					return
				}
			}
		}()
		return &persistenthost.Transport{Stdin: hostInput, Stdout: hostOutput, Reconnected: reconnected}, nil
	}
	startCfg := ports.ChatStartConfig{
		SessionID: "read-only-session", DataDir: dataDir, WorkspacePath: workspace,
		Permissions: ports.PermissionModeDefault, ReadOnly: true,
		SystemPrompt: "investigate using supplied context", ProviderScopeID: "triage",
	}
	fresh, err := driver.Start(ctx, startCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := driver.Resume(ctx, ports.ChatResumeConfig{
		SessionID: startCfg.SessionID, ProviderConversationID: providerID,
		DataDir: dataDir, WorkspacePath: workspace,
		Permissions: ports.PermissionModeDefault, ReadOnly: true,
		SystemPrompt: startCfg.SystemPrompt, ProviderScopeID: startCfg.ProviderScopeID,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resumed.Close() }()
	if !resumed.(ports.ChatLiveReconnector).ReconnectedLive() {
		t.Fatal("Resume did not exercise the live reconnect path")
	}
	if resumed.Capabilities().Has(ports.ChatCapabilityTools) {
		t.Fatal("no-tools conversation advertised native tools")
	}
	for _, approval := range []ports.PermissionMode{ports.PermissionModeDefault, ports.PermissionModeBypassPermissions} {
		if _, err := resumed.SendTurn(ctx, ports.ChatUserMessage{
			Text: "inspect injected facts", Settings: ports.ChatTurnSettings{Approval: approval},
		}); err != nil {
			t.Fatalf("read-only SendTurn(%q): %v", approval, err)
		}
		select {
		case cmd := <-commands:
			if cmd.Type != "turn" || cmd.Text != "inspect injected facts" {
				t.Fatalf("native turn = %#v", cmd)
			}
		case <-ctx.Done():
			t.Fatal("read-only turn was not sent")
		}
	}
	if !resumed.(*conversation).cfg.ReadOnly {
		t.Fatal("turn settings widened the conversation policy")
	}
	if len(fingerprints) != 2 || fingerprints[0] != fingerprints[1] {
		t.Fatalf("Start/Resume ownership changed: %v", fingerprints)
	}
	legacyOwner, _ := json.Marshal([]string{filepath.Clean(workspace), string(domain.HarnessUnreal), "triage"})
	legacyFingerprint := sha256.Sum256(legacyOwner)
	if fingerprints[0] == hex.EncodeToString(legacyFingerprint[:]) {
		t.Fatal("read-only policy shares ownership with a mutable host")
	}
}

func TestDriverDefersAuthenticationToLaunchEnvironment(t *testing.T) {
	driver := New(authPlugin{
		status: ports.AgentAuthStatusUnauthorized, envStatus: ports.AgentAuthStatusAuthorized,
	}, nil)
	driver.executable = func() (string, error) { return "/path/to/ao", nil }
	if _, err := driver.Probe(t.Context()); err != nil {
		t.Fatalf("Probe() error = %v, want launch environment to decide auth", err)
	}
	if err := driver.validateLaunchAuth(t.Context(), map[string]string{"OPENAI_API_KEY": "project-key"}); err != nil {
		t.Fatalf("validateLaunchAuth() error = %v", err)
	}
	missing := New(authPlugin{envStatus: ports.AgentAuthStatusUnauthorized}, nil)
	if err := missing.validateLaunchAuth(t.Context(), nil); !errors.Is(err, ports.ErrChatAuthRequired) {
		t.Fatalf("validateLaunchAuth() error = %v, want ErrChatAuthRequired", err)
	}
}

func TestReplayFailureTerminatesFreshHost(t *testing.T) {
	driver := New(authPlugin{
		status: ports.AgentAuthStatusAuthorized, envStatus: ports.AgentAuthStatusAuthorized,
	}, nil)
	providerInput, hostInput := io.Pipe()
	hostOutput, providerOutput := io.Pipe()
	t.Cleanup(func() {
		_ = providerInput.Close()
		_ = hostInput.Close()
		_ = hostOutput.Close()
		_ = providerOutput.Close()
	})
	driver.connectHost = func(context.Context, persistenthost.Config) (*persistenthost.Transport, error) {
		go func() {
			_ = json.NewEncoder(providerOutput).Encode(frame{Version: protocolVersion, Type: "ready"})
			var replay command
			if err := json.NewDecoder(providerInput).Decode(&replay); err == nil {
				_ = json.NewEncoder(providerOutput).Encode(frame{
					Version: protocolVersion, Type: "result", RequestID: replay.RequestID,
					Error: "forced replay failure",
				})
			}
		}()
		return &persistenthost.Transport{Stdin: hostInput, Stdout: hostOutput}, nil
	}
	terminated := make(chan string, 1)
	driver.shutdownHost = func(_ context.Context, _ string, sessionID string) error {
		terminated <- sessionID
		return nil
	}
	cfg := providerConfig{
		AOSessionID: "ao-session", ProviderConversationID: "provider-session",
		DataDir: t.TempDir(), WorkspacePath: t.TempDir(),
	}

	_, err := driver.connect(t.Context(), cfg, nil, nil, "scope")
	if err == nil || !strings.Contains(err.Error(), "forced replay failure") {
		t.Fatalf("connect error = %v, want replay failure", err)
	}
	select {
	case sessionID := <-terminated:
		if sessionID != cfg.AOSessionID {
			t.Fatalf("terminated session = %q, want %q", sessionID, cfg.AOSessionID)
		}
	default:
		t.Fatal("replay failure left the fresh host running")
	}
}
