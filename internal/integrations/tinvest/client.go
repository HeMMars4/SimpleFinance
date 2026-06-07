package tinvest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/HeMMars4/simple-finance/internal/models"
)

const baseURL = "https://invest-public-api.tinkoff.ru/rest"

type Client struct {
	token      string
	httpClient *http.Client
}

func New(token string) *Client {
	return &Client{
		token:      token,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// moneyValue represents protobuf MoneyValue (with currency) or Quotation (without)
type moneyValue struct {
	Units    string `json:"units"`
	Nano     int32  `json:"nano"`
	Currency string `json:"currency"`
}

func (m moneyValue) ToFloat() float64 {
	units, _ := strconv.ParseFloat(m.Units, 64)
	return units + float64(m.Nano)/1e9
}

func (m moneyValue) ToRUB(usdRUB float64) float64 {
	v := m.ToFloat()
	switch m.Currency {
	case "rub", "":
		return v
	case "usd":
		return v * usdRUB
	default:
		return v * usdRUB // rough fallback
	}
}

type account struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	AccessLevel string `json:"accessLevel"`
}

type accountsResponse struct {
	Accounts []account `json:"accounts"`
}

type apiPosition struct {
	FIGI           string     `json:"figi"`
	InstrumentType string     `json:"instrumentType"`
	Quantity       moneyValue `json:"quantity"`
	AveragePositionPrice moneyValue `json:"averagePositionPrice"`
	CurrentPrice   moneyValue `json:"currentPrice"`
	ExpectedYield  moneyValue `json:"expectedYield"`
	Ticker         string     `json:"ticker"`
}

// LivePosition is a portfolio position with resolved ticker and RUB values.
type LivePosition struct {
	FIGI             string
	Ticker           string
	InstrumentType   string
	Quantity         float64
	CurrentPriceRUB  float64
	ValueRUB         float64
	ExpectedYieldRUB float64
}

// PortfolioSummary is the live portfolio state fetched from T-Invest API.
type PortfolioSummary struct {
	TotalRUB  float64
	Positions []LivePosition
}

// MarginAttributes contains margin trading attributes for an account.
type MarginAttributes struct {
	LiquidPortfolio       float64
	StartingMargin        float64
	FundsSufficiencyLevel float64
}

type portfolioResponse struct {
	TotalAmountPortfolio moneyValue    `json:"totalAmountPortfolio"`
	Positions            []apiPosition `json:"positions"`
}

func (c *Client) post(ctx context.Context, endpoint string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tinvest API status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) getAccounts(ctx context.Context) ([]account, error) {
	var resp accountsResponse
	if err := c.post(ctx,
		"/tinkoff.public.invest.api.contract.v1.UsersService/GetAccounts",
		map[string]any{}, &resp,
	); err != nil {
		return nil, err
	}
	return resp.Accounts, nil
}

// GetFirstAccountID returns the ID of the first open, accessible account.
func (c *Client) GetFirstAccountID(ctx context.Context) (string, error) {
	accounts, err := c.getAccounts(ctx)
	if err != nil {
		return "", err
	}
	for _, acc := range accounts {
		if acc.Status == "ACCOUNT_STATUS_OPEN" && acc.AccessLevel != "ACCOUNT_ACCESS_LEVEL_NO_ACCESS" {
			return acc.ID, nil
		}
	}
	return "", fmt.Errorf("no open accounts")
}

func (c *Client) getPortfolio(ctx context.Context, accountID string) (*portfolioResponse, error) {
	var resp portfolioResponse
	err := c.post(ctx,
		"/tinkoff.public.invest.api.contract.v1.OperationsService/GetPortfolio",
		map[string]any{"accountId": accountID},
		&resp,
	)
	return &resp, err
}

// FetchAssets returns one Asset per open account. Each asset has positions JSON in Extra.
func (c *Client) FetchAssets(ctx context.Context, usdRUB float64) ([]models.Asset, error) {
	accounts, err := c.getAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("get accounts: %w", err)
	}

	var assets []models.Asset
	for _, acc := range accounts {
		if acc.Status != "ACCOUNT_STATUS_OPEN" {
			continue
		}
		if acc.AccessLevel == "ACCOUNT_ACCESS_LEVEL_NO_ACCESS" {
			continue
		}

		portfolio, err := c.getPortfolio(ctx, acc.ID)
		if err != nil {
			continue
		}

		totalRUB := portfolio.TotalAmountPortfolio.ToRUB(usdRUB)

		var positions []models.InvestPosition
		for _, p := range portfolio.Positions {
			qty := p.Quantity.ToFloat()
			if qty == 0 {
				continue
			}
			priceRUB := p.CurrentPrice.ToRUB(usdRUB)
			valueRUB := qty * priceRUB

			ticker := p.Ticker
			if ticker == "" {
				ticker = p.InstrumentType
			}

			positions = append(positions, models.InvestPosition{
				Ticker:         ticker,
				InstrumentType: p.InstrumentType,
				Quantity:       qty,
				PriceRUB:       priceRUB,
				ValueRUB:       valueRUB,
			})
		}

		sort.Slice(positions, func(i, j int) bool {
			return positions[i].ValueRUB > positions[j].ValueRUB
		})

		posJSON, _ := json.Marshal(positions)

		name := acc.Name
		if name == "" {
			name = "Брокерский счёт"
		}

		assets = append(assets, models.Asset{
			Source:    "tinvest",
			Name:      name,
			Type:      models.AssetTypeInvest,
			AmountRaw: totalRUB,
			Currency:  "RUB",
			AmountRUB: totalRUB,
			Extra:     string(posJSON),
		})
	}
	return assets, nil
}

