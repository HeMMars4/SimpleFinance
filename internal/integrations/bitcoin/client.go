package bitcoin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/HeMMars4/simple-finance/internal/models"
)

// Uses mempool.space public API — no auth required
const mempoolURL = "https://mempool.space/api/address/%s"

type Client struct {
	addresses []string
	http      *http.Client
}

func New(addresses []string) *Client {
	return &Client{
		addresses: addresses,
		http:      &http.Client{Timeout: 15 * time.Second},
	}
}

type addressInfo struct {
	ChainStats struct {
		FundedTxoSum int64 `json:"funded_txo_sum"` // satoshis
		SpentTxoSum  int64 `json:"spent_txo_sum"`
	} `json:"chain_stats"`
	MempoolStats struct {
		FundedTxoSum int64 `json:"funded_txo_sum"`
		SpentTxoSum  int64 `json:"spent_txo_sum"`
	} `json:"mempool_stats"`
}

func (c *Client) FetchAssets(ctx context.Context, btcRUBRate float64) ([]models.Asset, error) {
	if len(c.addresses) == 0 {
		return nil, nil
	}
	btcRUB := btcRUBRate
	slog.Debug("bitcoin rates", "btcRUB", btcRUB)

	var assets []models.Asset
	for _, addr := range c.addresses {
		if addr == "" {
			continue
		}
		info, err := c.fetchAddress(ctx, addr)
		if err != nil {
			return nil, fmt.Errorf("address %s: %w", addr, err)
		}

		// Balance = funded - spent (confirmed + mempool)
		balanceSat := (info.ChainStats.FundedTxoSum - info.ChainStats.SpentTxoSum) +
			(info.MempoolStats.FundedTxoSum - info.MempoolStats.SpentTxoSum)
		balanceBTC := float64(balanceSat) / 1e8

		if balanceBTC == 0 {
			continue
		}

		// Shorten address for display
		shortAddr := addr[:6] + "..." + addr[len(addr)-4:]

		assets = append(assets, models.Asset{
			Source:    "bitcoin",
			Name:      fmt.Sprintf("BTC %s", shortAddr),
			Type:      models.AssetTypeCrypto,
			AmountRaw: balanceBTC,
			Currency:  "BTC",
			AmountRUB: balanceBTC * btcRUB,
		})
	}
	return assets, nil
}

func (c *Client) fetchAddress(ctx context.Context, addr string) (*addressInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(mempoolURL, addr), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var info addressInfo
	return &info, json.Unmarshal(body, &info)
}
