package steam

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/HeMMars4/simple-finance/internal/models"
)

type Client struct {
	steamID string
	appID   string
	http    *http.Client
}

func New(steamID, appID string) *Client {
	if appID == "" {
		appID = "730"
	}
	return &Client{
		steamID: steamID,
		appID:   appID,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

type invAsset struct {
	ClassID    string `json:"classid"`
	InstanceID string `json:"instanceid"`
	Amount     string `json:"amount"`
}

type invDescription struct {
	ClassID        string `json:"classid"`
	InstanceID     string `json:"instanceid"`
	Name           string `json:"name"`
	MarketHashName string `json:"market_hash_name"`
	Marketable     int    `json:"marketable"`
}

type invResponse struct {
	Assets       []invAsset       `json:"assets"`
	Descriptions []invDescription `json:"descriptions"`
	Success      int              `json:"success"`
	Error        string           `json:"Error"`
}

type priceResponse struct {
	Success     bool   `json:"success"`
	MedianPrice string `json:"median_price"`
	LowestPrice string `json:"lowest_price"`
}

// parseRUBPrice parses Steam market prices like "5 027,50 ₽".
// Works by stripping all non-numeric characters except the rightmost comma/period
// (decimal separator), so U+00A0 thousands separators are handled automatically.
func parseRUBPrice(s string) float64 {
	var result []byte
	foundDecimal := false
	// scan right-to-left to find decimal separator first
	for i := len(s) - 1; i >= 0; i-- {
		ch := s[i]
		if ch >= '0' && ch <= '9' {
			result = append(result, ch)
		} else if (ch == ',' || ch == '.') && !foundDecimal {
			result = append(result, '.')
			foundDecimal = true
		}
	}
	// result is reversed, flip it
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	f, _ := strconv.ParseFloat(string(result), 64)
	return f
}

var errNotFound = fmt.Errorf("not found")

func (c *Client) get(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusTooManyRequests:
		return fmt.Errorf("rate limited (429)")
	case http.StatusNotFound, http.StatusForbidden, http.StatusBadRequest:
		return errNotFound
	default:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) == 0 || string(body) == "null" {
		return errNotFound
	}
	return json.Unmarshal(body, out)
}

func (c *Client) fetchInventory(ctx context.Context) (*invResponse, error) {
	u := fmt.Sprintf("https://steamcommunity.com/inventory/%s/%s/2?l=english&count=2000",
		c.steamID, c.appID)
	var inv invResponse
	if err := c.get(ctx, u, &inv); err != nil {
		if err == errNotFound {
			return nil, fmt.Errorf("steam inventory not found (check SteamID64, app ID, and that inventory is public)")
		}
		return nil, err
	}
	if inv.Error != "" {
		return nil, fmt.Errorf("steam inventory: %s", inv.Error)
	}
	if inv.Success != 1 {
		return nil, fmt.Errorf("steam inventory: request not successful")
	}
	return &inv, nil
}

// fetchPrice returns the item price in USD (currency=1, global market).
func (c *Client) fetchPrice(ctx context.Context, hashName string) float64 {
	u := fmt.Sprintf(
		"https://steamcommunity.com/market/priceoverview/?country=TR&currency=1&appid=%s&market_hash_name=%s",
		c.appID, url.QueryEscape(hashName),
	)
	var pr priceResponse
	if err := c.get(ctx, u, &pr); err != nil {
		slog.Warn("steam: price fetch failed", "item", hashName, "err", err)
		return 0
	}
	if !pr.Success {
		slog.Warn("steam: no price data", "item", hashName, "median", pr.MedianPrice, "lowest", pr.LowestPrice)
		return 0
	}
	raw := pr.MedianPrice
	if raw == "" {
		raw = pr.LowestPrice
	}
	priceUSD := parseRUBPrice(raw) // parser is currency-agnostic
	slog.Debug("steam: price", "item", hashName, "median", pr.MedianPrice, "lowest", pr.LowestPrice, "usd", priceUSD)
	return priceUSD
}

// FetchAssets returns a single Asset representing the total Steam inventory value.
// Prices are fetched in USD and converted to RUB using usdRUB.
// Only marketable items are included. Item details are stored as JSON in Extra.
func (c *Client) FetchAssets(ctx context.Context, usdRUB float64) ([]models.Asset, error) {
	inv, err := c.fetchInventory(ctx)
	if err != nil {
		slog.Warn("steam: inventory unavailable", "steamid", c.steamID, "appid", c.appID, "err", err)
		return nil, nil
	}

	descMap := make(map[string]*invDescription, len(inv.Descriptions))
	for i := range inv.Descriptions {
		d := &inv.Descriptions[i]
		descMap[d.ClassID+"_"+d.InstanceID] = d
	}

	quantities := make(map[string]int)
	displayNames := make(map[string]string)
	for _, a := range inv.Assets {
		d, ok := descMap[a.ClassID+"_"+a.InstanceID]
		if !ok || d.Marketable != 1 {
			continue
		}
		qty, _ := strconv.Atoi(a.Amount)
		if qty == 0 {
			qty = 1
		}
		quantities[d.MarketHashName] += qty
		displayNames[d.MarketHashName] = d.Name
	}

	slog.Info("steam: fetching prices", "unique_items", len(quantities))

	var items []models.SteamItem
	var totalRUB float64

	for hashName, qty := range quantities {
		if ctx.Err() != nil {
			break
		}
		priceUSD := c.fetchPrice(ctx, hashName)
		priceRUB := priceUSD * usdRUB
		valueRUB := priceRUB * float64(qty)
		totalRUB += valueRUB
		items = append(items, models.SteamItem{
			Name:     displayNames[hashName],
			Quantity: qty,
			PriceRUB: priceRUB,
			ValueRUB: valueRUB,
		})
		time.Sleep(1 * time.Second)
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].ValueRUB > items[j].ValueRUB
	})

	extra, _ := json.Marshal(items)

	slog.Info("steam: inventory fetched", "items", len(items), "totalRUB", totalRUB)

	return []models.Asset{{
		Source:    "steam",
		Name:      "Steam Inventory",
		Type:      models.AssetTypeSteam,
		AmountRaw: totalRUB,
		Currency:  "RUB",
		AmountRUB: totalRUB,
		Extra:     string(extra),
	}}, nil
}
