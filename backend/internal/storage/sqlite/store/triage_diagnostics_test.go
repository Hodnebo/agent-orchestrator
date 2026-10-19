package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func triageDiagnosticFixture(t *testing.T) (*sqlite.Store, *sqlite.Store, *sql.DB, domain.SessionRecord) {
	t.Helper()
	dir := t.TempDir()
	writable, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writable.Close() })
	seedProject(t, writable, "triage")
	record := sampleRecord("triage")
	record.Mode = domain.SessionModeChat
	record.ProvisionState = domain.SessionProvisionFailed
	record.ProvisionError = strings.Repeat("é", 5000)
	record.Metadata.Prompt = strings.Repeat("private task input ", 10000)
	record.Metadata.WorkspacePath = "/private/selected/workspace"
	selected, err := writable.CreateSession(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	file := url.URL{Path: filepath.Join(dir, "ao.db")}
	raw, err := sql.Open("sqlite", "file:"+file.EscapedPath()+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	readOnly, err := sqlite.OpenReadOnly(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readOnly.Close() })
	return writable, readOnly, raw, selected
}

func TestTriageDiagnosticsBoundFieldsBeforeReturningPrivateSessionFacts(t *testing.T) {
	writable, readOnly, raw, selected := triageDiagnosticFixture(t)
	if _, err := raw.Exec(`UPDATE sessions SET updated_at=? WHERE id=?`, strings.Repeat("timestamp", 2000), selected.ID); err != nil {
		t.Fatal(err)
	}
	facts, found, err := readOnly.ReadTriageSessionFacts(context.Background(), selected.ID)
	if err != nil || !found {
		t.Fatalf("read facts: found=%v err=%v", found, err)
	}
	if len(facts.ProvisionError.Value) != 4096 || !facts.ProvisionError.Truncated || len(facts.UpdatedAt.Value) != 64 || !facts.UpdatedAt.Truncated {
		t.Fatalf("SQL field bounds were not applied: error=%d/%v updated=%d/%v", len(facts.ProvisionError.Value), facts.ProvisionError.Truncated, len(facts.UpdatedAt.Value), facts.UpdatedAt.Truncated)
	}
	encoded, _ := json.Marshal(facts)
	if strings.Contains(string(encoded), "private task input") || facts.Harness.Value != string(selected.Harness) {
		t.Fatalf("diagnostic projection included private inputs or changed agent: %s", encoded)
	}
	for n := range 12 {
		name := fmt.Sprintf("repo-%02d", n)
		path := fmt.Sprintf("/worktrees/repo-%02d", n)
		if n == 0 {
			name = domain.RootWorkspaceRepoName
			path = strings.Repeat("/long", 2000)
		}
		if err := writable.UpsertSessionWorktree(context.Background(), domain.SessionWorktreeRecord{SessionID: selected.ID, RepoName: name, WorktreePath: path}); err != nil {
			t.Fatal(err)
		}
	}
	roots, omitted, err := readOnly.ReadTriageWorkspaceRoots(context.Background(), selected.ID)
	if err != nil || len(roots) != 8 || !omitted {
		t.Fatalf("unbounded roots: count=%d omitted=%v err=%v", len(roots), omitted, err)
	}
	if roots[0].Name.Value != domain.RootWorkspaceRepoName || len(roots[0].Path.Value) != 4096 || !roots[0].Path.Truncated {
		t.Fatalf("root-first order or path bound lost: %#v", roots[0])
	}
	if _, found, err := readOnly.ReadTriageSessionFacts(context.Background(), "absent"); err != nil || found {
		t.Fatalf("missing session fabricated: found=%v err=%v", found, err)
	}
}

