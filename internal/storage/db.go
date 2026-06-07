package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/HeMMars4/simple-finance/config"
	"github.com/HeMMars4/simple-finance/internal/models"
)

type User struct {
	ID           int64     `db:"id"`
	Username     string    `db:"username"`
	PasswordHash string    `db:"password_hash"`
	CreatedAt    time.Time `db:"created_at"`
}

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

// SaveAssets replaces all assets for a given user+source in one transaction
func (db *DB) SaveAssets(ctx context.Context, userID int64, source string, assets []models.Asset) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM assets WHERE user_id = $1 AND source = $2`, userID, source,
	); err != nil {
		return err
	}

	for _, a := range assets {
		if a.AmountRaw <= 0 || a.AmountRUB <= 0 {
			continue
		}
		extra := a.Extra
		if extra == "" {
			extra = "null"
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO assets (user_id, source, name, type, amount_raw, currency, amount_rub, fetched_at, extra)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			userID, a.Source, a.Name, a.Type, a.AmountRaw, a.Currency, a.AmountRUB, time.Now(), extra,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetAllAssets returns the latest snapshot of all assets for a user
func (db *DB) GetAllAssets(ctx context.Context, userID int64) ([]models.Asset, error) {
	var assets []models.Asset
	err := db.SelectContext(ctx, &assets, `
		SELECT DISTINCT ON (source, name)
		  id, source, name, type, amount_raw, currency, amount_rub, fetched_at, extra
		FROM assets
		WHERE user_id = $1
		ORDER BY source, name, fetched_at DESC
	`, userID)
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

// GetAllAPIKeys returns all stored API keys for a user as a map
func (db *DB) GetAllAPIKeys(ctx context.Context, userID int64) (map[string]string, error) {
	rows, err := db.QueryxContext(ctx,
		`SELECT key_name, key_value FROM api_keys WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		result[name] = value
	}
	return result, rows.Err()
}

// SetAPIKey upserts a single API key value for a user
func (db *DB) SetAPIKey(ctx context.Context, userID int64, name, value string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO api_keys (user_id, key_name, key_value, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (user_id, key_name) DO UPDATE SET key_value = EXCLUDED.key_value, updated_at = NOW()`,
		userID, name, value,
	)
	return err
}

// UserCount returns number of users in the DB.
func (db *DB) UserCount(ctx context.Context) (int, error) {
	var n int
	err := db.QueryRowxContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// GetUser returns a user by username.
func (db *DB) GetUser(ctx context.Context, username string) (*User, error) {
	var u User
	err := db.QueryRowxContext(ctx, `SELECT * FROM users WHERE username = $1`, username).StructScan(&u)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateUser inserts a new user with a pre-hashed password.
func (db *DB) CreateUser(ctx context.Context, username, passwordHash string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash) VALUES ($1, $2)`,
		username, passwordHash,
	)
	return err
}

// DeleteUser removes a user by username.
func (db *DB) DeleteUser(ctx context.Context, username string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM users WHERE username = $1`, username)
	return err
}

// ListUsers returns all users ordered by creation date.
func (db *DB) ListUsers(ctx context.Context) ([]User, error) {
	var users []User
	err := db.SelectContext(ctx, &users, `SELECT * FROM users ORDER BY created_at ASC`)
	return users, err
}

// UpdatePassword replaces the password hash for a user.
func (db *DB) UpdatePassword(ctx context.Context, username, passwordHash string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE users SET password_hash = $2 WHERE username = $1`,
		username, passwordHash,
	)
	return err
}

// --- Manual Assets ---

func (db *DB) GetManualAssets(ctx context.Context, userID int64) ([]models.ManualAsset, error) {
	var assets []models.ManualAsset
	err := db.SelectContext(ctx, &assets,
		`SELECT id, user_id, name, type, amount, currency, created_at FROM manual_assets WHERE user_id = $1 ORDER BY created_at ASC`,
		userID,
	)
	return assets, err
}

func (db *DB) CreateManualAsset(ctx context.Context, userID int64, name string, typ models.AssetType, amount float64, currency string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO manual_assets (user_id, name, type, amount, currency) VALUES ($1, $2, $3, $4, $5)`,
		userID, name, typ, amount, currency,
	)
	return err
}

func (db *DB) DeleteManualAsset(ctx context.Context, userID, id int64) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM manual_assets WHERE id = $1 AND user_id = $2`,
		id, userID,
	)
	return err
}

