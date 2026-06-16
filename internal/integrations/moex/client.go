package moex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const issBase = "https://iss.moex.com/iss"

type Client struct {
	http *http.Client
}

func New() *Client {
	return &Client{http: &http.Client{Timeout: 8 * time.Second}}
}

type Quote struct {
	Ticker    string
	Last      float64
	PrevClose float64
	ChangePct float64
	Volume    float64
}

type issResp struct {
	MarketData struct {
		Columns []string `json:"columns"`
		Data    [][]any  `json:"data"`
	} `json:"marketdata"`
	Securities struct {
		Columns []string `json:"columns"`
		Data    [][]any  `json:"data"`
	} `json:"securities"`
}

// NormalizeTicker strips board suffix appended by T-Invest (e.g. "@", "@GS").
func NormalizeTicker(ticker string) string {
	if idx := strings.Index(ticker, "@"); idx >= 0 {
		ticker = ticker[:idx]
	}
	return strings.ToUpper(ticker)
}

// GetQuote fetches the latest quote for a MOEX ticker, trying common boards in order.
func (c *Client) GetQuote(ctx context.Context, ticker string) (*Quote, error) {
	ticker = NormalizeTicker(ticker)
	for _, board := range []string{"TQBR", "TQTF", "TQOB"} {
		q, err := c.fetchBoard(ctx, ticker, board)
		if err == nil && q.Last > 0 {
			return q, nil
		}
	}
	return nil, fmt.Errorf("котировка %s не найдена", ticker)
}

func (c *Client) fetchBoard(ctx context.Context, ticker, board string) (*Quote, error) {
	url := fmt.Sprintf(
		"%s/engines/stock/markets/shares/boards/%s/securities/%s.json?iss.meta=off&iss.only=marketdata,securities",
		issBase, board, ticker,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var r issResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}

	q := &Quote{Ticker: ticker}
	if lastIdx := colIdx(r.MarketData.Columns, "LAST"); lastIdx >= 0 && len(r.MarketData.Data) > 0 {
		row := r.MarketData.Data[0]
		q.Last = asFloat(row, lastIdx)
		q.Volume = asFloat(row, colIdx(r.MarketData.Columns, "VOLTODAY"))
	}
	if prevIdx := colIdx(r.Securities.Columns, "PREVPRICE"); prevIdx >= 0 && len(r.Securities.Data) > 0 {
		q.PrevClose = asFloat(r.Securities.Data[0], prevIdx)
	}
	if q.Last > 0 && q.PrevClose > 0 {
		q.ChangePct = (q.Last - q.PrevClose) / q.PrevClose * 100
	}
	return q, nil
}

// GetQuotes fetches quotes for multiple tickers concurrently, keyed by normalized ticker.
func (c *Client) GetQuotes(ctx context.Context, tickers []string) map[string]*Quote {
	type result struct {
		ticker string
		quote  *Quote
	}

	seen := make(map[string]bool)
	unique := make([]string, 0, len(tickers))
	for _, t := range tickers {
		norm := NormalizeTicker(t)
		if !seen[norm] {
			seen[norm] = true
			unique = append(unique, norm)
		}
	}

	ch := make(chan result, len(unique))
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup

	for _, t := range unique {
		wg.Add(1)
		go func(ticker string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if q, err := c.GetQuote(ctx, ticker); err == nil {
				ch <- result{ticker: ticker, quote: q}
			}
		}(t)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	out := make(map[string]*Quote, len(unique))
	for r := range ch {
		out[r.ticker] = r.quote
	}
	return out
}

func colIdx(cols []string, name string) int {
	for i, c := range cols {
		if c == name {
			return i
		}
	}
	return -1
}

func asFloat(row []any, idx int) float64 {
	if idx < 0 || idx >= len(row) {
		return 0
	}
	switch v := row[idx].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return 0
}
