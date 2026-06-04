package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/yourname/simple-finance/config"
	"github.com/yourname/simple-finance/internal/models"
)

type DB struct {
	*sqlx.DB
}

func New(cfg config.DBConfig) (*DB, error) {
	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.Name,
	)
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("connect db: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	return &DB{db}, nil
}

// SaveAssets replaces all assets for a given source in one transaction
func (db *DB) SaveAssets(ctx context.Context, source string, assets []models.Asset) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM assets WHERE source = $1`, source); err != nil {
		return err
	}

	for _, a := range assets {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO assets (source, name, type, amount_raw, currency, amount_rub, fetched_at, extra)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			a.Source, a.Name, a.Type, a.AmountRaw, a.Currency, a.AmountRUB, time.Now(), a.Extra,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetAllAssets returns the latest snapshot of all assets
func (db *DB) GetAllAssets(ctx context.Context) ([]models.Asset, error) {
	var assets []models.Asset
	err := db.SelectContext(ctx, &assets, `
		SELECT DISTINCT ON (source, name) *
		FROM assets
		ORDER BY source, name, fetched_at DESC
	`)
	return assets, err
}

// GetRate returns a cached exchange rate
func (db *DB) GetRate(ctx context.Context, from, to string) (float64, error) {
	var rate float64
	err := db.QueryRowxContext(ctx,
		`SELECT rate FROM exchange_rates WHERE from_currency=$1 AND to_currency=$2`,
		from, to,
	).Scan(&rate)
	return rate, err
}

// UpsertRate updates or inserts an exchange rate
func (db *DB) UpsertRate(ctx context.Context, from, to string, rate float64) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO exchange_rates (from_currency, to_currency, rate, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (from_currency, to_currency)
		DO UPDATE SET rate = EXCLUDED.rate, updated_at = NOW()`,
		from, to, rate,
	)
	return err
}

// SaveSnapshot records a point-in-time total
func (db *DB) SaveSnapshot(ctx context.Context, total, credit float64) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO snapshots (total_rub, credit_rub) VALUES ($1, $2)`,
		total, credit,
	)
	return err
}

// GetSnapshots returns the last N snapshots for charting
func (db *DB) GetSnapshots(ctx context.Context, limit int) ([]models.Snapshot, error) {
	var snaps []models.Snapshot
	err := db.SelectContext(ctx, &snaps,
		`SELECT * FROM snapshots ORDER BY created_at DESC LIMIT $1`, limit,
	)
	return snaps, err
}
