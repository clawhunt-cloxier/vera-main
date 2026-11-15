-- +goose Up
-- Account scope is a non-secret identifier shared by every process using the
-- same vendor account. A surviving in-flight marker must never be discarded
-- automatically: an SDK request may outlive its PostgreSQL session.
CREATE TABLE emquant_rate_state (
    scope text PRIMARY KEY CHECK (scope <> '' AND length(scope) <= 128 AND scope = btrim(scope)),
    in_flight boolean NOT NULL,
    next_allowed timestamptz NOT NULL
);
-- The workspace role cannot modify account-wide fencing state. The system
-- connection role owns this table and is responsible for the rate gate.

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM emquant_rate_state) THEN
        RAISE EXCEPTION 'cannot remove nonempty EMQuant rate state; coordinate safe account recovery first';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE emquant_rate_state;
