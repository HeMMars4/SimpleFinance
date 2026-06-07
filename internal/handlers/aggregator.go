package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/HeMMars4/simple-finance/config"
	"github.com/HeMMars4/simple-finance/internal/integrations/bitcoin"
	"github.com/HeMMars4/simple-finance/internal/integrations/bybit"
	"github.com/HeMMars4/simple-finance/internal/integrations/monero"
	"github.com/HeMMars4/simple-finance/internal/integrations/rates"
	"github.com/HeMMars4/simple-finance/internal/integrations/steam"
	"github.com/HeMMars4/simple-finance/internal/integrations/tbank"
	"github.com/HeMMars4/simple-finance/internal/integrations/tinvest"
	"github.com/HeMMars4/simple-finance/internal/models"
	"github.com/HeMMars4/simple-finance/internal/storage"
)

type Aggregator struct {
	cfg   *config.Config
	db    *storage.DB
	rates *rates.Client
}

func NewAggregator(cfg *config.Config, db *storage.DB) *Aggregator {
	return &Aggregator{
		cfg:   cfg,
		db:    db,
		rates: rates.New(),
	}
}

func dbKey(keys map[string]string, key, fallback string) string {
	if v, ok := keys[key]; ok && v != "" {
		return v
	}
	return fallback
}

func splitAddresses(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, a := range strings.Split(s, ",") {
		a = strings.TrimSpace(a)
		if a != "" {
			out = append(out, a)
		}
	}
	return out
}

