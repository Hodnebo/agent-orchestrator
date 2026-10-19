package triage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// DiagnosticSession is a local binding candidate. Its scrubbed display name
// helps the human choose; prompts, project configuration and paths are excluded.
type DiagnosticSession struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Harness   string `json:"harness"`
	State     string `json:"state"`
	UpdatedAt string `json:"updatedAt"`
}

func discoverDiagnosticSessions(ctx context.Context, dataDir string) ([]DiagnosticSession, error) {
	db, err := sqlite.OpenReadOnly(ctx, dataDir)
	if err != nil {
		return nil, errors.New("the existing local session database is unavailable; no database was created or repaired")
	}
	defer func() { _ = db.Close() }()
	rows, err := db.ReadTriageRecentSessions(ctx)
	if err != nil {
		return nil, errors.New("recent local sessions could not be read; retained data may be unavailable or use an older schema")
	}
	sessions := make([]DiagnosticSession, 0, len(rows))
	for _, row := range rows {
		if row.ID.Truncated || !validRunID(row.ID.Value) {
			continue
		}
		harness, state, updated := "unknown", "unknown", ""
		if !row.Harness.Truncated && usableRetainedRepoName(row.Harness.Value) {
			harness = Redact(row.Harness.Value)
		}
		switch row.State.Value {
		case "active", "idle", "waiting_input", "blocked", "exited", "terminated":
			state = row.State.Value
		}
		if !row.UpdatedAt.Truncated {
			for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05.999999999-07:00"} {
				if at, err := time.Parse(layout, row.UpdatedAt.Value); err == nil {
					updated = at.UTC().Format(time.RFC3339Nano)
					break
				}
			}
		}
		label := strings.TrimSpace(Redact(row.DisplayName.Value))
		if label == "" {
			label = fmt.Sprintf("Recent local session %d", len(sessions)+1)
		}
		sessions = append(sessions, DiagnosticSession{ID: row.ID.Value, Label: label, Harness: harness, State: state, UpdatedAt: updated})
	}
	return sessions, nil
}

// validateDiagnosticSession limits a later model-selected binding to the small
// candidate list read from this host. A typed arbitrary ID is never a source root.
func validateDiagnosticSession(ctx context.Context, dataDir, id string) error {
	if !validRunID(id) {
		return errors.New("invalid local session candidate")
	}
	sessions, err := discoverDiagnosticSessions(ctx, dataDir)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if session.ID == id {
			return nil
		}
	}
	return errors.New("the selected session is not a recent local candidate")
}
