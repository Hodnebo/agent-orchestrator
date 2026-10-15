package fxacp

import (
	"context"
	"log/slog"
	"reflect"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type stubPlugin struct{}

func (stubPlugin) ResolveBinary(context.Context) (string, error) {
	return "/Users/test/.local/bin/fx", nil
}
func (stubPlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	return ports.AgentAuthStatusAuthorized, nil
}

// `fx acp` accepts only --model and --log-file, and neither permissions nor
// standing instructions have a launch channel, so every mode launches the same
// argv and the session carries the rest.
func TestConfigureLaunchesPlainACPForEveryMode(t *testing.T) {
	for _, mode := range []ports.PermissionMode{
		"", ports.PermissionModeDefault, ports.PermissionModeAcceptEdits,
		ports.PermissionModeAuto, ports.PermissionModeBypassPermissions,
	} {
		args, env, err := configure(context.Background(), acpdriver.LaunchConfig{
			Permissions: mode, Model: "gpt-6-sol", SystemPrompt: "AO rules",
		})
		if err != nil {
			t.Fatalf("configure(%q): %v", mode, err)
		}
		if !reflect.DeepEqual(args, []string{"acp"}) {
			t.Errorf("configure(%q) args = %#v, want [acp]", mode, args)
		}
		if env != nil {
			t.Errorf("configure(%q) env = %#v, want nil (FX_PERMISSION_MODE does not reach ACP)", mode, env)
		}
	}
}

func TestSessionModeUsesOnlyAdvertisedModes(t *testing.T) {
	for mode, want := range map[ports.PermissionMode]string{
		"":                                    modeAsk,
		ports.PermissionModeDefault:           modeAsk,
		ports.PermissionModeAcceptEdits:       modeAsk,
		ports.PermissionModeAuto:              modeCode,
		ports.PermissionModeBypassPermissions: modeAsk,
	} {
		if got := sessionMode(mode); got != want {
			t.Errorf("sessionMode(%q) = %q, want %q", mode, got, want)
		}
	}
}

func TestSessionOptionsMapModelAndEffort(t *testing.T) {
	got := sessionOptions(ports.ChatTurnSettings{Model: " gpt-6-sol ", Effort: "xhigh"})
	want := []acpdriver.SessionOption{{ID: "model", Value: "gpt-6-sol"}, {ID: "effort", Value: "xhigh"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sessionOptions = %#v, want %#v", got, want)
	}
	if got := sessionOptions(ports.ChatTurnSettings{}); got != nil {
		t.Fatalf("empty settings = %#v, want nil", got)
	}
}

func TestSessionPreambleDeliversStandingInstructions(t *testing.T) {
	blocks := sessionPreamble(acpdriver.LaunchConfig{SystemPrompt: "  You are an AO worker.  "})
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	resource := blocks[0].Resource
	if resource == nil || resource.Resource.TextResourceContents == nil {
		t.Fatalf("block = %#v, want an embedded text resource", blocks[0])
	}
	contents := resource.Resource.TextResourceContents
	if contents.Uri != standingInstructionsURI || contents.Text != "You are an AO worker." {
		t.Fatalf("contents = %#v", contents)
	}
	if contents.MimeType == nil || *contents.MimeType != "text/markdown" {
		t.Fatalf("mimeType = %v, want text/markdown", contents.MimeType)
	}
	for _, empty := range []string{"", " \n\t "} {
		if blocks := sessionPreamble(acpdriver.LaunchConfig{SystemPrompt: empty}); blocks != nil {
			t.Errorf("sessionPreamble(%q) = %v, want nil", empty, blocks)
		}
	}
}

func TestPermissionPolicy(t *testing.T) {
	edit := acpsdk.ToolKindEdit
	execute := acpsdk.ToolKindExecute
	params := func(kind *acpsdk.ToolKind) acpsdk.RequestPermissionRequest {
		return acpsdk.RequestPermissionRequest{
			ToolCall: acpsdk.ToolCallUpdate{Kind: kind},
			Options: []acpsdk.PermissionOption{
				{OptionId: "reject", Kind: acpsdk.PermissionOptionKindRejectOnce},
				{OptionId: "allow", Kind: acpsdk.PermissionOptionKindAllowOnce},
			},
		}
	}
	// Bypass answers every request fx's ask mode sends.
	for _, kind := range []*acpsdk.ToolKind{&edit, &execute, nil} {
		if id, ok := permissionPolicy(ports.PermissionModeBypassPermissions, params(kind)); !ok || id != "allow" {
			t.Fatalf("bypass = (%q, %v), want (allow, true)", id, ok)
		}
	}
	// Accept-edits answers edit-shaped requests only.
	if id, ok := permissionPolicy(ports.PermissionModeAcceptEdits, params(&edit)); !ok || id != "allow" {
		t.Fatalf("accept-edits on edit = (%q, %v), want (allow, true)", id, ok)
	}
	if _, ok := permissionPolicy(ports.PermissionModeAcceptEdits, params(&execute)); ok {
		t.Fatal("accept-edits answered a non-edit tool")
	}
	// Default asks the human; auto leaves fx's reviewer in charge.
	for _, mode := range []ports.PermissionMode{ports.PermissionModeDefault, ports.PermissionModeAuto} {
		if _, ok := permissionPolicy(mode, params(&edit)); ok {
			t.Errorf("%q answered a permission request", mode)
		}
	}
}

func TestNewDriverTargetsFXHarness(t *testing.T) {
	driver := New(stubPlugin{}, slog.New(slog.DiscardHandler))
	if driver.Harness() != domain.HarnessFX {
		t.Fatalf("harness = %q, want %q", driver.Harness(), domain.HarnessFX)
	}
	capabilities, err := driver.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	for _, capability := range []ports.ChatCapability{ports.ChatCapabilityModels, ports.ChatCapabilityConfigOptions} {
		if !capabilities.Has(capability) {
			t.Errorf("Probe does not advertise %q", capability)
		}
	}
}
