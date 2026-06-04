package models

import "time"

// AssetType categorises each data source
type AssetType string

const (
	AssetTypeBankCurrent  AssetType = "bank_current"
	AssetTypeBankSaving   AssetType = "bank_saving"
	AssetTypeCreditCard   AssetType = "credit_card" // shown separately, not in total
	AssetTypeCrypto       AssetType = "crypto"
	AssetTypeExchange     AssetType = "exchange"
	AssetTypeSteam        AssetType = "steam"
)

// Asset is a single balance entry at a point in time
type Asset struct {
	ID          int64     `db:"id"`
	Source      string    `db:"source"`       // "tbank", "bybit", "bitcoin", "monero"
	Name        string    `db:"name"`         // human-readable label
	Type        AssetType `db:"type"`
	AmountRaw   float64   `db:"amount_raw"`   // in original currency
	Currency    string    `db:"currency"`     // "RUB", "USD", "BTC", "XMR"
	AmountRUB   float64   `db:"amount_rub"`   // converted to RUB for totals
	FetchedAt   time.Time `db:"fetched_at"`
	Extra       string    `db:"extra"`        // JSON blob for source-specific data
}

// Snapshot is a point-in-time roll-up of all assets
type Snapshot struct {
	ID          int64     `db:"id"`
	TotalRUB    float64   `db:"total_rub"`    // sum of non-credit assets
	CreditRUB   float64   `db:"credit_rub"`   // total credit debt
	CreatedAt   time.Time `db:"created_at"`
}

// ExchangeRate cached rates
type ExchangeRate struct {
	From      string    `db:"from_currency"`
	To        string    `db:"to_currency"`
	Rate      float64   `db:"rate"`
	UpdatedAt time.Time `db:"updated_at"`
}

// DashboardData is what the template receives
type DashboardData struct {
	Assets        []Asset
	BankAssets    []Asset
	CryptoAssets  []Asset
	CreditCards   []Asset // shown separately
	TotalRUB      float64
	CreditTotalRUB float64
	LastUpdated   time.Time
	Error         string
	Rates         map[string]float64 // e.g. "USD" -> 90.5
}
