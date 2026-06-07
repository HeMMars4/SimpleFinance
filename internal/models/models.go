package models

import (
	"encoding/json"
	"time"
)

type AssetType string

const (
	AssetTypeBankCurrent AssetType = "bank_current"
	AssetTypeBankSaving  AssetType = "bank_saving"
	AssetTypeCreditCard  AssetType = "credit_card" // shown separately, not in total
	AssetTypeCrypto      AssetType = "crypto"
	AssetTypeExchange    AssetType = "exchange"
	AssetTypeSteam       AssetType = "steam"
	AssetTypeInvest      AssetType = "invest"
	AssetTypeCash        AssetType = "cash"
)

type Asset struct {
	ID        int64     `db:"id"`
	Source    string    `db:"source"`
	Name      string    `db:"name"`
	Type      AssetType `db:"type"`
	AmountRaw float64   `db:"amount_raw"`
	Currency  string    `db:"currency"`
	AmountRUB float64   `db:"amount_rub"`
	FetchedAt time.Time `db:"fetched_at"`
	Extra     string    `db:"extra"`
}

type Snapshot struct {
	ID        int64     `db:"id"`
	TotalRUB  float64   `db:"total_rub"`
	CreditRUB float64   `db:"credit_rub"`
	CreatedAt time.Time `db:"created_at"`
}

type ExchangeRate struct {
	From      string    `db:"from_currency"`
	To        string    `db:"to_currency"`
	Rate      float64   `db:"rate"`
	UpdatedAt time.Time `db:"updated_at"`
}

type InvestPosition struct {
	Ticker         string  `json:"ticker"`
	InstrumentType string  `json:"instrument_type"`
	Quantity       float64 `json:"quantity"`
	PriceRUB       float64 `json:"price_rub"`
	ValueRUB       float64 `json:"value_rub"`
}

type SteamItem struct {
	Name     string  `json:"name"`
	Quantity int     `json:"quantity"`
	PriceRUB float64 `json:"price_rub"`
	ValueRUB float64 `json:"value_rub"`
}

type ManualAsset struct {
	ID        int64     `db:"id"`
	UserID    int64     `db:"user_id"`
	Name      string    `db:"name"`
	Type      AssetType `db:"type"`
	Amount    float64   `db:"amount"`
	Currency  string    `db:"currency"`
	CreatedAt time.Time `db:"created_at"`
}

type IntegrationInstance struct {
	ID      int64  `db:"id"`
	UserID  int64  `db:"user_id"`
	Type    string `db:"type"`
	Label   string `db:"label"`
	Config  string `db:"config"`
	Enabled bool   `db:"enabled"`
}

type UserSettings struct {
	UserID             int64   `db:"user_id"`
	RiskLevel          string  `db:"risk_level"`
	MaxLossPct         float64 `db:"max_loss_pct"`
	ClaudeAPIKey       string  `db:"claude_api_key"`
	TInvestTradeToken  string  `db:"tinvest_trade_token"`
	BotEnabled         bool    `db:"bot_enabled"`
	BotIntervalMinutes int     `db:"bot_interval_minutes"`
	BotUseMargin       bool    `db:"bot_use_margin"`
}

type BotLog struct {
	ID        int64     `db:"id"`
	UserID    int64     `db:"user_id"`
	CreatedAt time.Time `db:"created_at"`
	Level     string    `db:"level"`
	Message   string    `db:"message"`
}

type TradeRecommendation struct {
	Action    string `json:"action"`
	Ticker    string `json:"ticker"`
	Quantity  int    `json:"quantity"`
	Reasoning string `json:"reasoning"`
}

type ChartSegment struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Color string  `json:"color"`
}

type DashboardData struct {
	Assets          []Asset
	BankAssets      []Asset
	CryptoAssets    []Asset
	InvestAssets    []Asset
	InvestPositions map[string][]InvestPosition
	SteamAssets     []Asset
	SteamItems      map[string][]SteamItem
	CreditCards     []Asset
	ManualAssets    []Asset // cash/manual entries converted to Asset format
	TotalRUB        float64
	CreditTotalRUB  float64
	BankTotalRUB    float64
	CryptoTotalRUB  float64
	InvestTotalRUB  float64
	SteamTotalRUB   float64
	ManualTotalRUB  float64
	LastUpdated     time.Time
	Error           string
	Rates           map[string]float64
	ChartData       string // JSON for Chart.js
}

func (d *DashboardData) BuildChartData() {
	var segments []ChartSegment
	if d.BankTotalRUB > 0 {
		segments = append(segments, ChartSegment{"Банк", d.BankTotalRUB, "#448aff"})
	}
	if d.InvestTotalRUB > 0 {
		segments = append(segments, ChartSegment{"Инвестиции", d.InvestTotalRUB, "#00e676"})
	}
	if d.CryptoTotalRUB > 0 {
		segments = append(segments, ChartSegment{"Крипто", d.CryptoTotalRUB, "#f7931a"})
	}
	if d.SteamTotalRUB > 0 {
		segments = append(segments, ChartSegment{"Steam", d.SteamTotalRUB, "#66c0f4"})
	}
	if d.ManualTotalRUB > 0 {
		segments = append(segments, ChartSegment{"Наличные", d.ManualTotalRUB, "#ffc107"})
	}
	b, _ := json.Marshal(segments)
	d.ChartData = string(b)
}
