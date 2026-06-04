package handlers

import (
	"context"
	"log/slog"
	"time"

	"github.com/yourname/simple-finance/config"
	"github.com/yourname/simple-finance/internal/integrations/bitcoin"
	"github.com/yourname/simple-finance/internal/integrations/bybit"
	"github.com/yourname/simple-finance/internal/integrations/monero"
	"github.com/yourname/simple-finance/internal/integrations/tbank"
	"github.com/yourname/simple-finance/internal/models"
	"github.com/yourname/simple-finance/internal/storage"
)

type Aggregator struct {
	cfg    *config.Config
	db     *storage.DB
	tbank  *tbank.Client
	bybit  *bybit.Client
	btc    *bitcoin.Client
	xmr    *monero.Client
}

func NewAggregator(cfg *config.Config, db *storage.DB) *Aggregator {
	return &Aggregator{
		cfg:   cfg,
		db:    db,
		tbank: tbank.New(cfg.TBank.SessionID),
		bybit: bybit.New(cfg.Bybit.APIKey, cfg.Bybit.APISecret),
		btc:   bitcoin.New(cfg.BTCAddresses),
		xmr:   monero.New(""),
	}
}

// RefreshAll fetches all sources concurrently and persists to DB
func (a *Aggregator) RefreshAll(ctx context.Context) error {
	usdRUB := a.cfg.ManualUSDRUB

	// Try to get fresh rate from DB
	if rate, err := a.db.GetRate(ctx, "USD", "RUB"); err == nil && rate > 0 {
		usdRUB = rate
	}
	xmrRUB, _ := a.db.GetRate(ctx, "XMR", "RUB")
	if xmrRUB == 0 {
		xmrRUB = 18000
	}

	type result struct {
		source string
		assets []models.Asset
		err    error
	}
	ch := make(chan result, 4)

	// T-Bank
	go func() {
		assets, err := a.tbank.FetchAssets(ctx, usdRUB)
		ch <- result{"tbank", assets, err}
	}()

	// Bybit
	go func() {
		assets, err := a.bybit.FetchSpotAssets(ctx, usdRUB)
		ch <- result{"bybit", assets, err}
	}()

	// Bitcoin
	go func() {
		assets, err := a.btc.FetchAssets(ctx, usdRUB)
		ch <- result{"bitcoin", assets, err}
	}()

	// Monero
	go func() {
		asset, err := a.xmr.FetchAsset(ctx, xmrRUB)
		var assets []models.Asset
		if asset != nil {
			assets = []models.Asset{*asset}
		}
		ch <- result{"monero", assets, err}
	}()

	var hasError bool
	for i := 0; i < 4; i++ {
		r := <-ch
		if r.err != nil {
			slog.Error("fetch failed", "source", r.source, "err", r.err)
			hasError = true
			continue
		}
		if len(r.assets) == 0 {
			continue
		}
		if err := a.db.SaveAssets(ctx, r.source, r.assets); err != nil {
			slog.Error("save failed", "source", r.source, "err", err)
			hasError = true
		}
	}

	// Always save a snapshot (even partial)
	if allAssets, err := a.db.GetAllAssets(ctx); err == nil {
		var total, credit float64
		for _, a := range allAssets {
			if a.Type == models.AssetTypeCreditCard {
				credit += a.AmountRUB
			} else {
				total += a.AmountRUB
			}
		}
		_ = a.db.SaveSnapshot(ctx, total, credit)
	}

	if hasError {
		return context.DeadlineExceeded // soft error — partial data available
	}
	return nil
}

// BuildDashboard assembles DashboardData from DB
func (a *Aggregator) BuildDashboard(ctx context.Context) (*models.DashboardData, error) {
	assets, err := a.db.GetAllAssets(ctx)
	if err != nil {
		return nil, err
	}

	data := &models.DashboardData{
		Rates:       map[string]float64{},
		LastUpdated: time.Now(),
	}

	if rate, err := a.db.GetRate(ctx, "USD", "RUB"); err == nil {
		data.Rates["USD"] = rate
	}

	for _, asset := range assets {
		switch asset.Type {
		case models.AssetTypeCreditCard:
			data.CreditCards = append(data.CreditCards, asset)
			data.CreditTotalRUB += asset.AmountRUB
		case models.AssetTypeBankCurrent, models.AssetTypeBankSaving:
			data.BankAssets = append(data.BankAssets, asset)
			data.TotalRUB += asset.AmountRUB
		case models.AssetTypeCrypto, models.AssetTypeExchange:
			data.CryptoAssets = append(data.CryptoAssets, asset)
			data.TotalRUB += asset.AmountRUB
		}
		data.Assets = append(data.Assets, asset)
	}

	return data, nil
}
