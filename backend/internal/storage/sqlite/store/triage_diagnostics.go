package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// TriageDiagnosticText is bounded by SQLite before the driver allocates it.
type TriageDiagnosticText struct {
	Value     string
	Truncated bool
}

// TriageSessionFacts excludes prompts, project configuration and provider data.
// WorkspacePath is a private file-source reference, never report evidence.
type TriageSessionFacts struct {
	Harness, Mode, Activity, ProvisionState TriageDiagnosticText
	ProvisionError, WorkspacePath           TriageDiagnosticText
	CreatedAt, UpdatedAt, ActivityLastAt    TriageDiagnosticText
	IsTerminated                            bool
}

// TriageTurnFacts describes one retained turn owned by the selected session.
type TriageTurnFacts struct {
	State, Error, RequestedAt, StartedAt, CompletedAt TriageDiagnosticText
}

// TriageWorkspaceRoot is a private registered root, not public evidence.
type TriageWorkspaceRoot struct {
	Name, Path TriageDiagnosticText
}

// TriageRecentSession contains a bounded display name, never a prompt or path.
// Sessions in this database are local; remote/cloud sessions are not stored here.
type TriageRecentSession struct {
	ID, DisplayName, Harness, State, UpdatedAt TriageDiagnosticText
}

// TriageHistoryPageSize bounds retained conversation pages.
const TriageHistoryPageSize = 8

// TriageProviderPageSize bounds retained provider event pages.
const TriageProviderPageSize = 6

// TriageMaxHistoryOffset bounds record pagination for one investigation.
const TriageMaxHistoryOffset = 256

// TriageHistoryEntry is private investigation context, never public evidence.
type TriageHistoryEntry struct {
	Sequence                                int64
	Kind, Role, State, TurnState, CreatedAt TriageDiagnosticText
	Text, Detail, Output, Streamed          TriageDiagnosticText
	RolledBack                              bool
}

// TriageProviderEvent is a bounded private provider archive entry.
type TriageProviderEvent struct {
	Method, Payload, ReceivedAt TriageDiagnosticText
}

