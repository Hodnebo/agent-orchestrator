package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// FallBackChatStart moves a session created in Chat to the TUI before its
// first Chat controller starts, because the worker that is about to run it
// cannot start in Chat. Only a session whose Chat came from its create (no
// interface transition was ever recorded) and that never started moves: a
// session the person switched to Chat stays on the interface they chose, and
// a session whose Chat already ran (a turn was claimed or settled, or the
// agent reported its conversation) stays in Chat on a later worker restart
// or reconnect, rather than showing a false notice and running its first
// task again in the terminal.
//
// In the same transaction the prompt's queued first turn is settled (the TUI
// agent receives the prompt on its command line instead), and a failed
// TUI-to-Chat transition carrying code and detail is recorded, which the
// desktop shows as the session's interface notice. It reports whether the
// session moved.
func (s *Store) FallBackChatStart(ctx context.Context, orgID, sessionID, code, detail string) (bool, error) {
	var moved bool
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		var err error
		moved, err = fallBackChatStartTx(ctx, tx, orgID, sessionID, code, detail)
		return err
	})
	return moved, err
}

// WorkerFallBackChatStart is FallBackChatStart asked for by the session's
// current worker, which found it cannot start the Chat controller (for
// Claude, no ACP adapter and no npm to install one).
func (s *Store) WorkerFallBackChatStart(
	ctx context.Context,
	orgID, sessionID, workerID string,
	epoch int64,
	code, detail string,
) (bool, error) {
	var moved bool
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		if err := requireCurrentWorker(ctx, tx, orgID, sessionID, workerID, epoch); err != nil {
			return err
		}
		var err error
		moved, err = fallBackChatStartTx(ctx, tx, orgID, sessionID, code, detail)
		return err
	})
	return moved, err
}

func fallBackChatStartTx(ctx context.Context, tx pgx.Tx, orgID, sessionID, code, detail string) (bool, error) {
	var prompt string
	err := tx.QueryRow(ctx,
		`UPDATE ao_sessions session
		SET interface = 'tui', updated_at = now()
		WHERE session.org_id = $1 AND session.id = $2
		  AND session.interface = 'chat'
		  AND session.agent_session_id = ''
		  AND NOT EXISTS (
			SELECT 1 FROM ao_interface_transitions transition
			WHERE transition.org_id = session.org_id AND transition.session_id = session.id
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM ao_turns turn
			WHERE turn.org_id = session.org_id AND turn.session_id = session.id
			  AND (turn.attempt_count > 0 OR turn.state <> 'queued')
		  )
		RETURNING session.prompt`,
		orgID, sessionID,
	).Scan(&prompt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if prompt != "" {
		// The create recorded the prompt as the session's first user message
		// and queued it; nothing has claimed it while no Chat controller ran.
		if _, err := tx.Exec(ctx,
			`UPDATE ao_turns turn
			SET state = 'completed', completed_at = now(), updated_at = now()
			WHERE turn.org_id = $1 AND turn.session_id = $2
			  AND turn.state = 'queued' AND turn.attempt_count = 0
			  AND turn.user_message_sequence = (
				SELECT min(event.sequence) FROM ao_events event
				WHERE event.org_id = $1 AND event.session_id = $2
				  AND event.type = 'chat.user_message'
			  )`,
			orgID, sessionID,
		); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO ao_interface_transitions (
			org_id, session_id, source_interface, target_interface, policy,
			phase, error_code, error_detail, completed_at
		) VALUES ($1, $2, 'tui', 'chat', 'interrupt', 'failed', $3, $4, now())`,
		orgID, sessionID, code, detail,
	); err != nil {
		return false, normalizeConstraintError(err)
	}
	return true, nil
}
