package tbank

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/HeMMars4/simple-finance/internal/models"
)

// normCurrency maps ISO 4217 numeric codes to alphabetic codes.
// T-Bank API sometimes returns numeric strCode like "643" instead of "RUB".
func normCurrency(c *currency) string {
	if c == nil {
		return "RUB"
	}
	switch c.Code {
	case 643:
		return "RUB"
	case 840:
		return "USD"
	case 978:
		return "EUR"
	case 156:
		return "CNY"
	case 826:
		return "GBP"
	}
	// StrCode is "643" when the API returns the numeric code — skip those
	if c.StrCode != "" && len(c.StrCode) == 3 && !isAllDigits(c.StrCode) {
		return strings.ToUpper(c.StrCode)
	}
	if c.Name != "" && len(c.Name) == 3 {
		return strings.ToUpper(c.Name)
	}
	return "RUB"
}

func isAllDigits(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

const accountsURL = "https://www.tbank.ru/api/common/v1/accounts_light_ib?appName=supreme&appVersion=0.0.1&platform=web&origin=web%%2Cib5%%2Cplatform&sessionid=%s"

type Client struct {
	sessionID  string
	httpClient *http.Client
}

func New(sessionID string) *Client {
	return &Client{
		sessionID: sessionID,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// apiResponse mirrors the T-Bank payload structure
type apiResponse struct {
	ResultCode string    `json:"resultCode"`
	Payload    []account `json:"payload"`
}

type brand struct {
	LogoFile string `json:"logoFile"`
}

type account struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	AccountType string      `json:"accountType"`
	Status      string      `json:"status"`
	MoneyAmount *moneyField `json:"moneyAmount"`
	CreditLimit *moneyField `json:"creditLimit"`
	DebtAmount  *moneyField `json:"debtAmount"`
	Currency    *currency   `json:"currency"`
	Brand       *brand      `json:"brand"`
}

type moneyField struct {
	Value    float64  `json:"value"`
	Currency currency `json:"currency"`
}

type currency struct {
	Code    int    `json:"code"`
	Name    string `json:"name"`
	StrCode string `json:"strCode"`
}

// FetchAssets calls T-Bank API and maps response to domain models
func (c *Client) FetchAssets(ctx context.Context, usdRUBRate float64) ([]models.Asset, error) {
	url := fmt.Sprintf(accountsURL, c.sessionID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// Required headers so T-Bank doesn't reject the request
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://www.tbank.ru/")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tbank request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var apiResp apiResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("tbank parse: %w", err)
	}
	if apiResp.ResultCode != "OK" {
		return nil, fmt.Errorf("tbank API returned: %s", apiResp.ResultCode)
	}

	return mapAccounts(apiResp.Payload, usdRUBRate), nil
}

func mapAccounts(accounts []account, usdRUBRate float64) []models.Asset {
	var result []models.Asset

	for _, a := range accounts {
		if a.Status != "NORM" {
			continue
		}
		if a.MoneyAmount == nil {
			continue
		}

		// Determine asset type
		assetType := models.AssetTypeBankCurrent
		switch a.AccountType {
		case "Saving":
			assetType = models.AssetTypeBankSaving
		case "Credit":
			assetType = models.AssetTypeCreditCard
		case "Telecom", "BNPL":
			continue // skip non-financial accounts
		case "OpenBankingCurrentAccount":
			assetType = models.AssetTypeBankCurrent
		}

		// Derive source: OpenBanking accounts have brand info for the actual bank
		source := "tbank"
		if a.AccountType == "OpenBankingCurrentAccount" && a.Brand != nil {
			logo := strings.ToLower(a.Brand.LogoFile)
			switch {
			case strings.Contains(logo, "sber"):
				source = "sber"
			}
		}

		currCode := normCurrency(a.Currency)

		// For credit cards use the debt (owed amount) as the primary value.
		// Regular accounts use moneyAmount (current balance).
		var amountRaw float64
		if a.AccountType == "Credit" {
			if a.DebtAmount != nil {
				amountRaw = a.DebtAmount.Value
			}
		} else {
			amountRaw = a.MoneyAmount.Value
		}

		var amountRUB float64
		switch currCode {
		case "RUB", "643":
			amountRUB = amountRaw
		case "USD", "840":
			amountRUB = amountRaw * usdRUBRate
		default:
			amountRUB = amountRaw
		}

		extra := ""
		if a.AccountType == "Credit" {
			var limitVal, availVal float64
			if a.CreditLimit != nil {
				limitVal = a.CreditLimit.Value
			}
			if a.MoneyAmount != nil {
				availVal = a.MoneyAmount.Value
			}
			debtVal := amountRaw
			b, _ := json.Marshal(map[string]interface{}{
				"credit_limit": limitVal,
				"available":    availVal,
				"debt":         debtVal,
			})
			extra = string(b)
		}

		result = append(result, models.Asset{
			Source:    source,
			Name:      a.Name,
			Type:      assetType,
			AmountRaw: amountRaw,
			Currency:  currCode,
			AmountRUB: amountRUB,
			Extra:     extra,
		})
	}

	return result
}
