-- +goose Up
CREATE TABLE IF NOT EXISTS assets (
    id          BIGSERIAL PRIMARY KEY,
    source      TEXT NOT NULL,
    name        TEXT NOT NULL,
    type        TEXT NOT NULL,
    amount_raw  NUMERIC(20, 8) NOT NULL DEFAULT 0,
    currency    TEXT NOT NULL,
    amount_rub  NUMERIC(20, 2) NOT NULL DEFAULT 0,
    fetched_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    extra       JSONB
);

CREATE TABLE IF NOT EXISTS snapshots (
    id          BIGSERIAL PRIMARY KEY,
    total_rub   NUMERIC(20, 2) NOT NULL DEFAULT 0,
    credit_rub  NUMERIC(20, 2) NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS exchange_rates (
    from_currency TEXT NOT NULL,
    to_currency   TEXT NOT NULL,
    rate          NUMERIC(20, 8) NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (from_currency, to_currency)
);

-- Seed some defaults
INSERT INTO exchange_rates (from_currency, to_currency, rate)
VALUES ('USD', 'RUB', 90.0),
       ('BTC', 'RUB', 9000000.0),
       ('XMR', 'RUB', 18000.0)
ON CONFLICT DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS assets;
DROP TABLE IF EXISTS snapshots;
DROP TABLE IF EXISTS exchange_rates;
