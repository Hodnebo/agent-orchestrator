package triage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func seedDiagnosticHistory(t *testing.T, raw *sql.DB, id domain.SessionID) {
	t.Helper()
	now := time.Now().UTC()
	queries := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO conversations (id,scope,project_id,session_id,current_session_id,created_at,updated_at)
VALUES ('private-history','session','selected',?,?,?,?)`, []any{id, id, now, now}},
		{`INSERT INTO conversation_turns (id,conversation_id,handled_by_session_id,state,requested_at)
VALUES ('private-turn','private-history',?,'failed',?)`, []any{id, now}},
		{`INSERT INTO conversation_messages
(id,conversation_id,turn_id,sequence,role,origin,text,created_at,updated_at)
VALUES ('human','private-history','private-turn',1,'user','human',?,?,?)`, []any{"Preview failed when I saved. api_key=human-secret", now, now}},
		{`INSERT INTO conversation_messages
(id,conversation_id,turn_id,sequence,role,origin,text,created_at,updated_at)
VALUES ('assistant','private-history','private-turn',2,'assistant','provider',?,?,?)`, []any{"The preview connection was refused.", now, now}},
		{`INSERT INTO conversation_activities
(id,conversation_id,turn_id,sequence,kind,status,summary,detail_json,command_output,created_at,updated_at)
VALUES ('tool','private-history','private-turn',3,'command','failed','curl preview',?,?,?,?)`, []any{`{"command":"curl preview","credentials":{"custom":"nested-tool-secret"}}`, "connection refused password=output-secret", now, now}},
		{`INSERT INTO conversation_provider_events (conversation_id,session_id,method,payload_json,received_at)
VALUES ('private-history',?,'tool.failed',?,?)`, []any{id, `{"message":"connection refused","auth":{"token":"provider-secret"}}`, now}},
	}
	for _, query := range queries {
		if _, err := raw.Exec(query.query, query.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPrivateSessionHistoryIncludesActualConversationAndToolEvents(t *testing.T) {
	dir, id, _, _, raw := retainedContextFixture(t)
	seedDiagnosticHistory(t, raw, id)
	item, err := readDiagnosticContext(context.Background(), dir, Origin{SessionID: string(id)}, ContextRead{Source: "session-history"})
	if err != nil || item.Status != "collected" || item.Values["nextOffset"] != int64(3) {
		t.Fatalf("private history = %#v err=%v", item, err)
	}
	encoded, _ := json.Marshal(item)
	for _, required := range []string{"Preview failed when I saved", "The preview connection was refused", "curl preview", "connection refused", `"role":"user"`, `"role":"assistant"`, `"privateContext":true`} {
		if !strings.Contains(string(encoded), required) {
			t.Fatalf("actual history omitted %q: %s", required, encoded)
		}
	}
	for _, secret := range []string{"human-secret", "nested-tool-secret", "output-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("private history did not redact %q", secret)
		}
	}
	public, err := readRetainedContext(context.Background(), dir, Origin{SessionID: string(id)})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(public.Evidence)
	if strings.Contains(string(encoded), "Preview failed when I saved") || strings.Contains(string(encoded), "curl preview") {
		t.Fatal("actual private history entered public retained evidence")
	}
	provider, err := readDiagnosticContext(context.Background(), dir, Origin{SessionID: string(id)}, ContextRead{Source: "provider-log"})
	encoded, _ = json.Marshal(provider)
	if err != nil || provider.Status != "collected" || !strings.Contains(string(encoded), "tool.failed") || strings.Contains(string(encoded), "provider-secret") {
		t.Fatalf("private provider archive = %s err=%v", encoded, err)
	}
}

func TestPrivateHistoryChunksHaveRecordAndByteLimits(t *testing.T) {
	dir, id, _, _, raw := retainedContextFixture(t)
	seedDiagnosticHistory(t, raw, id)
	now := time.Now().UTC()
	for n := range 15 {
		if _, err := raw.Exec(`INSERT INTO conversation_messages
(id,conversation_id,turn_id,sequence,role,origin,text,created_at,updated_at)
VALUES (?,'private-history','private-turn',?,'user','human',?,?,?)`, fmt.Sprintf("large-%d", n), n+4, strings.Repeat("x", collectMaxBytes*2), now, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, offset := range []int64{0, 8, 16} {
		item, err := readDiagnosticContext(context.Background(), dir, Origin{SessionID: string(id)}, ContextRead{Source: "session-history", Offset: offset})
		if err != nil || len(item.Values["entries"].([]any)) > 8 || item.Values["offsetKind"] != "records" {
			t.Fatalf("history chunk %d = %#v err=%v", offset, item, err)
		}
		encoded, _ := json.Marshal(item)
		if len(encoded) > collectMaxBytes {
			t.Fatalf("history chunk exceeded its byte bound: %d", len(encoded))
		}
	}
	for _, request := range []ContextRead{
		{Source: "session-history", Path: "../ao.db"},
		{Source: "session-history", List: true},
		{Source: "session-history", Offset: -1},
		{Source: "session-history", Offset: 257},
		{Source: "provider-log", Path: "archive", Offset: 257},
	} {
		if _, err := readDiagnosticContext(context.Background(), dir, Origin{SessionID: string(id)}, request); err == nil {
			t.Fatalf("invalid private history request was accepted: %#v", request)
		}
	}
}

func TestPrivateHistoryBoundsEncodedJSONWithoutSkippingRecords(t *testing.T) {
	dir, id, _, _, raw := retainedContextFixture(t)
	seedDiagnosticHistory(t, raw, id)
	now := time.Now().UTC()
	for n := range 10 {
		if _, err := raw.Exec(`INSERT INTO conversation_messages
(id,conversation_id,turn_id,sequence,role,origin,text,created_at,updated_at)
VALUES (?,'private-history','private-turn',?,'user','human',?,?,?)`, fmt.Sprintf("escaped-%d", n), n+4, strings.Repeat("<", 4096), now, now); err != nil {
			t.Fatal(err)
		}
		payload := `{"message":"` + strings.Repeat("<", 6000) + `"}`
		if _, err := raw.Exec(`INSERT INTO conversation_provider_events (conversation_id,session_id,method,payload_json,received_at)
VALUES ('private-history',?,'tool.output',?,?)`, id, payload, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, source := range []string{"session-history", "provider-log"} {
		item, err := readDiagnosticContext(context.Background(), dir, Origin{SessionID: string(id)}, ContextRead{Source: source})
		if err != nil || item.Status != "truncated" {
			t.Fatalf("encoded %s page = %#v %v", source, item, err)
		}
		encoded, _ := json.Marshal(item)
		entries := item.Values["entries"].([]any)
		if len(encoded) > collectMaxBytes || len(entries) == 0 || item.Values["nextOffset"] != int64(len(entries)) {
			t.Fatalf("encoded %s page exceeded budget or skipped records: bytes=%d count=%d next=%v", source, len(encoded), len(entries), item.Values["nextOffset"])
		}
	}
}

func TestPrivateRetainedSourcesRejectRemoteCloudAndMismatchedOwners(t *testing.T) {
	for _, source := range []string{"session-history", "provider-log", "daemon-log", "session-hook-errors"} {
		for _, origin := range []Origin{
			{SessionID: "selected", HostID: "remote"},
			{SessionID: "selected", Session: []byte(`{"id":"selected","hostId":"remote"}`)},
			{SessionID: "selected", Session: []byte(`{"id":"selected","cloud":{"orgId":"remote"}}`)},
		} {
			dir := filepath.Join(t.TempDir(), "uncreated")
			if _, err := readDiagnosticContext(context.Background(), dir, origin, ContextRead{Source: source}); err == nil {
				t.Fatalf("%s accepted nonlocal context", source)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("nonlocal read touched the local data directory")
			}
		}
	}
	for _, source := range []string{"session-history", "provider-log", "session-hook-errors"} {
		if _, err := readDiagnosticContext(context.Background(), t.TempDir(), Origin{SessionID: "selected", Session: []byte(`{"id":"different"}`)}, ContextRead{Source: source}); err == nil {
			t.Fatalf("%s accepted a mismatched session snapshot", source)
		}
	}
}

func TestPrivateHistoryUnavailableDatabaseNeverCreatesOrRepairs(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt", "old-schema"} {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			if kind != "missing" {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if kind == "corrupt" {
					if err := os.WriteFile(filepath.Join(dir, "ao.db"), []byte("invalid retained database"), 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "ao.db"))
					if err != nil {
						t.Fatal(err)
					}
					_, err = db.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY); INSERT INTO sessions VALUES ('selected')`)
					_ = db.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			before, _ := os.ReadFile(filepath.Join(dir, "ao.db"))
			for _, source := range []string{"session-history", "provider-log"} {
				item, err := readDiagnosticContext(context.Background(), dir, Origin{SessionID: "selected"}, ContextRead{Source: source})
				if err != nil || item.Status != "unavailable" {
					t.Fatalf("%s unavailable result=%#v err=%v", source, item, err)
				}
			}
			after, _ := os.ReadFile(filepath.Join(dir, "ao.db"))
			if string(before) != string(after) {
				t.Fatal("private context modified or repaired the database")
			}
			if kind == "missing" {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatal("private context created the data directory")
				}
			}
		})
	}
}
