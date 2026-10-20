-- +goose Up

-- The user's default session interface a session was created with (the
-- desktop's setting): '' when the client sent none. A worker an orchestrator
-- spawns resolves its own interface from its orchestrator's, as a local spawn
-- resolves from the daemon's default.
ALTER TABLE ao_sessions
    ADD COLUMN default_interface TEXT NOT NULL DEFAULT ''
        CHECK (default_interface IN ('', 'tui', 'chat'));

-- +goose Down

ALTER TABLE ao_sessions DROP COLUMN IF EXISTS default_interface;
