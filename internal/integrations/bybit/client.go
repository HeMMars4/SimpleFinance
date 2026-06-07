package bybit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
				Coin            string `json:"coin"`
				WalletBalance   string `json:"walletBalance"`
				UsdValue        string `json:"usdValue"`
			} `json:"coin"`
		} `json:"list"`
	} `json:"result"`
}

// FetchSpotAssets fetches SPOT wallet balances
func (c *Client) FetchSpotAssets(ctx context.Context, usdRUBRate float64) ([]models.Asset, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("bybit API key not configured")
	}

	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	recvWindow := "5000"
	queryStr := "accountType=SPOT"

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
	if walletResp.RetCode != 0 {
		return nil, fmt.Errorf("bybit API error: %s", walletResp.RetMsg)
	}

	var assets []models.Asset
	for _, account := range walletResp.Result.List {
		for _, coin := range account.Coin {
			balance, err := strconv.ParseFloat(coin.WalletBalance, 64)
			if err != nil || balance == 0 {
				continue
			}
			usdVal, _ := strconv.ParseFloat(coin.UsdValue, 64)
			amountRUB := usdVal * usdRUBRate

			assets = append(assets, models.Asset{
				Source:    "bybit",
				Name:      fmt.Sprintf("Bybit Spot %s", coin.Coin),
				Type:      models.AssetTypeExchange,
				AmountRaw: balance,
				Currency:  coin.Coin,
				AmountRUB: amountRUB,
			})
		}
	}
	return assets, nil
}

func sign(message, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}
