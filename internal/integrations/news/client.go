// Package news fetches financial news headlines from free RSS feeds.
package news

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Headline is a single news item.
type Headline struct {
	Title   string
	PubDate string
	Source  string
}

type rssItem struct {
	Title   string `xml:"title"`
	PubDate string `xml:"pubDate"`
}

type rssFeed struct {
	Channel struct {
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

// RussianFeeds are free RSS sources for MOEX-relevant news.
var RussianFeeds = []string{
	"https://www.rbc.ru/rss/money",
	"https://www.interfax.ru/rss.asp",
}

// CryptoFeeds are free RSS sources for crypto news.
var CryptoFeeds = []string{
	"https://cointelegraph.com/rss",
	"https://coindesk.com/arc/outboundfeeds/rss/",
}

// FetchHeadlines fetches and aggregates headlines from the given RSS feeds.
// Returns up to limit items total; ignores individual feed errors.
func FetchHeadlines(ctx context.Context, feeds []string, limit int) []Headline {
	hc := &http.Client{Timeout: 10 * time.Second}
	var out []Headline

	for _, feedURL := range feeds {
		if len(out) >= limit {
			break
		}
		items, err := fetchFeed(ctx, hc, feedURL)
		if err != nil {
			continue
		}
		source := feedSource(feedURL)
		for _, it := range items {
			if len(out) >= limit {
				break
			}
			title := strings.TrimSpace(it.Title)
			if title == "" {
				continue
			}
			out = append(out, Headline{
				Title:   title,
				PubDate: it.PubDate,
				Source:  source,
			})
		}
	}
	return out
}

func fetchFeed(ctx context.Context, hc *http.Client, feedURL string) ([]rssItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "SimpleFinance/1.0")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, err
	}
	var feed rssFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, err
	}
	return feed.Channel.Items, nil
}

func feedSource(url string) string {
	switch {
	case strings.Contains(url, "rbc.ru"):
		return "RBC"
	case strings.Contains(url, "interfax.ru"):
		return "Interfax"
	case strings.Contains(url, "cointelegraph.com"):
		return "CoinTelegraph"
	case strings.Contains(url, "coindesk.com"):
		return "CoinDesk"
	default:
		return "News"
	}
}
