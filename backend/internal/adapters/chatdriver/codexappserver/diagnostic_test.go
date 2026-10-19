package codexappserver

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestDiagnosticReadyAllowsRestrictedContinuationAfterTerminalTurn(t *testing.T) {
	for _, state := range []domain.TurnState{domain.TurnStateCompleted, domain.TurnStateFailed, domain.TurnStateInterrupted} {
		for _, scope := range []string{"", "diagnostic-owner"} {
			t.Run(string(state)+"/"+scope, func(t *testing.T) {
				driver, server := newTestDriver(t)
				policy := driver.DiagnosticPolicy()
				if !policy.ReadOnly || !policy.DisableToolNetwork {
					t.Fatalf("native diagnostic controls unavailable: %#v", policy)
				}
				server.respondSequence("turn/start", `{"turn":{"id":"turn-1"}}`, `{"turn":{"id":"turn-2"}}`)
				provider, err := driver.Start(context.Background(), ports.ChatStartConfig{
					WorkspacePath: t.TempDir(), ReadOnly: true, DisableNetwork: true,
					Permissions: ports.PermissionModeDefault, ProviderScopeID: scope, ProviderIDsScoped: scope != "",
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = provider.Close() })
				conv := provider.(*conversation)
				first, err := provider.SendTurn(context.Background(), ports.ChatUserMessage{Text: "Inspect the supplied facts.", Settings: ports.ChatTurnSettings{Approval: ports.PermissionModeBypassPermissions}})
				if err != nil {
					t.Fatal(err)
				}
				server.push(fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":%q}}}`, state))
				completed := nextEvent(t, provider.Events(), ports.ChatEventTurnCompleted)
				ready := nextEvent(t, provider.Events(), ports.ChatEventControllerState)
				if completed.TurnState != state || completed.ProviderTurnID != first.ProviderTurnID || ready.ControllerState != ports.ChatControllerReady || ready.ProviderTurnID != first.ProviderTurnID || ready.ProviderConversationID != provider.ProviderConversationID() {
					t.Fatalf("completion/readiness correlation changed: completed=%#v ready=%#v ref=%#v", completed, ready, first)
				}
				conv.mu.Lock()
				active := conv.activeTurn
				conv.mu.Unlock()
				if active != "" {
					t.Fatalf("Ready retained an active turn: %q", active)
				}
				second, err := provider.SendTurn(context.Background(), ports.ChatUserMessage{Text: "Continue with the requested private context.", Settings: ports.ChatTurnSettings{Approval: ports.PermissionModeAuto}})
				if err != nil || second.ProviderTurnID != conv.scopedID("turn-2") {
					t.Fatalf("continuation after Ready failed: %#v err=%v", second, err)
				}
				if err := provider.Interrupt(context.Background(), ""); err != nil {
					t.Fatal(err)
				}
				interrupt := server.awaitFrame(func(f frame) bool { return f.Method == "turn/interrupt" })
				var params map[string]any
				if err := json.Unmarshal(interrupt.Params, &params); err != nil || params["turnId"] != "turn-2" {
					t.Fatalf("continuation did not own native active turn: %s err=%v", interrupt.Params, err)
				}
				server.mu.Lock()
				var turns []frame
				for _, request := range server.seen {
					if request.Method == "turn/start" {
						turns = append(turns, request)
					}
				}
				server.mu.Unlock()
				if len(turns) != 2 {
					t.Fatalf("expected two restricted turns, got %d", len(turns))
				}
				for _, turn := range turns {
					if err := json.Unmarshal(turn.Params, &params); err != nil {
						t.Fatal(err)
					}
					if params["approvalPolicy"] != "never" || !reflect.DeepEqual(params["sandboxPolicy"], map[string]any{"type": "readOnly", "networkAccess": false}) {
						t.Fatalf("native restrictions were lost across continuation: %s", turn.Params)
					}
				}
			})
		}
	}
}

func TestReadyDoesNotSettleChildStaleOrNonterminalTurn(t *testing.T) {
	driver, server := newTestDriver(t)
	provider, err := driver.Start(context.Background(), ports.ChatStartConfig{WorkspacePath: t.TempDir(), ProviderScopeID: "owner", ProviderIDsScoped: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	if _, err := provider.SendTurn(context.Background(), ports.ChatUserMessage{Text: "Inspect evidence."}); err != nil {
		t.Fatal(err)
	}
	for _, notification := range []string{
		`{"method":"turn/completed","params":{"threadId":"child-thread","turn":{"id":"turn-1","status":"completed"}}}`,
		`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"older-turn","status":"failed"}}}`,
		`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"inProgress"}}}`,
	} {
		server.push(notification)
	}
	server.push(`{"method":"thread/name/updated","params":{"threadId":"thread-1","threadName":"sentinel"}}`)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case event := <-provider.Events():
			if event.Kind == ports.ChatEventControllerState && event.ControllerState == ports.ChatControllerReady {
				t.Fatalf("unrelated or nonterminal completion announced Ready: %#v", event)
			}
			if event.Kind == ports.ChatEventThreadRenamed {
				conv := provider.(*conversation)
				conv.mu.Lock()
				active := conv.activeTurn
				conv.mu.Unlock()
				if active != "turn-1" {
					t.Fatalf("unrelated completion cleared root turn: %q", active)
				}
				server.push(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}}`)
				nextEvent(t, provider.Events(), ports.ChatEventTurnCompleted)
				if ready := nextEvent(t, provider.Events(), ports.ChatEventControllerState); ready.ControllerState != ports.ChatControllerReady {
					t.Fatalf("matching terminal completion did not announce Ready: %#v", ready)
				}
				return
			}
		case <-deadline:
			t.Fatal("provider events did not reach sentinel")
		}
	}
}

func TestDiagnosticReadyWaitsForLateTurnStartResponse(t *testing.T) {
	driver, server := newTestDriver(t)
	provider, err := driver.Start(context.Background(), ports.ChatStartConfig{
		WorkspacePath: t.TempDir(), ProviderScopeID: "owner", ProviderIDsScoped: true,
		ReadOnly: true, DisableNetwork: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	conv := provider.(*conversation)
	server.mu.Lock()
	delete(server.responses, "turn/start")
	server.mu.Unlock()
	type dispatchResult struct {
		turn ports.ChatTurnRef
		err  error
	}
	dispatched := make(chan dispatchResult, 1)
	go func() {
		turn, err := provider.SendTurn(context.Background(), ports.ChatUserMessage{Text: "Inspect the supplied facts."})
		dispatched <- dispatchResult{turn: turn, err: err}
	}()
	request := server.awaitFrame(func(f frame) bool { return f.Method == "turn/start" })
	server.push(`{"method":"turn/started","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"inProgress"}}}`)
	nextEvent(t, provider.Events(), ports.ChatEventTurnStarted)
	server.push(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}}`)
	// A provider request after the completion is a wire barrier: receiving its
	// refusal proves the reader consumed that completion before we release the
	// held turn/start response. Response delivery must remain independent of the
	// notification pump waiting for the dispatch lock.
	server.push(`{"id":9001,"method":"ao/testDispatchBarrier","params":{}}`)
	barrier := server.awaitFrame(func(f frame) bool { return f.Method == "" && f.ID != nil && string(*f.ID) == "9001" })
	if barrier.Error == nil {
		t.Fatal("unmodelled provider barrier was not refused")
	}
	if conv.sendMu.TryLock() {
		conv.sendMu.Unlock()
		t.Fatal("dispatch lock was released before the start response")
	}
	select {
	case result := <-dispatched:
		t.Fatalf("dispatch settled before its response: %#v", result)
	default:
	}
	select {
	case event := <-provider.Events():
		t.Fatalf("completion or readiness escaped before dispatch settled: %#v", event)
	default:
	}
	server.push(fmt.Sprintf(`{"id":%s,"result":{"turn":{"id":"turn-1"}}}`, *request.ID))
	select {
	case result := <-dispatched:
		if result.err != nil || result.turn.ProviderTurnID != conv.scopedID("turn-1") {
			t.Fatalf("late dispatch response failed: %#v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dispatch did not settle after its response")
	}
	completed := nextEvent(t, provider.Events(), ports.ChatEventTurnCompleted)
	ready := nextEvent(t, provider.Events(), ports.ChatEventControllerState)
	if completed.ProviderTurnID != conv.scopedID("turn-1") || ready.ControllerState != ports.ChatControllerReady || ready.ProviderTurnID != completed.ProviderTurnID {
		t.Fatalf("late acknowledgement changed completion/readiness order or identity: completed=%#v ready=%#v", completed, ready)
	}
	conv.mu.Lock()
	active := conv.activeTurn
	conv.mu.Unlock()
	if active != "" {
		t.Fatalf("late start response resurrected completed turn: %q", active)
	}
	server.respondTo("turn/start", `{"turn":{"id":"turn-2"}}`)
	if _, err := provider.SendTurn(context.Background(), ports.ChatUserMessage{Text: "Continue with private context."}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Interrupt(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	interrupt := server.awaitFrame(func(f frame) bool { return f.Method == "turn/interrupt" })
	var params map[string]any
	if err := json.Unmarshal(interrupt.Params, &params); err != nil || params["turnId"] != "turn-2" {
		t.Fatalf("next turn did not own implicit interruption: %s err=%v", interrupt.Params, err)
	}
}
