package bybit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/HeMMars4/simple-finance/internal/models"
)

const baseURL = "https://api.bybit.com"

type Client struct {
	apiKey    string
	apiSecret string
	http      *http.Client
}

func New(apiKey, apiSecret string) *Client {
	return &Client{
		apiKey:    apiKey,
		apiSecret: apiSecret,
		http:      &http.Client{Timeout: 15 * time.Second},
	}
}

type walletResponse struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		List []struct {
			AccountType string `json:"accountType"`
			Coin        []struct {
				Coin          string `json:"coin"`
				WalletBalance string `json:"walletBalance"`
				UsdValue      string `json:"usdValue"`
			} `json:"coin"`
		} `json:"list"`
	} `json:"result"`
}

type fundingResponse struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		AccountType string `json:"accountType"`
		Balance     []struct {
			Coin            string `json:"coin"`
			WalletBalance   string `json:"walletBalance"`
			TransferBalance string `json:"transferBalance"`
			Bonus           string `json:"bonus"`
		} `json:"balance"`
	} `json:"result"`
}

// FetchSpotAssets fetches wallet balances from all account types (UNIFIED, SPOT, FUND)
func (c *Client) FetchSpotAssets(ctx context.Context, usdRUBRate float64) ([]models.Asset, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("bybit API key not configured")
	}

	var allAssets []models.Asset

	for _, accountType := range []string{"UNIFIED", "SPOT"} {
		assets, err := c.fetchFromAccountType(ctx, accountType, usdRUBRate)
		if err != nil {
			slog.Debug("bybit fetch skipped", "accountType", accountType, "err", err)
			continue
		}
		allAssets = append(allAssets, assets...)
	}

	fundingAssets, err := c.fetchFundingAssets(ctx, usdRUBRate)
	if err != nil {
		slog.Warn("bybit funding fetch failed", "err", err)
	} else {
		allAssets = append(allAssets, fundingAssets...)
	}

	if len(allAssets) == 0 {
		return nil, fmt.Errorf("no assets found in any account type")
	}

	slog.Debug("bybit assets fetched", "count", len(allAssets), "rate", usdRUBRate)
	return allAssets, nil
}

// fetchFromAccountType fetches assets from a specific account type
func (c *Client) fetchFromAccountType(ctx context.Context, accountType string, usdRUBRate float64) ([]models.Asset, error) {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	recvWindow := "5000"
	queryStr := fmt.Sprintf("accountType=%s", accountType)

	// Bybit v5 signature: timestamp + apiKey + recvWindow + queryString
	raw := timestamp + c.apiKey + recvWindow + queryStr
	sig := sign(raw, c.apiSecret)

	url := fmt.Sprintf("%s/v5/account/wallet-balance?%s", baseURL, queryStr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-BAPI-API-KEY", c.apiKey)
	req.Header.Set("X-BAPI-SIGN", sig)
	req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bybit request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var walletResp walletResponse
	if err := json.Unmarshal(body, &walletResp); err != nil {
		return nil, fmt.Errorf("bybit parse: %w", err)
	}
	
	slog.Debug("bybit raw response", "accountType", accountType, "retCode", walletResp.RetCode, "retMsg", walletResp.RetMsg, "accounts", len(walletResp.Result.List))
	
	if walletResp.RetCode != 0 {
		return nil, fmt.Errorf("bybit API error: %s", walletResp.RetMsg)
	}

	var assets []models.Asset
	for _, account := range walletResp.Result.List {
		slog.Debug("bybit account fetched", "accountType", accountType, "coinCount", len(account.Coin))
		for _, coin := range account.Coin {
			balance, err := strconv.ParseFloat(coin.WalletBalance, 64)
			if err != nil {
				slog.Debug("bybit coin parse error", "coin", coin.Coin, "balance", coin.WalletBalance, "err", err)
				continue
			}
			if balance <= 0 {
				slog.Debug("bybit coin zero balance", "coin", coin.Coin, "balance", balance)
				continue
			}
			usdVal, err := strconv.ParseFloat(coin.UsdValue, 64)
			if err != nil {
				slog.Debug("bybit coin usd parse error", "coin", coin.Coin, "usdValue", coin.UsdValue, "err", err)
				continue
			}
			if usdVal <= 0 {
				slog.Debug("bybit coin zero usd value", "coin", coin.Coin, "usdValue", usdVal)
				continue
			}
			amountRUB := usdVal * usdRUBRate

			if amountRUB <= 0 {
				slog.Debug("bybit coin zero rub amount", "coin", coin.Coin, "usdVal", usdVal, "rate", usdRUBRate, "amountRUB", amountRUB)
				continue
			}

			slog.Debug("bybit coin added", "coin", coin.Coin, "balance", balance, "usdVal", usdVal, "amountRUB", amountRUB)

			name := fmt.Sprintf("Bybit %s %s", accountType, coin.Coin)
			assets = append(assets, models.Asset{
				Source:    "bybit",
				Name:      name,
				Type:      models.AssetTypeExchange,
				AmountRaw: balance,
				Currency:  coin.Coin,
				AmountRUB: amountRUB,
			})
		}
	}
	return assets, nil
}

// fetchFundingAssets fetches the funding wallet balance via the asset transfer endpoint.
// The /v5/account/wallet-balance endpoint does not support FUND — a separate API is required.
func (c *Client) fetchFundingAssets(ctx context.Context, usdRUBRate float64) ([]models.Asset, error) {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	recvWindow := "5000"
	queryStr := "accountType=FUND"

	raw := timestamp + c.apiKey + recvWindow + queryStr
	sig := sign(raw, c.apiSecret)

	url := fmt.Sprintf("%s/v5/asset/transfer/query-account-coins-balance?%s", baseURL, queryStr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-BAPI-API-KEY", c.apiKey)
	req.Header.Set("X-BAPI-SIGN", sig)
	req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bybit funding request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var fundResp fundingResponse
	if err := json.Unmarshal(body, &fundResp); err != nil {
		return nil, fmt.Errorf("bybit funding parse: %w", err)
	}
	if fundResp.RetCode != 0 {
		return nil, fmt.Errorf("bybit funding API error: %s", fundResp.RetMsg)
	}

	var assets []models.Asset
	for _, coin := range fundResp.Result.Balance {
		balance, err := strconv.ParseFloat(coin.WalletBalance, 64)
		if err != nil || balance <= 0 {
			continue
		}
		// Funding wallet doesn't return usdValue — derive from balance for USDT,
		// or skip non-stable coins without a price source.
		var usdVal float64
		if coin.Coin == "USDT" || coin.Coin == "USDC" {
			usdVal = balance
		} else {
			slog.Debug("bybit funding: skipping non-stable coin without price", "coin", coin.Coin)
			continue
		}
		amountRUB := usdVal * usdRUBRate
		if amountRUB <= 0 {
			continue
		}
		assets = append(assets, models.Asset{
			Source:    "bybit",
			Name:      fmt.Sprintf("Bybit FUND %s", coin.Coin),
			Type:      models.AssetTypeExchange,
			AmountRaw: balance,
			Currency:  coin.Coin,
			AmountRUB: amountRUB,
		})
	}
	return assets, nil
}

func sign(message, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}
