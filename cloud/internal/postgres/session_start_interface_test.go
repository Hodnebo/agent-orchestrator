package postgres

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// initialTurn reports the state of the turn queued for a session's first user
// message, and whether that message's event names it.
func initialTurn(t *testing.T, admin *pgxpool.Pool, sessionID string) (state string, found, linked bool) {
	t.Helper()
	ctx := context.Background()
	var turnID string
	err := admin.QueryRow(ctx, `SELECT turn.id, turn.state FROM ao_turns turn
		JOIN ao_events event ON event.session_id = turn.session_id AND event.sequence = turn.user_message_sequence
		WHERE turn.session_id = $1 AND event.type = 'chat.user_message'`, sessionID).Scan(&turnID, &state)
	if err != nil {
		return "", false, false
	}
	var payload map[string]any
	var raw []byte
	if err := admin.QueryRow(ctx, `SELECT payload FROM ao_events WHERE session_id = $1 AND type = 'chat.user_message'`, sessionID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	return state, true, payload["turnId"] == turnID
}

// A session created in Chat is stored in Chat with its prompt queued as the
// first turn, which is the only work the Chat controller takes. A TUI session
// (the default) keeps the prompt on the agent's command line and queues
// nothing, exactly as before.
func TestCreateSessionStartInterface(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	principal := domain.Principal{UserID: fixture.userID, Provider: "local"}
	create := func(harness string, iface domain.SessionInterface) domain.Session {
		t.Helper()
		session, err := store.CreateSession(ctx, principal, fixture.orgID, uuid.NewString(), 100, domain.CreateSession{
			ProjectID: fixture.projectID, Kind: "worker", Harness: harness, DisplayName: "s", Prompt: "do the work",
			Provider: "docker", Interface: iface,
		})
		if err != nil {
			t.Fatal(err)
		}
		return session
	}

	chat := create("claude-code", domain.SessionInterfaceChat)
	if chat.Interface != domain.SessionInterfaceChat {
		t.Fatalf("chat session interface = %q", chat.Interface)
	}
	if state, found, linked := initialTurn(t, admin, chat.ID); !found || state != "queued" || !linked {
		t.Fatalf("chat initial turn: state=%q found=%v linked=%v, want a queued turn the message names", state, found, linked)
	}

	for name, session := range map[string]domain.Session{
		"omitted":   create("claude-code", ""),
		"tui":       create("codex", domain.SessionInterfaceTUI),
		"cursor":    create("cursor", domain.SessionInterfaceChat),
		"tui codex": create("codex", ""),
	} {
		if session.Interface != domain.SessionInterfaceTUI {
			t.Fatalf("%s: interface = %q, want tui", name, session.Interface)
		}
		if _, found, _ := initialTurn(t, admin, session.ID); found {
			t.Fatalf("%s: a TUI session queued its prompt as a turn", name)
		}
	}
}

// A create that omits the interface records the same idempotency payload as
// before the field existed (it always carried "Interface":"tui"), so a retry
// across the upgrade still replays the first create.
func TestCreateSessionIdempotencyPayloadUnchangedWithoutInterface(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	principal := domain.Principal{UserID: fixture.userID, Provider: "local"}
	key := uuid.NewString()
	input := domain.CreateSession{
		ProjectID: fixture.projectID, Kind: "worker", Harness: "claude-code", DisplayName: "s", Prompt: "go", Provider: "docker",
	}
	first, err := store.CreateSession(ctx, principal, fixture.orgID, key, 100, input)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	var raw []byte
	if err := admin.QueryRow(ctx, `SELECT payload FROM ao_commands WHERE org_id = $1 AND idempotency_key = $2`, fixture.orgID, key).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["Interface"] != "tui" {
		t.Fatalf("stored payload Interface = %v, want tui (the pre-change payload)", stored["Interface"])
	}
	replay, err := store.CreateSession(ctx, principal, fixture.orgID, key, 100, input)
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %q, %v; want %q", replay.ID, err, first.ID)
	}
	// The same key with an explicit tui is the same request.
	input.Interface = domain.SessionInterfaceTUI
	if replay, err := store.CreateSession(ctx, principal, fixture.orgID, key, 100, input); err != nil || replay.ID != first.ID {
		t.Fatalf("explicit tui replay = %q, %v; want %q", replay.ID, err, first.ID)
	}
}

// A worker an orchestrator spawns starts in the orchestrator's interface when
// its harness can start there.
func TestCreateOrchestratorChildInheritsInterface(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	if _, err := admin.Exec(ctx, `UPDATE ao_sessions SET kind = 'orchestrator', interface = 'chat' WHERE id = $1`, fixture.sessionID); err != nil {
		t.Fatal(err)
	}
	spawn := func(harness string) domain.Session {
		t.Helper()
		child, err := store.CreateOrchestratorChild(ctx, fixture.orgID, fixture.sessionID, uuid.NewString(), 100, domain.CreateSession{
			Kind: "worker", Harness: harness, DisplayName: "child", Prompt: "go", Provider: "docker",
		})
		if err != nil {
			t.Fatal(err)
		}
		return child
	}
	if child := spawn("codex"); child.Interface != domain.SessionInterfaceChat {
		t.Fatalf("codex child of a Chat orchestrator = %q, want chat", child.Interface)
	} else if state, found, _ := initialTurn(t, admin, child.ID); !found || state != "queued" {
		t.Fatalf("chat child initial turn: state=%q found=%v", state, found)
	}
	if child := spawn("cursor"); child.Interface != domain.SessionInterfaceTUI {
		t.Fatalf("cursor child = %q, want tui", child.Interface)
	}
	if _, err := admin.Exec(ctx, `UPDATE ao_sessions SET interface = 'tui' WHERE id = $1`, fixture.sessionID); err != nil {
		t.Fatal(err)
	}
	if child := spawn("claude-code"); child.Interface != domain.SessionInterfaceTUI {
		t.Fatalf("claude child of a TUI orchestrator = %q, want tui", child.Interface)
	}
}