// ReadTriageRecentSessions discovers at most twelve local candidate identities.
func (s *Store) ReadTriageRecentSessions(ctx context.Context) ([]TriageRecentSession, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT
COALESCE(CAST(substr(CAST(id AS BLOB),1,255) AS TEXT),''), length(CAST(id AS BLOB))>255,
COALESCE(CAST(substr(CAST(display_name AS BLOB),1,240) AS TEXT),''), length(CAST(display_name AS BLOB))>240,
COALESCE(CAST(substr(CAST(harness AS BLOB),1,64) AS TEXT),''), length(CAST(harness AS BLOB))>64,
COALESCE(CAST(substr(CAST(CASE WHEN is_terminated THEN 'terminated' ELSE activity_state END AS BLOB),1,64) AS TEXT),''),
length(CAST(activity_state AS BLOB))>64,
COALESCE(CAST(substr(CAST(updated_at AS BLOB),1,64) AS TEXT),''), length(CAST(updated_at AS BLOB))>64
FROM sessions ORDER BY updated_at DESC, id DESC LIMIT 12`)
	if err != nil {
		return nil, fmt.Errorf("read recent diagnostic sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var sessions []TriageRecentSession
	for rows.Next() {
		var session TriageRecentSession
		if err := rows.Scan(&session.ID.Value, &session.ID.Truncated, &session.DisplayName.Value, &session.DisplayName.Truncated, &session.Harness.Value, &session.Harness.Truncated,
			&session.State.Value, &session.State.Truncated, &session.UpdatedAt.Value, &session.UpdatedAt.Truncated); err != nil {
			return nil, fmt.Errorf("scan recent diagnostic session: %w", err)
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

// ReadTriageSessionHistory reads actual prose and tool activities, newest first.
// Both the turn owner and the conversation join must match. This also preserves
// an old session's own history after a project conversation changes controllers.
// Ordinary conversation readers may reconcile authentication, so they cannot be
// used by this read-only recovery path. Bounds apply before Go allocates text.
func (s *Store) ReadTriageSessionHistory(ctx context.Context, id domain.SessionID, offset int64) ([]TriageHistoryEntry, bool, error) {
	if offset < 0 || offset > TriageMaxHistoryOffset {
		return nil, false, errors.New("invalid diagnostic history offset")
	}
	rows, err := s.readDB.QueryContext(ctx, `WITH owned AS (
 SELECT id, conversation_id, requested_at, rowid AS turn_order, state AS turn_state,
        rolled_back_at IS NOT NULL AS rolled_back
 FROM conversation_turns WHERE handled_by_session_id=? AND promoted_to_turn_id IS NULL
), timeline AS (
 SELECT m.sequence, 'message' AS kind, m.role, '' AS state, t.turn_state,
        m.text, '' AS detail, '' AS output, '' AS streamed, m.created_at,
        t.rolled_back, t.requested_at, t.turn_order
 FROM conversation_messages m JOIN owned t ON m.turn_id=t.id AND m.conversation_id=t.conversation_id
 UNION ALL
 SELECT a.sequence, a.kind, '', a.status, t.turn_state,
        a.summary, a.detail_json, a.command_output, a.streamed_text, a.created_at,
        t.rolled_back, t.requested_at, t.turn_order
 FROM conversation_activities a JOIN owned t ON a.turn_id=t.id AND a.conversation_id=t.conversation_id
)
SELECT sequence,
COALESCE(CAST(substr(CAST(kind AS BLOB),1,64) AS TEXT),''), length(CAST(kind AS BLOB))>64,
COALESCE(CAST(substr(CAST(role AS BLOB),1,64) AS TEXT),''), length(CAST(role AS BLOB))>64,
COALESCE(CAST(substr(CAST(state AS BLOB),1,64) AS TEXT),''), length(CAST(state AS BLOB))>64,
COALESCE(CAST(substr(CAST(turn_state AS BLOB),1,64) AS TEXT),''), length(CAST(turn_state AS BLOB))>64,
COALESCE(CAST(substr(CAST(text AS BLOB),1,4096) AS TEXT),''), length(CAST(text AS BLOB))>4096,
COALESCE(CAST(substr(CAST(detail AS BLOB),1,2048) AS TEXT),''), length(CAST(detail AS BLOB))>2048,
COALESCE(CAST(substr(CAST(output AS BLOB),1,2048) AS TEXT),''), length(CAST(output AS BLOB))>2048,
COALESCE(CAST(substr(CAST(streamed AS BLOB),1,1024) AS TEXT),''), length(CAST(streamed AS BLOB))>1024,
COALESCE(CAST(substr(CAST(created_at AS BLOB),1,64) AS TEXT),''), length(CAST(created_at AS BLOB))>64,
rolled_back
FROM timeline ORDER BY requested_at DESC, turn_order DESC, sequence DESC LIMIT 9 OFFSET ?`, id, offset)
	if err != nil {
		return nil, false, fmt.Errorf("read retained session history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	entries := make([]TriageHistoryEntry, 0, TriageHistoryPageSize)
	more := false
	for rows.Next() {
		var entry TriageHistoryEntry
		if err := rows.Scan(&entry.Sequence,
			&entry.Kind.Value, &entry.Kind.Truncated, &entry.Role.Value, &entry.Role.Truncated,
			&entry.State.Value, &entry.State.Truncated, &entry.TurnState.Value, &entry.TurnState.Truncated,
			&entry.Text.Value, &entry.Text.Truncated, &entry.Detail.Value, &entry.Detail.Truncated,
			&entry.Output.Value, &entry.Output.Truncated, &entry.Streamed.Value, &entry.Streamed.Truncated,
			&entry.CreatedAt.Value, &entry.CreatedAt.Truncated, &entry.RolledBack); err != nil {
			return nil, false, fmt.Errorf("scan retained session history: %w", err)
		}
		if len(entries) == TriageHistoryPageSize {
			more = true
			continue
		}
		entries = append(entries, entry)
	}
	return entries, more, rows.Err()
}

// ReadTriageProviderEvents excludes events from other owners, including those
// sharing the same project conversation. Raw payloads remain private context.
func (s *Store) ReadTriageProviderEvents(ctx context.Context, id domain.SessionID, offset int64) ([]TriageProviderEvent, bool, error) {
	if offset < 0 || offset > TriageMaxHistoryOffset {
		return nil, false, errors.New("invalid diagnostic provider offset")
	}
	rows, err := s.readDB.QueryContext(ctx, `SELECT
COALESCE(CAST(substr(CAST(method AS BLOB),1,128) AS TEXT),''), length(CAST(method AS BLOB))>128,
COALESCE(CAST(substr(CAST(payload_json AS BLOB),1,8192) AS TEXT),''), length(CAST(payload_json AS BLOB))>8192,
COALESCE(CAST(substr(CAST(received_at AS BLOB),1,64) AS TEXT),''), length(CAST(received_at AS BLOB))>64
FROM conversation_provider_events WHERE session_id=? ORDER BY id DESC LIMIT 7 OFFSET ?`, id, offset)
	if err != nil {
		return nil, false, fmt.Errorf("read retained provider events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	entries := make([]TriageProviderEvent, 0, TriageProviderPageSize)
	more := false
	for rows.Next() {
		var entry TriageProviderEvent
		if err := rows.Scan(&entry.Method.Value, &entry.Method.Truncated, &entry.Payload.Value, &entry.Payload.Truncated,
			&entry.ReceivedAt.Value, &entry.ReceivedAt.Truncated); err != nil {
			return nil, false, fmt.Errorf("scan retained provider event: %w", err)
		}
		if len(entries) == TriageProviderPageSize {
			more = true
			continue
		}
		entries = append(entries, entry)
	}
	return entries, more, rows.Err()
}

// ReadTriageSessionFacts reads one narrow session row without reconciliation.
func (s *Store) ReadTriageSessionFacts(ctx context.Context, id domain.SessionID) (TriageSessionFacts, bool, error) {
	var facts TriageSessionFacts
	err := s.readDB.QueryRowContext(ctx, `SELECT
COALESCE(CAST(substr(CAST(harness AS BLOB),1,64) AS TEXT),''), COALESCE(length(CAST(harness AS BLOB)),0)>64,
COALESCE(CAST(substr(CAST(session_mode AS BLOB),1,64) AS TEXT),''), COALESCE(length(CAST(session_mode AS BLOB)),0)>64,
COALESCE(CAST(substr(CAST(activity_state AS BLOB),1,64) AS TEXT),''), COALESCE(length(CAST(activity_state AS BLOB)),0)>64,
COALESCE(CAST(substr(CAST(provision_state AS BLOB),1,64) AS TEXT),''), COALESCE(length(CAST(provision_state AS BLOB)),0)>64,
COALESCE(CAST(substr(CAST(provision_error AS BLOB),1,4096) AS TEXT),''), COALESCE(length(CAST(provision_error AS BLOB)),0)>4096,
COALESCE(CAST(substr(CAST(workspace_path AS BLOB),1,4096) AS TEXT),''), COALESCE(length(CAST(workspace_path AS BLOB)),0)>4096,
COALESCE(CAST(substr(CAST(created_at AS BLOB),1,64) AS TEXT),''), COALESCE(length(CAST(created_at AS BLOB)),0)>64,
COALESCE(CAST(substr(CAST(updated_at AS BLOB),1,64) AS TEXT),''), COALESCE(length(CAST(updated_at AS BLOB)),0)>64,
COALESCE(CAST(substr(CAST(activity_last_at AS BLOB),1,64) AS TEXT),''), COALESCE(length(CAST(activity_last_at AS BLOB)),0)>64,
is_terminated
FROM sessions WHERE id=? LIMIT 1`, id).Scan(
		&facts.Harness.Value, &facts.Harness.Truncated,
		&facts.Mode.Value, &facts.Mode.Truncated,
		&facts.Activity.Value, &facts.Activity.Truncated,
		&facts.ProvisionState.Value, &facts.ProvisionState.Truncated,
		&facts.ProvisionError.Value, &facts.ProvisionError.Truncated,
		&facts.WorkspacePath.Value, &facts.WorkspacePath.Truncated,
		&facts.CreatedAt.Value, &facts.CreatedAt.Truncated,
		&facts.UpdatedAt.Value, &facts.UpdatedAt.Truncated,
		&facts.ActivityLastAt.Value, &facts.ActivityLastAt.Truncated,
		&facts.IsTerminated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return TriageSessionFacts{}, false, nil
	}
	if err != nil {
		return TriageSessionFacts{}, false, fmt.Errorf("read retained session facts: %w", err)
	}
	return facts, true, nil
}

// ReadTriageLatestTurn reads at most one turn from the currently linked
// conversation and requires exact session ownership even for project narratives.
func (s *Store) ReadTriageLatestTurn(ctx context.Context, id domain.SessionID) (TriageTurnFacts, bool, error) {
	var facts TriageTurnFacts
	err := s.readDB.QueryRowContext(ctx, `SELECT
COALESCE(CAST(substr(CAST(state AS BLOB),1,64) AS TEXT),''), COALESCE(length(CAST(state AS BLOB)),0)>64,
COALESCE(CAST(substr(CAST(error_message AS BLOB),1,4096) AS TEXT),''), COALESCE(length(CAST(error_message AS BLOB)),0)>4096,
COALESCE(CAST(substr(CAST(requested_at AS BLOB),1,64) AS TEXT),''), COALESCE(length(CAST(requested_at AS BLOB)),0)>64,
COALESCE(CAST(substr(CAST(COALESCE(started_at,'') AS BLOB),1,64) AS TEXT),''), COALESCE(length(CAST(COALESCE(started_at,'') AS BLOB)),0)>64,
COALESCE(CAST(substr(CAST(COALESCE(completed_at,'') AS BLOB),1,64) AS TEXT),''), COALESCE(length(CAST(COALESCE(completed_at,'') AS BLOB)),0)>64
FROM conversation_turns
WHERE conversation_id=(
 SELECT id FROM conversations WHERE current_session_id=?
 ORDER BY scope='project' DESC, created_at DESC, id DESC LIMIT 1
)
AND handled_by_session_id=? AND rolled_back_at IS NULL AND promoted_to_turn_id IS NULL
ORDER BY requested_at DESC, rowid DESC LIMIT 1`, id, id).Scan(
		&facts.State.Value, &facts.State.Truncated,
		&facts.Error.Value, &facts.Error.Truncated,
		&facts.RequestedAt.Value, &facts.RequestedAt.Truncated,
		&facts.StartedAt.Value, &facts.StartedAt.Truncated,
		&facts.CompletedAt.Value, &facts.CompletedAt.Truncated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return TriageTurnFacts{}, false, nil
	}
	if err != nil {
		return TriageTurnFacts{}, false, fmt.Errorf("read retained owned turn: %w", err)
	}
	return facts, true, nil
}

// ReadTriageWorkspaceRoots reads up to eight selected-session roots. The extra
// sentinel row reports omitted rows without allocating an unbounded registry.
func (s *Store) ReadTriageWorkspaceRoots(ctx context.Context, id domain.SessionID) ([]TriageWorkspaceRoot, bool, error) {
	rows, err := s.readDB.QueryContext(ctx, `SELECT
COALESCE(CAST(substr(CAST(repo_name AS BLOB),1,128) AS TEXT),''), COALESCE(length(CAST(repo_name AS BLOB)),0)>128,
COALESCE(CAST(substr(CAST(worktree_path AS BLOB),1,4096) AS TEXT),''), COALESCE(length(CAST(worktree_path AS BLOB)),0)>4096
FROM session_worktrees WHERE session_id=? AND state='active'
ORDER BY CASE WHEN repo_name='__root__' THEN 0 ELSE 1 END, repo_name LIMIT 9`, id)
	if err != nil {
		return nil, false, fmt.Errorf("read retained workspace roots: %w", err)
	}
	defer func() { _ = rows.Close() }()
	roots := make([]TriageWorkspaceRoot, 0, 8)
	truncated := false
	for rows.Next() {
		var root TriageWorkspaceRoot
		if err := rows.Scan(&root.Name.Value, &root.Name.Truncated, &root.Path.Value, &root.Path.Truncated); err != nil {
			return nil, false, fmt.Errorf("scan retained workspace root: %w", err)
		}
		if len(roots) == 8 {
			truncated = true
			continue
		}
		roots = append(roots, root)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("read retained workspace roots: %w", err)
	}
	return roots, truncated, nil
}
