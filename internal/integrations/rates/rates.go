package rates

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	http *http.Client
}

func New() *Client {
	return &Client{
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

// GetUSDRUB fetches USD/RUB from the CBR JSON mirror (cbr-xml-daily.ru).
func (c *Client) GetUSDRUB(ctx context.Context) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://www.cbr-xml-daily.ru/daily_json.js", nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("cbr-json request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Valute map[string]struct {
			Value   float64 `json:"Value"`
			Nominal int     `json:"Nominal"`
		} `json:"Valute"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("cbr-json parse: %w", err)
	}
	usd, ok := result.Valute["USD"]
	if !ok || usd.Nominal == 0 {
		return 0, fmt.Errorf("cbr-json: USD not found")
	}
	return usd.Value / float64(usd.Nominal), nil
}

// GetBTCRUB fetches BTC/RUB from the CoinGecko public API.
func (c *Client) GetBTCRUB(ctx context.Context) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.coingecko.com/api/v3/simple/price?ids=bitcoin&vs_currencies=rub", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("coingecko btc request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Bitcoin struct {
			RUB float64 `json:"rub"`
		} `json:"bitcoin"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("coingecko btc parse: %w", err)
	}
	if result.Bitcoin.RUB == 0 {
		return 0, fmt.Errorf("coingecko: BTC/RUB not found or zero")
	}
	return result.Bitcoin.RUB, nil
}

// GetXMRRUB fetches XMR/RUB from the CoinGecko public API.
func (c *Client) GetXMRRUB(ctx context.Context) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.coingecko.com/api/v3/simple/price?ids=monero&vs_currencies=rub", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("coingecko request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Monero struct {
			RUB float64 `json:"rub"`
		} `json:"monero"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("coingecko parse: %w", err)
	}
	if result.Monero.RUB == 0 {
		return 0, fmt.Errorf("coingecko: XMR/RUB not found or zero")
	}
	return result.Monero.RUB, nil
}
