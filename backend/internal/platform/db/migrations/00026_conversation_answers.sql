-- +goose Up
ALTER TABLE conversation_turns ADD COLUMN automatic boolean NOT NULL DEFAULT false;

ALTER TABLE model_calls ADD COLUMN auto_released_at timestamptz;
UPDATE model_calls SET auto_released_at=coalesce(finished_at, sent_at)
    WHERE status='RECONCILED' AND cost_usd=0 AND error LIKE '%auto-released%';

ALTER TABLE conversation_turns DROP CONSTRAINT conversation_turns_state_check;
ALTER TABLE conversation_turns ADD CONSTRAINT conversation_turns_state_check
    CHECK (state IN ('pending','clarify','confirm','research','reference','answer','unavailable'));

-- +goose Down
ALTER TABLE model_calls DROP COLUMN auto_released_at;
ALTER TABLE conversation_turns DROP COLUMN automatic;
UPDATE conversation_turns SET state='unavailable',message=NULL,intent=NULL WHERE state='answer';
ALTER TABLE conversation_turns DROP CONSTRAINT conversation_turns_state_check;
ALTER TABLE conversation_turns ADD CONSTRAINT conversation_turns_state_check
    CHECK (state IN ('pending','clarify','confirm','research','reference','unavailable'));
