-- +goose Up

-- The failed provider build (a Coder workspace build id) the reconciler has
-- already acted on, with a suffix naming what it did: the bare id when it
-- reported the failure, "#restarted" once it started the one replacement build
-- a user retry asked for, "#ignored" when a worker was connected and the
-- failure was left alone. Keying on build ids rather than timestamps keeps a
-- failure from being acted on twice and needs no clock agreement with the
-- provider. Numbered 00085 and idempotent: 00084 is taken by another branch.
ALTER TABLE ao_sandboxes ADD COLUMN IF NOT EXISTS provider_failure_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE ao_sandboxes DROP COLUMN IF EXISTS provider_failure_id;
