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
)

func TestDiscoverDiagnosticSessionsReturnsRecentBoundedSafeCandidates(t *testing.T) {
	dir, id, workspace, _, raw := retainedContextFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	for n := range 16 {
		if _, err := raw.Exec(`INSERT INTO sessions
(id,project_id,num,harness,activity_state,activity_last_at,created_at,updated_at,display_name,prompt,workspace_path,is_terminated)
VALUES (?,'selected',?,'fake','blocked',?,?,?,'Checkout review token=ghp_abcdefghijklmnopqrstuvwxyz123456','private prompt',?,?)`,
			fmt.Sprintf("candidate-%02d", n), n+100, now, now, now.Add(time.Duration(n)*time.Second), workspace, n == 15); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := discoverDiagnosticSessions(context.Background(), dir)
	if err != nil || len(sessions) != 12 || sessions[0].ID != "candidate-15" || sessions[0].State != "terminated" || sessions[1].State != "blocked" || sessions[0].UpdatedAt == "" {
		t.Fatalf("recent candidates = %#v err=%v", sessions, err)
	}
	encoded, _ := json.Marshal(sessions)
	if !strings.HasPrefix(sessions[0].Label, "Checkout review") {
		t.Fatal("session choice lost its recognizable display name")
	}
	for _, private := range []string{"ghp_abcdefghijklmnopqrstuvwxyz123456", "private prompt", workspace} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("candidate labels leaked %q", private)
		}
	}
	if err := validateDiagnosticSession(context.Background(), dir, sessions[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{string(id), "candidate-00", "../candidate-15", "arbitrary-local-id"} {
		if err := validateDiagnosticSession(context.Background(), dir, invalid); err == nil {
			t.Fatalf("accepted an undiscovered binding %q", invalid)
		}
	}
}

func TestDiscoverDiagnosticSessionsUnavailableDoesNotCreateOrMigrate(t *testing.T) {
	for _, old := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "data")
		if old {
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
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
		before, _ := os.ReadFile(filepath.Join(dir, "ao.db"))
		if sessions, err := discoverDiagnosticSessions(context.Background(), dir); err == nil || len(sessions) != 0 {
			t.Fatalf("unavailable sessions were fabricated: %#v %v", sessions, err)
		}
		after, _ := os.ReadFile(filepath.Join(dir, "ao.db"))
		if string(before) != string(after) {
			t.Fatal("candidate discovery changed the database")
		}
		if !old {
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("candidate discovery created a missing data directory")
			}
		}
	}
}

func TestPrivateReadersReturnWithinBudgetWhenDatabaseLocked(t *testing.T) {
	for _, source := range []string{"sessions", "session-history", "provider-log"} {
		t.Run(source, func(t *testing.T) {
			dir := t.TempDir()
			db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "ao.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			if _, err := db.Exec(`CREATE TABLE sentinel (id TEXT); BEGIN EXCLUSIVE`); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			done := make(chan struct{})
			started := time.Now()
			_, err = within(ctx, func() (Evidence, error) {
				defer close(done)
				if source == "sessions" {
					_, err := discoverDiagnosticSessions(ctx, dir)
					return Evidence{}, err
				}
				return readDiagnosticContext(ctx, dir, Origin{SessionID: "selected"}, ContextRead{Source: source})
			})
			if err == nil || time.Since(started) > time.Second {
				t.Fatalf("locked read exceeded budget: %v", err)
			}
			if _, err := db.Exec(`ROLLBACK`); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("locked reader did not finish after release")
			}
		})
	}
}
