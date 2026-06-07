-- +goose Up
CREATE TABLE IF NOT EXISTS api_keys (
    key_name   TEXT PRIMARY KEY,
    key_value  TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS api_keys;
