-- +goose Up
ALTER TABLE ao_sessions
    ADD COLUMN task_naming_state TEXT NOT NULL DEFAULT 'done'
        CHECK (task_naming_state IN ('pending', 'reserved', 'done')),
    ADD COLUMN task_name_slug TEXT NOT NULL DEFAULT '',
    ADD COLUMN task_name_initial_title TEXT;

-- +goose Down
ALTER TABLE ao_sessions
    DROP COLUMN task_name_initial_title,
    DROP COLUMN task_name_slug,
    DROP COLUMN task_naming_state;
