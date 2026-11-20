-- +goose Up
-- Older prices and metrics have no recoverable provider attribution.
ALTER TABLE watchlist_items ADD COLUMN added_price_provider_id text REFERENCES providers(id);
ALTER TABLE watchlist_snapshots ADD COLUMN provider_id text REFERENCES providers(id);
ALTER TABLE watchlist_snapshots DROP CONSTRAINT watchlist_snapshots_pkey;
ALTER TABLE watchlist_snapshots ADD CONSTRAINT watchlist_snapshots_source_day
    UNIQUE NULLS NOT DISTINCT (item_id, provider_id, trade_date);

-- +goose Down
-- This fails transactionally if restoring the legacy key would discard data.
ALTER TABLE watchlist_snapshots ADD PRIMARY KEY (item_id, trade_date);
ALTER TABLE watchlist_snapshots DROP CONSTRAINT watchlist_snapshots_source_day;
ALTER TABLE watchlist_snapshots DROP COLUMN provider_id;
ALTER TABLE watchlist_items DROP COLUMN added_price_provider_id;
