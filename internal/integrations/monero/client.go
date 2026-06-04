package monero

// Monero is privacy-focused — balance can't be fetched from a public explorer
// without a view key. Options:
//
// Option A (recommended): monero-wallet-rpc
//   Run a local wallet RPC daemon with your view-only wallet.
//   This client calls it on localhost.
//
// Option B: Manual entry via the dashboard UI (simplest for now).
//
// This file implements Option A skeleton; Option B is handled by the manual_asset handler.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yourname/simple-finance/internal/models"
)

type Client struct {
	rpcURL string
	http   *http.Client
}

func New(rpcURL string) *Client {
	if rpcURL == "" {
		rpcURL = "http://localhost:18082/json_rpc"
	}
	return &Client{
		rpcURL: rpcURL,
		http:   &http.Client{Timeout: 15 * time.Second},
	}
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
}

type balanceResponse struct {
	Result struct {
		Balance         uint64 `json:"balance"`
		UnlockedBalance uint64 `json:"unlocked_balance"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// FetchAsset calls monero-wallet-rpc to get the balance.
// Returns nil (no error) if RPC is not reachable — wallet is optional.
func (c *Client) FetchAsset(ctx context.Context, xmrRUBRate float64) (*models.Asset, error) {
	payload, _ := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      "0",
		Method:  "get_balance",
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rpcURL,
		strings.NewReader(string(payload)))
	if err != nil {
		return nil, nil // RPC not configured, skip silently
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil // not running, skip silently
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result balanceResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("monero parse: %w", err)
	}
	if result.Error != nil {
		return nil, fmt.Errorf("monero rpc: %s", result.Error.Message)
	}

	// Balance is in piconero (1 XMR = 1e12 piconero)
	balanceXMR := float64(result.Result.UnlockedBalance) / 1e12
	if balanceXMR == 0 {
		return nil, nil
	}

	return &models.Asset{
		Source:    "monero",
		Name:      "Monero Wallet",
		Type:      models.AssetTypeCrypto,
		AmountRaw: balanceXMR,
		Currency:  "XMR",
		AmountRUB: balanceXMR * xmrRUBRate,
	}, nil
}
