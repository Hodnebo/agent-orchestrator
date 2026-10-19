//go:build darwin || linux

package unrealagent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestConversationProtocolMismatchSignalsReadyAndStopped(t *testing.T) {
	hostInput, providerInput := io.Pipe()
	providerOutput, hostOutput := io.Pipe()
	t.Cleanup(func() {
		_ = hostInput.Close()
		_ = hostOutput.Close()
		_ = providerOutput.Close()
	})
	conv := newConversation(providerConfig{}, &persistenthost.Transport{
		Stdin: providerInput, Stdout: providerOutput,
	}, slog.New(slog.DiscardHandler), nil)

	if err := json.NewEncoder(hostOutput).Encode(frame{Version: protocolVersion + 1, Type: "ready"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	readyErr := conv.waitReady(ctx)
	if readyErr == nil || !strings.Contains(readyErr.Error(), "protocol version") {
		t.Fatalf("waitReady error = %v, want protocol version mismatch", readyErr)
	}

	select {
	case event := <-conv.Events():
		if event.Kind != ports.ChatEventControllerState || event.ControllerState != ports.ChatControllerStopped ||
			event.Err == nil || !strings.Contains(event.Err.Error(), "protocol version") {
			t.Fatalf("stopped event = %#v", event)
		}
	case <-ctx.Done():
		t.Fatal("protocol mismatch emitted no stopped event")
	}
}

func TestConversationMutableTurnsStillRejectDefaultPermissions(t *testing.T) {
	conv := &conversation{cfg: providerConfig{ReadOnly: false}}
	for _, approval := range []ports.PermissionMode{
		ports.PermissionModeDefault, ports.PermissionModeAcceptEdits, ports.PermissionModeAuto,
	} {
		if _, err := conv.SendTurn(t.Context(), ports.ChatUserMessage{
			Text: "change a file", Settings: ports.ChatTurnSettings{Approval: approval},
		}); err == nil || !strings.Contains(err.Error(), "requires bypass-permissions") {
			t.Errorf("mutable SendTurn(%q) = %v, want bypass validation", approval, err)
		}
	}
}
