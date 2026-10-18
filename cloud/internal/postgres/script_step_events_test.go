package postgres

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/jackc/pgx/v5"
)

// Cleanup runs while a session is being deleted, and the control plane
// appends its steps afterwards: a deleted session (terminated, its sandbox's
// deletion requested and then completed) still takes events under the
// organization's RLS context, and its replay serves them.
func TestScriptStepEventsAppendToADeletedSession(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	principal := domain.Principal{UserID: fixture.userID, Provider: "local"}
	appendStep := func(step int, status string) {
		t.Helper()
		exitCode, duration := 0, int64(42)
		event := worker.ScriptStepEvent{
			Phase: worker.ScriptPhaseCleanup, RunID: "cleanup-run", Step: step, Total: 2,
			Command: "make down", Status: status, Output: "stopped",
		}
		if status != worker.ScriptStepSkipped {
			event.ExitCode, event.DurationMS = &exitCode, &duration
		}
		payload, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendSessionEvent(ctx, fixture.orgID, fixture.sessionID, worker.ScriptStepEventType, payload); err != nil {
			t.Fatalf("append step %d to the deleted session: %v", step, err)
		}
	}

	if err := store.TerminateSession(ctx, principal, fixture.orgID, fixture.sessionID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	// The admin pool is held to forced RLS too when it does not own the
	// tables as a superuser, so it reads and writes under the org's context.
	asOrg := func(fn func(pgx.Tx) error) {
		t.Helper()
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT set_config('ao.org_id', $1, true)`, fixture.orgID); err != nil {
			t.Fatal(err)
		}
		if err := fn(tx); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	asOrg(func(tx pgx.Tx) error {
		var desired string
		err := tx.QueryRow(ctx, `SELECT desired_state FROM ao_sandboxes WHERE session_id = $1`, fixture.sessionID).Scan(&desired)
		if err == nil && desired != "deleted" {
			t.Fatalf("sandbox desired state = %q, want deleted", desired)
		}
		return err
	})
	// While the reconciler deletes the sandbox.
	appendStep(1, worker.SetupSucceeded)
	// And after it completed the deletion.
	asOrg(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE ao_sandboxes SET observed_state = 'deleted', provider_environment_id = NULL
			WHERE session_id = $1`, fixture.sessionID)
		return err
	})
	appendStep(2, worker.ScriptStepSkipped)

	events, _, err := store.ListClientEvents(ctx, principal, fixture.orgID, fixture.sessionID, 0, 100)
	if err != nil {
		t.Fatalf("replay the deleted session's events: %v", err)
	}
	var steps []worker.ScriptStepEvent
	for _, event := range events {
		if event.Type != worker.ScriptStepEventType {
			continue
		}
		var step worker.ScriptStepEvent
		if err := json.Unmarshal(event.Payload, &step); err != nil {
			t.Fatal(err)
		}
		steps = append(steps, step)
	}
	if len(steps) != 2 || steps[0].Step != 1 || steps[0].Status != worker.SetupSucceeded ||
		steps[1].Step != 2 || steps[1].Status != worker.ScriptStepSkipped || steps[0].Phase != worker.ScriptPhaseCleanup {
		t.Fatalf("replayed steps = %+v", steps)
	}

	// Another organization's context sees no such session.
	if _, err := store.AppendSessionEvent(ctx, "00000000-0000-4000-8000-000000000999", fixture.sessionID,
		worker.ScriptStepEventType, json.RawMessage(`{}`)); err == nil {
		t.Fatal("an event was appended to the session under another organization's context")
	}
}
