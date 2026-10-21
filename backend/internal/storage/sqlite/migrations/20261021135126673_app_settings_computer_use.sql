-- +goose Up
-- Off by default: when on, new agent sessions get a standing instruction that
-- they can drive desktop apps through `ao cua` (Settings > Computer use agents).
ALTER TABLE app_settings ADD COLUMN computer_use INTEGER NOT NULL DEFAULT 0
    CHECK (computer_use IN (0, 1));

-- +goose Down
ALTER TABLE app_settings DROP COLUMN computer_use;
