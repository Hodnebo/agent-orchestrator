package postgres

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/google/uuid"
)

// A Coder session's workspace name is chosen when the session is created,
// stored on its sandbox profile, and read back for the workspace link; a
// session created before names were stored reads back no name, so callers fall
// back to the computed one.
func TestCreateSessionStoresTheCoderWorkspaceName(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	principal := domain.Principal{UserID: fixture.userID, Provider: "local"}
	connection, err := store.UpsertProviderConnection(
		ctx, principal, fixture.orgID, "coder", "default", []byte("ciphertext"), []byte("nonce"),
		json.RawMessage(`{"baseUrl":"https://coder.acme.test","owner":"acme-bot","templateId":"3b3f373d-c42d-5313-a57e-b2abe56e2fe3"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	profile := json.RawMessage(`{"coder":{"baseUrl":"https://coder.acme.test","owner":"acme-bot","templateId":"3b3f373d-c42d-5313-a57e-b2abe56e2fe3","durableRoot":"$HOME","workspaceNamePrefix":"acme"}}`)
	storedName := func(sessionID string) string {
		t.Helper()
		var raw json.RawMessage
		if err := admin.QueryRow(ctx,
			`SELECT resource_profile FROM ao_sandboxes WHERE org_id = $1 AND session_id = $2`, fixture.orgID, sessionID,
		).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		decoded, err := sandbox.DecodeCoderSessionProfile(raw)
		if err != nil {
			t.Fatalf("decode stored profile %s: %v", raw, err)
		}
		if decoded.WorkspaceNameIssuedAt == nil {
			t.Fatalf("stored profile %s has no workspaceNameIssuedAt", raw)
		}
		return decoded.WorkspaceName
	}
	create := func(kind, displayName string) domain.Session {
		t.Helper()
		session, err := store.CreateSession(ctx, principal, fixture.orgID, "create-"+uuid.NewString(), 100, domain.CreateSession{
			ProjectID: fixture.projectID, Kind: kind, Harness: "codex", DisplayName: displayName,
			Provider: "coder", SandboxConnectionID: connection.ID,
			ResourceProfile: profile, BootstrapContext: json.RawMessage(`{}`), Release: "test",
		})
		if err != nil {
			t.Fatalf("create %s: %v", kind, err)
		}
		return session
	}

	orchestrator := create("orchestrator", "Orchestrator")
	if got, want := storedName(orchestrator.ID), domain.CoderWorkspaceName("acme", "orchestrator", "", orchestrator.ID); got != want || got != "acme-o-"+orchestrator.ID[:8] {
		t.Fatalf("orchestrator workspace name = %q, want %q", got, want)
	}
	worker := create("worker", "Fix the login bug")
	if got := storedName(worker.ID); got != "acme-w-fix-"+worker.ID[:8] {
		t.Fatalf("worker workspace name = %q, want acme-w-fix-%s", got, worker.ID[:8])
	}
	read, err := store.GetSession(ctx, principal, fixture.orgID, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.BYOCoderWorkspace == nil || read.BYOCoderWorkspace.Name != "acme-w-fix-"+worker.ID[:8] || read.BYOCoderWorkspace.NamePrefix != "acme" {
		t.Fatalf("workspace ref = %+v, want the stored name", read.BYOCoderWorkspace)
	}

	// A worker an orchestrator spawns is named the same way.
	child, err := store.CreateOrchestratorChild(ctx, fixture.orgID, orchestrator.ID, uuid.NewString(), 100, domain.CreateSession{
		Kind: "worker", Harness: "codex", DisplayName: "Add retries", Prompt: "go", Mode: "trusted", DeniedCommands: []string{},
		Provider: "coder", SandboxConnectionID: connection.ID,
		ResourceProfile: profile, BootstrapContext: json.RawMessage(`{}`), Release: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := storedName(child.ID); got != "acme-w-add-"+child.ID[:8] {
		t.Fatalf("child workspace name = %q, want acme-w-add-%s", got, child.ID[:8])
	}

	// A legacy row (written before names were stored) reads back no name.
	if _, err := admin.Exec(ctx,
		`UPDATE ao_sandboxes SET resource_profile = resource_profile #- '{coder,workspaceName}' #- '{coder,workspaceNameIssuedAt}'
		WHERE org_id = $1 AND session_id = $2`, fixture.orgID, worker.ID,
	); err != nil {
		t.Fatal(err)
	}
	legacy, err := store.GetSession(ctx, principal, fixture.orgID, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.BYOCoderWorkspace == nil || legacy.BYOCoderWorkspace.Name != "" || legacy.BYOCoderWorkspace.NamePrefix != "acme" {
		t.Fatalf("legacy workspace ref = %+v, want the prefix and no stored name", legacy.BYOCoderWorkspace)
	}
}