// RefreshAll fetches all sources for a specific user and persists to DB.
func (a *Aggregator) RefreshAll(ctx context.Context, userID int64) error {
	keys, _ := a.db.GetAllAPIKeys(ctx, userID)

	tbankSessionID := dbKey(keys, "tbank_session_id", a.cfg.TBank.SessionID)
	tinvestToken := dbKey(keys, "tinvest_token", a.cfg.TInvest.Token)
	bybitKey := dbKey(keys, "bybit_api_key", a.cfg.Bybit.APIKey)
	bybitSecret := dbKey(keys, "bybit_api_secret", a.cfg.Bybit.APISecret)
	btcAddresses := splitAddresses(dbKey(keys, "btc_addresses", strings.Join(a.cfg.BTCAddresses, ",")))
	moneroRPCURL := dbKey(keys, "monero_rpc_url", a.cfg.Monero.RPCURL)
	moneroAddress := dbKey(keys, "monero_address", "")
	moneroViewKey := dbKey(keys, "monero_view_key", "")
	steamID := dbKey(keys, "steam_id", a.cfg.Steam.SteamID)
	steamAppID := dbKey(keys, "steam_app_id", "730")

	moneroRestoreHeight, _ := strconv.ParseUint(dbKey(keys, "monero_restore_height", "0"), 10, 64)

	var tbankClient *tbank.Client
	if tbankSessionID != "" {
		tbankClient = tbank.New(tbankSessionID)
	}
	var tinvestClient *tinvest.Client
	if tinvestToken != "" {
		tinvestClient = tinvest.New(tinvestToken)
	}
	var bybitClient *bybit.Client
	if bybitKey != "" && bybitSecret != "" {
		bybitClient = bybit.New(bybitKey, bybitSecret)
	}
	btcClient := bitcoin.New(btcAddresses)

	usdRUB := a.cfg.ManualUSDRUB
	if rate, err := a.rates.GetUSDRUB(ctx); err == nil && rate > 0 {
		usdRUB = rate
		slog.Info("USD/RUB rate fetched from network", "rate", usdRUB)
	} else {
		slog.Warn("USD/RUB network fetch failed, using manual rate", "rate", usdRUB, "err", err)
	}

	xmrRUB := 18000.0
	if rate, err := a.db.GetRate(ctx, "XMR", "RUB"); err == nil && rate > 0 {
		xmrRUB = rate
	}
	if rate, err := a.rates.GetXMRRUB(ctx); err == nil && rate > 0 {
		xmrRUB = rate
		slog.Info("XMR rate fetched", "xmrRUB", rate)
	} else {
		slog.Warn("XMR/RUB fetch failed, using cached/default", "rate", xmrRUB, "err", err)
	}

	btcRUB := 8_000_000.0
	if rate, err := a.db.GetRate(ctx, "BTC", "RUB"); err == nil && rate > 0 {
		btcRUB = rate
	}
	if rate, err := a.rates.GetBTCRUB(ctx); err == nil && rate > 0 {
		btcRUB = rate
		slog.Info("BTC rate fetched", "btcRUB", rate)
	} else {
		slog.Warn("BTC/RUB fetch failed, using cached/default", "rate", btcRUB, "err", err)
	}

	type result struct {
		source string
		assets []models.Asset
		err    error
	}
	ch := make(chan result, 20)
	var n int

	if tbankClient != nil {
		n++
		go func() {
			assets, err := tbankClient.FetchAssets(ctx, usdRUB)
			ch <- result{"tbank", assets, err}
		}()
	}

	if tinvestClient != nil {
		n++
		go func() {
			assets, err := tinvestClient.FetchAssets(ctx, usdRUB)
			ch <- result{"tinvest", assets, err}
		}()
	}

	if bybitClient != nil {
		n++
		go func() {
			assets, err := bybitClient.FetchSpotAssets(ctx, usdRUB)
			ch <- result{"bybit", assets, err}
		}()
	}

	n++
	go func() {
		assets, err := btcClient.FetchAssets(ctx, btcRUB)
		ch <- result{"bitcoin", assets, err}
	}()

	n++
	go func() {
		mCtx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
		defer cancel()
		xmrClient := monero.New(moneroRPCURL, moneroAddress, moneroViewKey, moneroRestoreHeight)
		asset, err := xmrClient.FetchAsset(mCtx, xmrRUB)
		var assets []models.Asset
		if asset != nil {
			assets = []models.Asset{*asset}
		}
		ch <- result{"monero", assets, err}
	}()

	if steamID != "" {
		n++
		go func() {
			sCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			steamClient := steam.New(steamID, steamAppID)
			assets, err := steamClient.FetchAssets(sCtx, usdRUB)
			ch <- result{"steam", assets, err}
		}()
	}

	// Extra integration instances
	extraInstances, _ := a.db.GetIntegrationInstances(ctx, userID)
	for _, inst := range extraInstances {
		if !inst.Enabled {
			continue
		}
		inst := inst
		source := fmt.Sprintf("%s_%d", inst.Type, inst.ID)

		switch inst.Type {
		case "steam":
			var cfg struct {
				SteamID string `json:"steam_id"`
				AppID   string `json:"app_id"`
			}
			json.Unmarshal([]byte(inst.Config), &cfg)
			if cfg.SteamID == "" {
				continue
			}
			appID := cfg.AppID
			if appID == "" {
				appID = "730"
			}
			n++
			go func() {
				sCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				sc := steam.New(cfg.SteamID, appID)
				assets, err := sc.FetchAssets(sCtx, usdRUB)
				for i := range assets {
					assets[i].Source = source
					assets[i].Name = inst.Label
				}
				ch <- result{source, assets, err}
			}()

		case "monero":
			var cfg struct {
				Address       string `json:"address"`
				ViewKey       string `json:"view_key"`
				RestoreHeight uint64 `json:"restore_height"`
				RPCURL        string `json:"rpc_url"`
			}
			json.Unmarshal([]byte(inst.Config), &cfg)
			if cfg.Address == "" {
				continue
			}
			n++
			go func() {
				mCtx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
				defer cancel()
				mc := monero.New(cfg.RPCURL, cfg.Address, cfg.ViewKey, cfg.RestoreHeight)
				asset, err := mc.FetchAsset(mCtx, xmrRUB)
				var assets []models.Asset
				if asset != nil {
					asset.Source = source
					asset.Name = inst.Label
					assets = []models.Asset{*asset}
				}
				ch <- result{source, assets, err}
			}()

		case "btc":
			var cfg struct {
				Addresses string `json:"addresses"`
			}
			json.Unmarshal([]byte(inst.Config), &cfg)
			addrs := splitAddresses(cfg.Addresses)
			if len(addrs) == 0 {
				continue
			}
			n++
			go func() {
				bc := bitcoin.New(addrs)
				assets, err := bc.FetchAssets(ctx, btcRUB)
				for i := range assets {
					assets[i].Source = source
					if inst.Label != "" {
						assets[i].Name = inst.Label + " " + assets[i].Name
					}
				}
				ch <- result{source, assets, err}
			}()
		}
	}

	var failed []string
	for i := 0; i < n; i++ {
		r := <-ch
		if r.err != nil {
			slog.Error("fetch failed", "source", r.source, "err", r.err)
			failed = append(failed, r.source)
			continue
		}
		if len(r.assets) == 0 {
			slog.Debug("no assets fetched", "source", r.source)
			continue
		}
		if err := a.db.SaveAssets(ctx, userID, r.source, r.assets); err != nil {
			slog.Error("save failed", "source", r.source, "err", err)
			failed = append(failed, r.source)
		} else {
			slog.Info("assets saved", "source", r.source, "count", len(r.assets))
		}
	}

	if allAssets, err := a.db.GetAllAssets(ctx, userID); err == nil {
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

	_ = a.db.UpsertRate(ctx, "USD", "RUB", usdRUB)
	_ = a.db.UpsertRate(ctx, "XMR", "RUB", xmrRUB)
	_ = a.db.UpsertRate(ctx, "BTC", "RUB", btcRUB)

	if len(failed) > 0 {
		return fmt.Errorf("источники недоступны: %s", strings.Join(failed, ", "))
	}
	return nil
}

// BuildDashboard assembles DashboardData from DB for a specific user.
// Hidden assets (per asset_filters) are excluded.
// Manual assets are included with RUB conversion.
func (a *Aggregator) BuildDashboard(ctx context.Context, userID int64) (*models.DashboardData, error) {
	assets, err := a.db.GetAllAssets(ctx, userID)
	if err != nil {
		return nil, err
	}

	hiddenAssets, _ := a.db.GetHiddenAssets(ctx, userID)

	data := &models.DashboardData{
		Rates:       map[string]float64{},
		LastUpdated: time.Now(),
	}

	// Fetch cached rates for display and manual-asset conversion
	rateValues := map[string]float64{"USD": 90.0, "BTC": 8_000_000.0, "XMR": 18_000.0}
	for _, cur := range []string{"USD", "BTC", "XMR"} {
		if r, err := a.db.GetRate(ctx, cur, "RUB"); err == nil && r > 0 {
			rateValues[cur] = r
			data.Rates[cur] = r
		}
	}

	for _, asset := range assets {
		key := asset.Source + ":" + asset.Name
		if hiddenAssets[key] {
			continue
		}
		switch asset.Type {
		case models.AssetTypeCreditCard:
			data.CreditCards = append(data.CreditCards, asset)
			data.CreditTotalRUB += asset.AmountRUB
		case models.AssetTypeBankCurrent, models.AssetTypeBankSaving:
			data.BankAssets = append(data.BankAssets, asset)
			data.BankTotalRUB += asset.AmountRUB
			data.TotalRUB += asset.AmountRUB
		case models.AssetTypeCrypto, models.AssetTypeExchange:
			data.CryptoAssets = append(data.CryptoAssets, asset)
			data.CryptoTotalRUB += asset.AmountRUB
			data.TotalRUB += asset.AmountRUB
		case models.AssetTypeInvest:
			data.InvestAssets = append(data.InvestAssets, asset)
			data.InvestTotalRUB += asset.AmountRUB
			data.TotalRUB += asset.AmountRUB
			if asset.Extra != "" && asset.Extra != "null" {
				var positions []models.InvestPosition
				if err := json.Unmarshal([]byte(asset.Extra), &positions); err == nil {
					if data.InvestPositions == nil {
						data.InvestPositions = make(map[string][]models.InvestPosition)
					}
					data.InvestPositions[asset.Name] = positions
				}
			}
		case models.AssetTypeSteam:
			data.SteamAssets = append(data.SteamAssets, asset)
			data.SteamTotalRUB += asset.AmountRUB
			data.TotalRUB += asset.AmountRUB
			if asset.Extra != "" && asset.Extra != "null" {
				var items []models.SteamItem
				if err := json.Unmarshal([]byte(asset.Extra), &items); err == nil {
					if data.SteamItems == nil {
						data.SteamItems = make(map[string][]models.SteamItem)
					}
					data.SteamItems[asset.Name] = items
				}
			}
		}
		data.Assets = append(data.Assets, asset)
	}

	// Manual assets (cash entries)
	manuals, _ := a.db.GetManualAssets(ctx, userID)
	for _, ma := range manuals {
		var amountRUB float64
		switch ma.Currency {
		case "RUB":
			amountRUB = ma.Amount
		case "USD":
			amountRUB = ma.Amount * rateValues["USD"]
		case "BTC":
			amountRUB = ma.Amount * rateValues["BTC"]
		case "XMR":
			amountRUB = ma.Amount * rateValues["XMR"]
		default:
			amountRUB = ma.Amount
		}
		asset := models.Asset{
			Source:    "manual",
			Name:      ma.Name,
			Type:      models.AssetTypeCash,
			AmountRaw: ma.Amount,
			Currency:  ma.Currency,
			AmountRUB: amountRUB,
		}
		data.ManualAssets = append(data.ManualAssets, asset)
		data.ManualTotalRUB += amountRUB
		data.TotalRUB += amountRUB
	}

	data.BuildChartData()
	return data, nil
}
