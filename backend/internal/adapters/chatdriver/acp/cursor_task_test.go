package acp

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCursorTaskCompletedEventKeepsOnlyCompactMetadata(t *testing.T) {
	event, ok := cursorTaskCompletedEvent(json.RawMessage(`{"toolCallId":"call-1","description":"Inspect   the code","prompt":"PRIVATE_HELPER_PROMPT","subagentType":{"custom":"map-review"},"agentId":"agent-1","durationMs":1250}`))
	if !ok {
		t.Fatal("cursor/task notification was not normalized")
	}
	if event.Kind != ports.ChatEventActivityCompleted || event.ActivityKind != domain.ActivityKindSystem || event.ActivityStatus != domain.ActivityStatusCompleted {
		t.Fatalf("activity = %+v", event)
	}
	if event.ProviderItemID != "cursor-task:call-1" || event.ProviderEventID != "cursor/task:call-1" {
		t.Fatalf("provider identity = (%q, %q)", event.ProviderItemID, event.ProviderEventID)
	}
	if event.Summary != "Subagent completed: Inspect the code" {
		t.Fatalf("summary = %q", event.Summary)
	}
	if strings.Contains(string(event.Detail), "PRIVATE_HELPER_PROMPT") || strings.Contains(event.Summary, "PRIVATE_HELPER_PROMPT") {
		t.Fatalf("private delegated prompt persisted: %+v", event)
	}
	var detail map[string]any
	if err := json.Unmarshal(event.Detail, &detail); err != nil {
		t.Fatal(err)
	}
	if detail["event"] != "cursor.subagent" || detail["subagentType"] != "map-review" || detail["durationMs"] != float64(1250) {
		t.Fatalf("detail = %#v", detail)
	}
}

func TestCursorTaskCompletedEventBoundsAndValidatesMetadata(t *testing.T) {
	longDescription := strings.Repeat("x", 200)
	event, ok := cursorTaskCompletedEvent(json.RawMessage(`{"agentId":"agent-fallback","description":"` + longDescription + `","subagentType":"explore","durationMs":-1}`))
	if !ok {
		t.Fatal("agent id should provide a stable fallback")
	}
	if len([]rune(event.Summary)) > len([]rune("Subagent completed: "))+160 {
		t.Fatalf("summary was not bounded: %d runes", len([]rune(event.Summary)))
	}
	if event.ProviderItemID != "cursor-task:agent-fallback" || strings.Contains(string(event.Detail), "durationMs") {
		t.Fatalf("invalid duration or item id not handled safely: %+v", event)
	}

	for _, raw := range []string{
		`not-json`,
		`{"description":"missing id"}`,
		`{"toolCallId":3}`,
	} {
		if event, ok := cursorTaskCompletedEvent(json.RawMessage(raw)); ok {
			t.Fatalf("malformed notification %q became event %+v", raw, event)
		}
	}
}

func TestHandleCursorTaskNotificationEmitsInActiveTurn(t *testing.T) {
	events := make(chan ports.ChatEvent, 1)
	conversation := &conversation{
		activeTurn:      "turn-1",
		providerScopeID: "scope-1",
		events:          events,
		log:             slog.New(slog.DiscardHandler),
	}
	if !conversation.handleNotification("cursor/task", json.RawMessage(`{"toolCallId":"call-1","description":"Explore files","prompt":"do not store","subagentType":"explore","durationMs":500}`)) {
		t.Fatal("cursor/task notification was not recognized")
	}
	select {
	case event := <-events:
		if event.ProviderTurnID != "turn-1" || event.ProviderItemID != conversation.providerItemID("cursor-task:call-1") {
			t.Fatalf("event identity = %+v", event)
		}
		if strings.Contains(string(event.Detail), "do not store") {
			t.Fatalf("prompt leaked into activity detail: %s", event.Detail)
		}
	default:
		t.Fatal("cursor/task notification produced no activity event")
	}
}