// --- Trading methods (require full-access token) ---

type findInstrumentResponse struct {
	Instruments []struct {
		FIGI   string `json:"figi"`
		Ticker string `json:"ticker"`
		Name   string `json:"name"`
		UID    string `json:"uid"`
	} `json:"instruments"`
}

// FindFIGI returns the FIGI identifier for a MOEX ticker symbol.
func (c *Client) FindFIGI(ctx context.Context, ticker string) (string, error) {
	var resp findInstrumentResponse
	if err := c.post(ctx,
		"/tinkoff.public.invest.api.contract.v1.InstrumentsService/FindInstrument",
		map[string]any{"query": ticker, "apiTradeAvailableFlag": true},
		&resp,
	); err != nil {
		return "", err
	}
	for _, inst := range resp.Instruments {
		if strings.EqualFold(inst.Ticker, ticker) {
			return inst.FIGI, nil
		}
	}
	return "", fmt.Errorf("инструмент %s не найден", ticker)
}

type postOrderResponse struct {
	OrderID string `json:"orderId"`
}

// GetLivePortfolio fetches the current portfolio state directly from the API.
func (c *Client) GetLivePortfolio(ctx context.Context, accountID string, usdRUB float64) (*PortfolioSummary, error) {
	resp, err := c.getPortfolio(ctx, accountID)
	if err != nil {
		return nil, err
	}

	var positions []LivePosition
	for _, p := range resp.Positions {
		qty := p.Quantity.ToFloat()
		if qty == 0 {
			continue
		}
		priceRUB := p.CurrentPrice.ToRUB(usdRUB)
		ticker := p.Ticker
		if ticker == "" {
			ticker = p.FIGI
		}
		positions = append(positions, LivePosition{
			FIGI:             p.FIGI,
			Ticker:           ticker,
			InstrumentType:   p.InstrumentType,
			Quantity:         qty,
			CurrentPriceRUB:  priceRUB,
			ValueRUB:         qty * priceRUB,
			ExpectedYieldRUB: p.ExpectedYield.ToFloat(),
		})
	}
	return &PortfolioSummary{
		TotalRUB:  resp.TotalAmountPortfolio.ToRUB(usdRUB),
		Positions: positions,
	}, nil
}

// GetMarginAttributes returns margin trading attributes for the account.
func (c *Client) GetMarginAttributes(ctx context.Context, accountID string) (*MarginAttributes, error) {
	var resp struct {
		LiquidPortfolio       moneyValue `json:"liquidPortfolio"`
		StartingMargin        moneyValue `json:"startingMargin"`
		FundsSufficiencyLevel moneyValue `json:"fundsSufficiencyLevel"`
	}
	if err := c.post(ctx,
		"/tinkoff.public.invest.api.contract.v1.UsersService/GetMarginAttributes",
		map[string]any{"accountId": accountID},
		&resp,
	); err != nil {
		return nil, err
	}
	return &MarginAttributes{
		LiquidPortfolio:       resp.LiquidPortfolio.ToFloat(),
		StartingMargin:        resp.StartingMargin.ToFloat(),
		FundsSufficiencyLevel: resp.FundsSufficiencyLevel.ToFloat(),
	}, nil
}

// PostStopOrder places a stop-loss order. direction: "STOP_ORDER_DIRECTION_SELL" or "STOP_ORDER_DIRECTION_BUY".
func (c *Client) PostStopOrder(ctx context.Context, accountID, figi string, quantity int, stopPriceRUB float64, direction string) (string, error) {
	units := int64(stopPriceRUB)
	nano := int32((stopPriceRUB - float64(units)) * 1e9)
	var resp struct {
		StopOrderID string `json:"stopOrderId"`
	}
	orderID := fmt.Sprintf("sf-sl-%d", time.Now().UnixNano())
	if err := c.post(ctx,
		"/tinkoff.public.invest.api.contract.v1.StopOrdersService/PostStopOrder",
		map[string]any{
			"figi":           figi,
			"quantity":       strconv.Itoa(quantity),
			"stopPrice":      map[string]any{"units": strconv.FormatInt(units, 10), "nano": nano, "currency": "rub"},
			"direction":      direction,
			"accountId":      accountID,
			"expirationType": "STOP_ORDER_EXPIRATION_TYPE_GOOD_TILL_CANCEL",
			"stopOrderType":  "STOP_ORDER_TYPE_STOP_LOSS",
			"orderId":        orderID,
		},
		&resp,
	); err != nil {
		return "", err
	}
	return resp.StopOrderID, nil
}

// PlaceMarketOrder places a market order. direction: "ORDER_DIRECTION_BUY" or "ORDER_DIRECTION_SELL".
// quantity is in lots.
func (c *Client) PlaceMarketOrder(ctx context.Context, accountID, figi string, quantity int, direction string) (string, error) {
	var resp postOrderResponse
	orderID := fmt.Sprintf("sf-%d", time.Now().UnixNano())
	if err := c.post(ctx,
		"/tinkoff.public.invest.api.contract.v1.OrdersService/PostOrder",
		map[string]any{
			"accountId":    accountID,
			"instrumentId": figi,
			"quantity":     quantity,
			"direction":    direction,
			"orderType":    "ORDER_TYPE_MARKET",
			"orderId":      orderID,
		},
		&resp,
	); err != nil {
		return "", err
	}
	if resp.OrderID == "" {
		return "", fmt.Errorf("order not confirmed by API")
	}
	return resp.OrderID, nil
}
