package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

func TestRunningSetupReadsAsActive(t *testing.T) {
	stepPayload := func(phase, status string, step int) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"phase":%q,"runId":"run-1","step":%d,"total":2,"status":%q,"command":"make dev-up","output":""}`, phase, step, status))
	}
	appendEvent := func(t *testing.T, store *Store, fixture notificationFixture, eventType string, payload json.RawMessage) {
		t.Helper()
		if _, err := store.AppendSessionEvent(context.Background(), fixture.orgID, fixture.sessionID, eventType, payload); err != nil {
			t.Fatalf("append %s: %v", eventType, err)
		}
	}
	assertWorking := func(t *testing.T, store *Store, fixture notificationFixture) {
		t.Helper()
		assertCloudActivity(t, store, fixture, contract.ActivityActive)
		session, err := store.GetSession(context.Background(),
			domain.Principal{UserID: fixture.userID, Provider: "local"}, fixture.orgID, fixture.sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if status := session.Status(time.Now(), nil); status != contract.StatusWorking {
			t.Fatalf("status = %q, want working", status)
		}
	}

	t.Run("each running step reads as working until the run ends", func(t *testing.T) {
		store, _, fixture := openNotificationTestStore(t)
		assertCloudActivity(t, store, fixture, contract.ActivityIdle)
		appendEvent(t, store, fixture, "worker.ready", json.RawMessage(`{}`))
		appendEvent(t, store, fixture, "workspace.script.step", stepPayload("setup", "running", 1))
		assertWorking(t, store, fixture)
		// Live updates of a chatty step keep it working.
		for range 25 {
			appendEvent(t, store, fixture, "workspace.script.step", stepPayload("setup", "running", 1))
		}
		assertWorking(t, store, fixture)
		appendEvent(t, store, fixture, "workspace.script.step", stepPayload("setup", "succeeded", 1))
		appendEvent(t, store, fixture, "workspace.script.step", stepPayload("setup", "running", 2))
		assertWorking(t, store, fixture)
		appendEvent(t, store, fixture, "workspace.script.step", stepPayload("setup", "succeeded", 2))
		appendEvent(t, store, fixture, "workspace.setup", json.RawMessage(`{"status":"succeeded"}`))
		assertCloudActivity(t, store, fixture, contract.ActivityIdle)
	})

	t.Run("a failed step is not running", func(t *testing.T) {
		store, _, fixture := openNotificationTestStore(t)
		appendEvent(t, store, fixture, "workspace.script.step", stepPayload("setup", "running", 1))
		appendEvent(t, store, fixture, "workspace.script.step", stepPayload("setup", "failed", 1))
		assertCloudActivity(t, store, fixture, contract.ActivityIdle)
	})

	t.Run("a step whose worker is gone is not running", func(t *testing.T) {
		store, admin, fixture := openNotificationTestStore(t)
		appendEvent(t, store, fixture, "workspace.script.step", stepPayload("setup", "running", 1))
		execOneRowAsOrg(t, admin, fixture.orgID, `UPDATE ao_worker_connections SET disconnected_at = now()
			WHERE org_id = $1 AND session_id = $2`, fixture.orgID, fixture.sessionID)
		session, err := store.GetSession(context.Background(),
			domain.Principal{UserID: fixture.userID, Provider: "local"}, fixture.orgID, fixture.sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if session.ActivityState != contract.ActivityIdle {
			t.Fatalf("activity = %q, want idle", session.ActivityState)
		}
	})

	t.Run("a step the session moved past is not running", func(t *testing.T) {
		store, _, fixture := openNotificationTestStore(t)
		// Its final event was lost, but the agent started after it.
		appendEvent(t, store, fixture, "workspace.script.step", stepPayload("setup", "running", 1))
		appendEvent(t, store, fixture, "agent.ready", json.RawMessage(`{}`))
		assertCloudActivity(t, store, fixture, contract.ActivityIdle)
	})

	t.Run("a running Cleanup step does not count as Setup", func(t *testing.T) {
		store, _, fixture := openNotificationTestStore(t)
		appendEvent(t, store, fixture, "workspace.script.step", stepPayload("cleanup", "running", 1))
		assertCloudActivity(t, store, fixture, contract.ActivityIdle)
	})

	t.Run("Setup leaves the stored activity, which idle pause reads, alone", func(t *testing.T) {
		store, admin, fixture := openNotificationTestStore(t)
		appendEvent(t, store, fixture, "workspace.script.step", stepPayload("setup", "running", 1))
		assertWorking(t, store, fixture)
		var stored string
		withOrgTx(t, admin, fixture.orgID, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), `SELECT activity_state FROM ao_sessions
				WHERE org_id = $1 AND id = $2`, fixture.orgID, fixture.sessionID).Scan(&stored)
		})
		if stored != string(contract.ActivityIdle) {
			t.Fatalf("stored activity = %q, want idle", stored)
		}
	})
}
