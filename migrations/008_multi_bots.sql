-- +goose Up

ALTER TABLE user_settings
  ADD COLUMN IF NOT EXISTS investor_bot_enabled    BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS investor_interval_hours INT     NOT NULL DEFAULT 24,
  ADD COLUMN IF NOT EXISTS bybit_bot_enabled       BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS bybit_bot_interval_min  INT     NOT NULL DEFAULT 60,
  ADD COLUMN IF NOT EXISTS bybit_paper_trading     BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS bybit_paper_balance_usd FLOAT   NOT NULL DEFAULT 1000;

-- +goose Down

ALTER TABLE user_settings
  DROP COLUMN IF EXISTS investor_bot_enabled,
  DROP COLUMN IF EXISTS investor_interval_hours,
  DROP COLUMN IF EXISTS bybit_bot_enabled,
  DROP COLUMN IF EXISTS bybit_bot_interval_min,
  DROP COLUMN IF EXISTS bybit_paper_trading,
  DROP COLUMN IF EXISTS bybit_paper_balance_usd;
