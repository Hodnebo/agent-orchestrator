package triage

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func retainedContextFixture(t *testing.T) (string, domain.SessionID, string, string, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC()
	if err := db.UpsertProject(context.Background(), domain.ProjectRecord{ID: "selected", Path: t.TempDir(), RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	workspace, child := t.TempDir(), t.TempDir()
	session, err := db.CreateSession(context.Background(), domain.SessionRecord{ProjectID: "selected", Kind: domain.KindWorker, Harness: domain.HarnessFake,
		Mode: domain.SessionModeChat, Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: now}, CreatedAt: now, UpdatedAt: now,
		ProvisionState: domain.SessionProvisionFailed, ProvisionError: "Failed to open " + workspace + ": token=ghp_abcdefghijklmnopqrstuvwx",
		Metadata: domain.SessionMetadata{WorkspacePath: workspace, Prompt: strings.Repeat("private task prompt ", 1000)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertSessionWorktree(context.Background(), domain.SessionWorktreeRecord{SessionID: session.ID, RepoName: "child", WorktreePath: child}); err != nil {
		t.Fatal(err)
	}
	file := url.URL{Path: filepath.Join(dir, "ao.db")}
	raw, err := sql.Open("sqlite", "file:"+file.EscapedPath()+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return dir, session.ID, workspace, child, raw
}

func TestRetainedContextKeepsRootsPrivateAndMasksEmbeddedPaths(t *testing.T) {
	dir, id, workspace, child, _ := retainedContextFixture(t)
	result, err := readRetainedContext(context.Background(), dir, Origin{SessionID: string(id)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Roots["workspace"] != workspace || result.Roots["workspace:child"] != child {
		t.Fatalf("private file-source references missing: %#v", result.Roots)
	}
	encoded, _ := json.Marshal(result)
	for _, forbidden := range []string{workspace, child, "private task prompt", "ghp_abcdefghijklmnopqrstuvwx"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("retained evidence leaked private value %q: %s", forbidden, encoded)
		}
	}
	if !strings.Contains(string(encoded), "[workspace]") || !strings.Contains(string(encoded), "fake") {
		t.Fatalf("meaningful failure or agent fact lost: %s", encoded)
	}
	for _, source := range []string{"workspace", "workspace:child", "session-history", "daemon-log", "provider-log"} {
		if !strings.Contains(string(encoded), `"`+source+`"`) {
			t.Fatalf("opaque available source ID %s was omitted: %s", source, encoded)
		}
	}
}

func TestRetainedContextBudgetDoesNotWaitForLockedDatabase(t *testing.T) {
	dir := t.TempDir()
	file := url.URL{Path: filepath.Join(dir, "ao.db")}
	db, err := sql.Open("sqlite", "file:"+file.EscapedPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE sentinel(id TEXT); BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	started := time.Now()
	_, readErr := within(ctx, func() (retainedContext, error) {
		defer close(done)
		return readRetainedContext(ctx, dir, Origin{SessionID: "affected"})
	})
	if readErr == nil || time.Since(started) > time.Second {
		t.Fatalf("locked context read exceeded collection budget: %v", readErr)
	}
	if _, err := db.Exec(`ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled reader did not close after lock release")
	}
}

func TestRetainedContextSkipsRemoteCloudAndMismatchedIdentity(t *testing.T) {
	for _, origin := range []Origin{
		{SessionID: "collision", HostID: "remote-host"},
		{SessionID: "collision", Session: json.RawMessage(`{"id":"collision","hostId":"remote-host"}`)},
		{SessionID: "collision", Session: json.RawMessage(`{"id":"collision","cloud":{"orgId":"remote"}}`)},
		{SessionID: "collision", Session: json.RawMessage(`not-json`)},
		{SessionID: "collision", Session: json.RawMessage(`{"id":"different"}`)},
		{SessionID: "collision", Session: json.RawMessage(`{"id":42}`)},
	} {
		dir := filepath.Join(t.TempDir(), "must-not-create")
		result, _ := readRetainedContext(context.Background(), dir, origin)
		if len(result.Roots) != 0 || len(result.Evidence) != 1 || result.Evidence[0].Status != "unavailable" {
			t.Fatalf("nonlocal identity read retained facts: %#v", result)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("identity rejection touched local data directory: %v", err)
		}
	}
}

func TestRetainedContextUnavailableDatabaseNeverCreatesOrRepairs(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt", "old-schema", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			ctx := context.Background()
			if kind == "corrupt" || kind == "old-schema" {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if kind == "corrupt" {
					if err := os.WriteFile(filepath.Join(dir, "ao.db"), []byte("not a SQLite database"), 0o600); err != nil {
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
			if kind == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			result, err := readRetainedContext(ctx, dir, Origin{SessionID: "selected"})
			if err == nil || len(result.Roots) != 0 || len(result.Evidence) != 1 || result.Evidence[0].Status != "unavailable" {
				t.Fatalf("unavailable database was hidden or repaired: %#v err=%v", result, err)
			}
			if kind == "missing" || kind == "cancelled" {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("read-only failure created data directory: %v", err)
				}
			}
		})
	}
}

func TestRetainedContextPreservesFactsWhenTurnTableUnavailable(t *testing.T) {
	dir, id, workspace, _, raw := retainedContextFixture(t)
	if _, err := raw.Exec(`DROP TABLE conversation_turns`); err != nil {
		t.Fatal(err)
	}
	result, err := readRetainedContext(context.Background(), dir, Origin{SessionID: string(id)})
	if err == nil || result.Roots["workspace"] != workspace {
		t.Fatalf("partial retained facts lost: roots=%#v err=%v", result.Roots, err)
	}
	var session, turn bool
	for _, evidence := range result.Evidence {
		session = session || evidence.ID == "retained-session" && evidence.Status == "collected"
		turn = turn || evidence.ID == "retained-turn" && evidence.Status == "unavailable"
	}
	if !session || !turn {
		t.Fatalf("partial evidence was not honest: %#v", result.Evidence)
	}
}

func TestRetainedRootReferencesRejectUnsafeOrTruncatedPaths(t *testing.T) {
	for _, path := range []string{"", "relative", string(filepath.Separator), "bad\x00path"} {
		if usableRetainedRoot(path) {
			t.Fatalf("unsafe retained root accepted: %q", path)
		}
	}
	dir, id, _, _, raw := retainedContextFixture(t)
	if _, err := raw.Exec(`UPDATE sessions SET workspace_path=? WHERE id=?`, strings.Repeat("/long", 2000), id); err != nil {
		t.Fatal(err)
	}
	result, err := readRetainedContext(context.Background(), dir, Origin{SessionID: string(id)})
	if err != nil || result.Roots["workspace"] != "" {
		t.Fatalf("truncated path was used as a root: %#v err=%v", result.Roots, err)
	}
}
