-- +goose Up
-- One daemon owns this aggregate. Runtime/native observations are never stored.
CREATE TABLE awake_state (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 document TEXT NOT NULL CHECK (json_valid(document))
);
INSERT INTO awake_state (id, document) VALUES (1, '{}');

-- +goose Down
DROP TABLE awake_state;
