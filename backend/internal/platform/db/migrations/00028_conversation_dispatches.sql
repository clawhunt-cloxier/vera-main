-- +goose Up
ALTER TABLE conversation_turns ADD COLUMN agent_dispatches integer NOT NULL DEFAULT 0 CHECK (agent_dispatches >= 0);

-- +goose Down
ALTER TABLE conversation_turns DROP COLUMN agent_dispatches;
