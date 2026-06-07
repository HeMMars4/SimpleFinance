-- +goose Up

CREATE TABLE IF NOT EXISTS manual_assets (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    type        TEXT NOT NULL DEFAULT 'cash',
    amount      NUMERIC(20, 8) NOT NULL DEFAULT 0,
    currency    TEXT NOT NULL DEFAULT 'RUB',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- asset_key format: "{source}:{name}"
CREATE TABLE IF NOT EXISTS asset_filters (
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    asset_key   TEXT NOT NULL,
    hidden      BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (user_id, asset_key)
);

-- Additional integration instances (extra Steam inventories, Monero/BTC wallets)
CREATE TABLE IF NOT EXISTS integration_instances (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type        TEXT NOT NULL,
    label       TEXT NOT NULL,
    config      JSONB NOT NULL DEFAULT '{}',
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Per-user AI and trading settings
CREATE TABLE IF NOT EXISTS user_settings (
    user_id             BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    risk_level          TEXT NOT NULL DEFAULT 'medium',
    max_loss_pct        NUMERIC(5,2) NOT NULL DEFAULT 5.0,
    claude_api_key      TEXT NOT NULL DEFAULT '',
    tinvest_trade_token TEXT NOT NULL DEFAULT '',
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS user_settings;
DROP TABLE IF EXISTS integration_instances;
DROP TABLE IF EXISTS asset_filters;
DROP TABLE IF EXISTS manual_assets;
