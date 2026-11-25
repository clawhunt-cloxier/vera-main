-- +goose Up
ALTER TABLE model_calls ADD COLUMN content_erased_at timestamptz;

-- Earlier versions of the conversation worker could delete brief tasks
-- without marking the surviving accounting records.
UPDATE model_calls m SET response=NULL,error=NULL,content_erased_at=now()
    WHERE m.task_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM research_tasks t WHERE t.workspace_id=m.workspace_id AND t.id=m.task_id
    );

ALTER TABLE model_calls ADD CONSTRAINT model_call_erased_content_empty
    CHECK (content_erased_at IS NULL OR (response IS NULL AND error IS NULL));

-- +goose Down
ALTER TABLE model_calls DROP CONSTRAINT model_call_erased_content_empty, DROP COLUMN content_erased_at;