// --- Asset Filters ---

func (db *DB) GetHiddenAssets(ctx context.Context, userID int64) (map[string]bool, error) {
	rows, err := db.QueryxContext(ctx,
		`SELECT asset_key FROM asset_filters WHERE user_id = $1 AND hidden = TRUE`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]bool)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		result[key] = true
	}
	return result, rows.Err()
}

func (db *DB) SetAssetHidden(ctx context.Context, userID int64, assetKey string, hidden bool) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO asset_filters (user_id, asset_key, hidden)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, asset_key) DO UPDATE SET hidden = EXCLUDED.hidden`,
		userID, assetKey, hidden,
	)
	return err
}

// --- Integration Instances ---

func (db *DB) GetIntegrationInstances(ctx context.Context, userID int64) ([]models.IntegrationInstance, error) {
	var instances []models.IntegrationInstance
	err := db.SelectContext(ctx, &instances,
		`SELECT id, user_id, type, label, config::text, enabled FROM integration_instances WHERE user_id = $1 ORDER BY created_at ASC`,
		userID,
	)
	return instances, err
}

func (db *DB) CreateIntegrationInstance(ctx context.Context, userID int64, typ, label, config string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO integration_instances (user_id, type, label, config) VALUES ($1, $2, $3, $4::jsonb)`,
		userID, typ, label, config,
	)
	return err
}

func (db *DB) DeleteIntegrationInstance(ctx context.Context, userID, id int64) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM integration_instances WHERE id = $1 AND user_id = $2`,
		id, userID,
	)
	return err
}

// --- User Settings ---

func (db *DB) GetUserSettings(ctx context.Context, userID int64) (*models.UserSettings, error) {
	var s models.UserSettings
	err := db.QueryRowxContext(ctx,
		`SELECT user_id, risk_level, max_loss_pct, claude_api_key, tinvest_trade_token,
		        COALESCE(bot_enabled, FALSE) AS bot_enabled,
		        COALESCE(bot_interval_minutes, 60) AS bot_interval_minutes,
		        COALESCE(bot_use_margin, FALSE) AS bot_use_margin
		 FROM user_settings WHERE user_id = $1`, userID,
	).StructScan(&s)
	if err != nil {
		return &models.UserSettings{
			UserID:             userID,
			RiskLevel:          "medium",
			MaxLossPct:         5.0,
			BotIntervalMinutes: 60,
		}, nil
	}
	return &s, nil
}

func (db *DB) SaveUserSettings(ctx context.Context, s *models.UserSettings) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO user_settings (user_id, risk_level, max_loss_pct, claude_api_key, tinvest_trade_token,
		    bot_enabled, bot_interval_minutes, bot_use_margin, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
		ON CONFLICT (user_id) DO UPDATE SET
		  risk_level = EXCLUDED.risk_level,
		  max_loss_pct = EXCLUDED.max_loss_pct,
		  claude_api_key = EXCLUDED.claude_api_key,
		  tinvest_trade_token = EXCLUDED.tinvest_trade_token,
		  bot_enabled = EXCLUDED.bot_enabled,
		  bot_interval_minutes = EXCLUDED.bot_interval_minutes,
		  bot_use_margin = EXCLUDED.bot_use_margin,
		  updated_at = NOW()`,
		s.UserID, s.RiskLevel, s.MaxLossPct, s.ClaudeAPIKey, s.TInvestTradeToken,
		s.BotEnabled, s.BotIntervalMinutes, s.BotUseMargin,
	)
	return err
}

// --- Bot Logs ---

func (db *DB) AddBotLog(ctx context.Context, userID int64, level, message string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO bot_logs (user_id, level, message) VALUES ($1, $2, $3)`,
		userID, level, message,
	)
	return err
}

func (db *DB) GetBotLogs(ctx context.Context, userID int64, limit int) ([]models.BotLog, error) {
	var logs []models.BotLog
	err := db.SelectContext(ctx, &logs,
		`SELECT id, user_id, created_at, level, message
		 FROM bot_logs WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2`,
		userID, limit,
	)
	return logs, err
}

func (db *DB) ClearBotLogs(ctx context.Context, userID int64) error {
	_, err := db.ExecContext(ctx, `DELETE FROM bot_logs WHERE user_id = $1`, userID)
	return err
}
