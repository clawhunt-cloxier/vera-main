-- +goose Up
-- Only a successful full collection for the requested stock writes this row.
-- Directory entries and securities encountered as peers are not registrations.
CREATE TABLE tushare_requested_subjects (
    code text PRIMARY KEY REFERENCES entities(code),
    published_at timestamptz NOT NULL,
    CHECK (code ~ '^[0-9]{6}[.](SH|SZ|BJ)$')
);
GRANT SELECT ON tushare_requested_subjects TO vera_app;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM tushare_requested_subjects) THEN
        RAISE EXCEPTION 'cannot discard published on-demand research registrations';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE tushare_requested_subjects;
