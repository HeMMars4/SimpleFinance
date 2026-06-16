-- +goose Up

ALTER TABLE user_settings
  ADD COLUMN IF NOT EXISTS paper_trading BOOLEAN NOT NULL DEFAULT FALSE,
  ADD COLUMN IF NOT EXISTS paper_balance_rub FLOAT NOT NULL DEFAULT 100000;

CREATE TABLE IF NOT EXISTS paper_positions (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  ticker TEXT NOT NULL,
  figi TEXT NOT NULL DEFAULT '',
  quantity INT NOT NULL DEFAULT 0,
  avg_price_rub FLOAT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE(user_id, ticker)
);

-- +goose Down

ALTER TABLE user_settings
  DROP COLUMN IF EXISTS paper_trading,
  DROP COLUMN IF EXISTS paper_balance_rub;

DROP TABLE IF EXISTS paper_positions;
