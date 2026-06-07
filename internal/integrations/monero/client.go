package monero

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/HeMMars4/simple-finance/internal/models"
)

type Client struct {
	rpcURL        string
	address       string
	viewKey       string
	restoreHeight uint64
	http          *http.Client
}

func New(rpcURL, address, viewKey string, restoreHeight uint64) *Client {
	if rpcURL == "" {
		rpcURL = "http://localhost:18082/json_rpc"
	}
	return &Client{
		rpcURL:        rpcURL,
		address:       address,
		viewKey:       viewKey,
		restoreHeight: restoreHeight,
		http:          &http.Client{Timeout: 60 * time.Second},
	}
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

type balanceResult struct {
	Balance         uint64 `json:"balance"`
	UnlockedBalance uint64 `json:"unlocked_balance"`
}

type rpcResponse[T any] struct {
	Result T         `json:"result"`
	Error  *rpcError `json:"error"`
}

// walletName returns a filename that encodes the restore height so that
// changing restore_height forces a fresh wallet scan from the new block.
func (c *Client) walletName() string {
	if c.restoreHeight > 0 {
		return fmt.Sprintf("viewonly_%d", c.restoreHeight)
	}
	return "viewonly"
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	payload, _ := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      "0",
		Method:  method,
		Params:  params,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rpcURL,
		strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return json.Unmarshal(body, out)
}

// ensureWallet opens or creates the view-only wallet.
// If open_wallet fails for any reason, attempts generate_from_keys.
// -14 ("already exists") after generate means the file is there but wasn't opened;
// we try open_wallet once more in that case.
func (c *Client) ensureWallet(ctx context.Context) error {
	name := c.walletName()

	var openResp rpcResponse[struct{}]
	if err := c.call(ctx, "open_wallet", map[string]any{
		"filename": name,
		"password": "",
	}, &openResp); err != nil {
		return fmt.Errorf("monero rpc unreachable: %w", err)
	}
	if openResp.Error == nil {
		return nil // opened successfully
	}

	slog.Debug("monero: open_wallet failed, attempting create",
		"name", name, "code", openResp.Error.Code, "msg", openResp.Error.Message)

	if c.address == "" || c.viewKey == "" {
		return fmt.Errorf("monero: wallet unavailable (%s) and no address/view-key configured", openResp.Error.Message)
	}

	slog.Info("monero: creating view-only wallet", "name", name, "restore_height", c.restoreHeight)
	var genResp rpcResponse[struct{ Info string `json:"info"` }]
	if err := c.call(ctx, "generate_from_keys", map[string]any{
		"filename":         name,
		"address":          c.address,
		"viewkey":          c.viewKey,
		"restore_height":   c.restoreHeight,
		"password":         "",
		"autosave_current": true,
	}, &genResp); err != nil {
		return fmt.Errorf("generate_from_keys rpc: %w", err)
	}
	if genResp.Error == nil {
		return nil // created and opened
	}
	// -14 = file already exists but wasn't open; retry open
	if genResp.Error.Code == -14 {
		var r2 rpcResponse[struct{}]
		if err := c.call(ctx, "open_wallet", map[string]any{"filename": name, "password": ""}, &r2); err != nil {
			return fmt.Errorf("monero rpc: %w", err)
		}
		if r2.Error != nil {
			return fmt.Errorf("monero: open_wallet retry: %s", r2.Error.Message)
		}
		return nil
	}
	return fmt.Errorf("generate_from_keys: %s", genResp.Error.Message)
}

// FetchAsset queries the unlocked XMR balance via monero-wallet-rpc.
// Returns nil (no error) if RPC is not reachable — wallet is optional.
func (c *Client) FetchAsset(ctx context.Context, xmrRUBRate float64) (*models.Asset, error) {
	if err := c.ensureWallet(ctx); err != nil {
		slog.Warn("monero: wallet unavailable", "err", err)
		return nil, nil
	}

	var resp rpcResponse[balanceResult]
	if err := c.call(ctx, "get_balance", map[string]any{"account_index": 0}, &resp); err != nil {
		slog.Warn("monero: get_balance failed", "err", err)
		return nil, nil
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("monero get_balance: %s", resp.Error.Message)
	}

	balanceXMR := float64(resp.Result.UnlockedBalance) / 1e12
	slog.Info("monero: balance fetched", "xmr", balanceXMR)

	return &models.Asset{
		Source:    "monero",
		Name:      "Monero Wallet",
		Type:      models.AssetTypeCrypto,
		AmountRaw: balanceXMR,
		Currency:  "XMR",
		AmountRUB: balanceXMR * xmrRUBRate,
	}, nil
}