func TestTriageLatestTurnExcludesOtherOwnersAndBoundsError(t *testing.T) {
	writable, readOnly, raw, selected := triageDiagnosticFixture(t)
	other, err := writable.CreateSession(context.Background(), sampleRecord("triage"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	conversation, err := writable.CreateConversation(context.Background(), "shared-triage", domain.ConversationScopeProject, "triage", selected.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range []struct {
		id, owner, failure string
		at                 time.Time
	}{
		{"selected-turn", string(selected.ID), strings.Repeat("selected failure ", 2000), now},
		{"other-turn", string(other.ID), "unrelated private failure", now.Add(time.Second)},
	} {
		if _, err := raw.Exec(`INSERT INTO conversation_turns
(id,conversation_id,handled_by_session_id,state,error_message,requested_at)
VALUES (?,?,?,'failed',?,?)`, turn.id, conversation.ID, turn.owner, turn.failure, turn.at); err != nil {
			t.Fatal(err)
		}
	}
	facts, found, err := readOnly.ReadTriageLatestTurn(context.Background(), selected.ID)
	if err != nil || !found || facts.State.Value != "failed" || !facts.Error.Truncated || len(facts.Error.Value) != 4096 {
		t.Fatalf("owned latest turn unavailable or unbounded: %#v found=%v err=%v", facts, found, err)
	}
	if strings.Contains(facts.Error.Value, "unrelated") || !strings.HasPrefix(facts.Error.Value, "selected failure") {
		t.Fatal("shared conversation leaked another session's error")
	}
	if _, err := raw.Exec(`DELETE FROM conversation_turns WHERE handled_by_session_id=?`, selected.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := readOnly.ReadTriageLatestTurn(context.Background(), selected.ID); err != nil || found {
		t.Fatalf("another owner's turn replaced the missing selected turn: found=%v err=%v", found, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := readOnly.ReadTriageLatestTurn(cancelled, selected.ID); err == nil {
		t.Fatal("cancelled diagnostic read succeeded")
	}
}

func TestTriageHistoryReadsProseAndToolsOnlyForExactTurnOwner(t *testing.T) {
	writable, readOnly, raw, selected := triageDiagnosticFixture(t)
	ctx := context.Background()
	other, err := writable.CreateSession(ctx, sampleRecord("triage"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	conversation, err := writable.CreateConversation(ctx, "owned-history", domain.ConversationScopeProject, "triage", selected.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []domain.SessionID{selected.ID, other.ID} {
		if _, err := raw.Exec(`INSERT INTO conversation_turns (id,conversation_id,handled_by_session_id,state,requested_at)
VALUES (?,?,?,'completed',?)`, "turn-"+string(owner), conversation.ID, owner, now); err != nil {
			t.Fatal(err)
		}
	}
	for n, message := range []struct{ owner, role, text string }{
		{string(selected.ID), "user", "The preview stopped after saving."},
		{string(selected.ID), "assistant", "The save returned a connection error."},
		{string(other.ID), "user", "unrelated confidential conversation"},
	} {
		if _, err := raw.Exec(`INSERT INTO conversation_messages
(id,conversation_id,turn_id,sequence,role,origin,text,created_at,updated_at) VALUES (?,?,?,?,?,'human',?,?,?)`,
			fmt.Sprintf("message-%d", n), conversation.ID, "turn-"+message.owner, n+1, message.role, message.text, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec(`INSERT INTO conversation_activities
(id,conversation_id,turn_id,sequence,kind,status,summary,detail_json,command_output,streamed_text,created_at,updated_at)
VALUES ('tool',?,?,4,'command','failed','curl preview',?,?,'connection refused',?,?)`,
		conversation.ID, "turn-"+string(selected.ID), `{"command":"curl preview"}`, strings.Repeat("tool-output ", 2000), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO conversation_messages
(id,conversation_id,sequence,role,origin,text,created_at,updated_at)
VALUES ('unowned',?,5,'user','human','unowned private input',?,?)`, conversation.ID, now, now); err != nil {
		t.Fatal(err)
	}
	// A replacement controller must not hide the selected session's own history.
	if _, err := raw.Exec(`UPDATE conversations SET current_session_id=? WHERE id=?`, other.ID, conversation.ID); err != nil {
		t.Fatal(err)
	}
	entries, more, err := readOnly.ReadTriageSessionHistory(ctx, selected.ID, 0)
	if err != nil || more || len(entries) != 3 {
		t.Fatalf("owned history: count=%d more=%v err=%v", len(entries), more, err)
	}
	if entries[0].Kind.Value != "command" || !entries[0].Output.Truncated || len(entries[0].Output.Value) != 2048 || entries[0].Detail.Value != `{"command":"curl preview"}` {
		t.Fatalf("actual bounded tool activity missing: %#v", entries[0])
	}
	if entries[1].Role.Value != "assistant" || entries[2].Role.Value != "user" || !strings.Contains(entries[2].Text.Value, "preview stopped") {
		t.Fatalf("actual conversation text missing: %#v", entries)
	}
	encoded, _ := json.Marshal(entries)
	if strings.Contains(string(encoded), "confidential") || strings.Contains(string(encoded), "unowned private") {
		t.Fatal("history crossed session ownership")
	}
}

func TestTriageHistoryAndProviderArchivePageAndBoundBeforeAllocation(t *testing.T) {
	writable, readOnly, raw, selected := triageDiagnosticFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	conversation, err := writable.CreateConversation(ctx, "paged-history", domain.ConversationScopeSession, "triage", selected.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO conversation_turns (id,conversation_id,handled_by_session_id,state,requested_at)
VALUES ('paged-turn',?,?,'completed',?)`, conversation.ID, selected.ID, now); err != nil {
		t.Fatal(err)
	}
	for n := range 18 {
		if _, err := raw.Exec(`INSERT INTO conversation_messages
(id,conversation_id,turn_id,sequence,role,origin,text,created_at,updated_at)
VALUES (?,?,'paged-turn',?,'user','human',?,?,?)`, fmt.Sprintf("page-%d", n), conversation.ID, n+1, strings.Repeat("z", 20000), now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(`INSERT INTO conversation_provider_events (conversation_id,session_id,method,payload_json,received_at)
VALUES (?,?,'tool.call',?,?)`, conversation.ID, selected.ID, strings.Repeat("z", 20000), now); err != nil {
			t.Fatal(err)
		}
	}
	entries, more, err := readOnly.ReadTriageSessionHistory(ctx, selected.ID, 0)
	if err != nil || !more || len(entries) != 8 || entries[0].Sequence != 18 || len(entries[0].Text.Value) != 4096 || !entries[0].Text.Truncated {
		t.Fatalf("history bounds: count=%d more=%v err=%v", len(entries), more, err)
	}
	page, more, err := readOnly.ReadTriageSessionHistory(ctx, selected.ID, 16)
	if err != nil || more || len(page) != 2 || page[0].Sequence != 2 {
		t.Fatalf("history next page: %#v more=%v err=%v", page, more, err)
	}
	events, more, err := readOnly.ReadTriageProviderEvents(ctx, selected.ID, 0)
	if err != nil || !more || len(events) != 6 || len(events[0].Payload.Value) != 8192 || !events[0].Payload.Truncated {
		t.Fatalf("provider bounds: count=%d more=%v err=%v", len(events), more, err)
	}
	if _, _, err := readOnly.ReadTriageSessionHistory(ctx, selected.ID, 257); err == nil {
		t.Fatal("accepted an unbounded history offset")
	}
	if _, _, err := readOnly.ReadTriageProviderEvents(ctx, selected.ID, -1); err == nil {
		t.Fatal("accepted an invalid provider offset")
	}
	other, err := writable.CreateSession(ctx, sampleRecord("triage"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO conversation_provider_events (conversation_id,session_id,method,payload_json,received_at)
VALUES (?,?,'private-other-event','{}',?)`, conversation.ID, other.ID, now); err != nil {
		t.Fatal(err)
	}
	events, _, err = readOnly.ReadTriageProviderEvents(ctx, selected.ID, 0)
	if err != nil || events[0].Method.Value != "tool.call" {
		t.Fatal("provider archive crossed session ownership")
	}
}
