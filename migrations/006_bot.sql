-- +goose Up

ALTER TABLE user_settings
  ADD COLUMN IF NOT EXISTS bot_enabled BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS bot_interval_minutes INT NOT NULL DEFAULT 60,
  ADD COLUMN IF NOT EXISTS bot_use_margin BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS bot_logs (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    level TEXT NOT NULL DEFAULT 'info',
    message TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS bot_logs_user_time ON bot_logs(user_id, created_at DESC);

-- +goose Down

ALTER TABLE user_settings
  DROP COLUMN IF EXISTS bot_enabled,
  DROP COLUMN IF EXISTS bot_interval_minutes,
  DROP COLUMN IF EXISTS bot_use_margin;

DROP TABLE IF EXISTS bot_logs;
