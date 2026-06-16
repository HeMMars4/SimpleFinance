package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/HeMMars4/simple-finance/internal/integrations/claude"
	"github.com/HeMMars4/simple-finance/internal/integrations/moex"
	"github.com/HeMMars4/simple-finance/internal/integrations/news"
	"github.com/HeMMars4/simple-finance/internal/integrations/tinvest"
	"github.com/HeMMars4/simple-finance/internal/models"
	"github.com/HeMMars4/simple-finance/internal/storage"
)

var moscowTZ *time.Location

func init() {
	var err error
	moscowTZ, err = time.LoadLocation("Europe/Moscow")
	if err != nil {
		moscowTZ = time.FixedZone("MSK", 3*60*60)
	}
}

// Runner manages per-user trading bot goroutines (investor, trader, bybit).
type Runner struct {
	db        *storage.DB
	mu        sync.RWMutex
	traders   map[int64]context.CancelFunc // short-term news trader (T-Invest)
	investors map[int64]context.CancelFunc // long-term investor (T-Invest)
	bybitBots map[int64]context.CancelFunc // Bybit crypto bot
}

func New(db *storage.DB) *Runner {
	return &Runner{
		db:        db,
		traders:   make(map[int64]context.CancelFunc),
		investors: make(map[int64]context.CancelFunc),
		bybitBots: make(map[int64]context.CancelFunc),
	}
}

// --- Trader bot (T-Invest, short-term, news-aware) ---

func (r *Runner) IsRunning(userID int64) bool { return r.IsTraderRunning(userID) }

func (r *Runner) IsTraderRunning(userID int64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.traders[userID]
	return ok
}

func (r *Runner) Start(userID int64) { r.StartTrader(userID) }

func (r *Runner) StartTrader(userID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.traders[userID]; ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.traders[userID] = cancel
	go r.runTrader(ctx, userID)
}

func (r *Runner) Stop(userID int64) { r.StopTrader(userID) }

func (r *Runner) StopTrader(userID int64) {
	r.mu.Lock()
	if cancel, ok := r.traders[userID]; ok {
		cancel()
		delete(r.traders, userID)
	}
	r.mu.Unlock()
	go func() {
		s, err := r.db.GetUserSettings(context.Background(), userID)
		if err == nil {
			s.BotEnabled = false
			r.db.SaveUserSettings(context.Background(), s) //nolint:errcheck
		}
		r.addLog(context.Background(), userID, "info", "Трейдер-бот остановлен")
	}()
}

// --- Investor bot (T-Invest, long-term rebalancing) ---

func (r *Runner) IsInvestorRunning(userID int64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.investors[userID]
	return ok
}

func (r *Runner) StartInvestor(userID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.investors[userID]; ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.investors[userID] = cancel
	go r.runInvestor(ctx, userID)
}

func (r *Runner) StopInvestor(userID int64) {
	r.mu.Lock()
	if cancel, ok := r.investors[userID]; ok {
		cancel()
		delete(r.investors, userID)
	}
	r.mu.Unlock()
	go func() {
		s, err := r.db.GetUserSettings(context.Background(), userID)
		if err == nil {
			s.InvestorBotEnabled = false
			r.db.SaveUserSettings(context.Background(), s) //nolint:errcheck
		}
		r.addLog(context.Background(), userID, "info", "Инвестор-бот остановлен")
	}()
}

// --- Bybit bot ---

func (r *Runner) IsBybitRunning(userID int64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.bybitBots[userID]
	return ok
}

func (r *Runner) StartBybit(userID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.bybitBots[userID]; ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.bybitBots[userID] = cancel
	go r.runBybit(ctx, userID)
}

func (r *Runner) StopBybit(userID int64) {
	r.mu.Lock()
	if cancel, ok := r.bybitBots[userID]; ok {
		cancel()
		delete(r.bybitBots, userID)
	}
	r.mu.Unlock()
	go func() {
		s, err := r.db.GetUserSettings(context.Background(), userID)
		if err == nil {
			s.BybitBotEnabled = false
			r.db.SaveUserSettings(context.Background(), s) //nolint:errcheck
		}
		r.addLog(context.Background(), userID, "info", "Bybit-бот остановлен")
	}()
}

// --- Log helper ---

func (r *Runner) addLog(ctx context.Context, userID int64, level, msg string) {
	slog.Info("bot", "user", userID, "level", level, "msg", msg)
	r.db.AddBotLog(ctx, userID, level, msg) //nolint:errcheck
}

// =============================================================================
// Trader bot goroutine (short-term, news-aware, MOEX hours)
// =============================================================================

func (r *Runner) runTrader(ctx context.Context, userID int64) {
	r.addLog(ctx, userID, "info", "▶ Трейдер-бот запущен")
	defer func() {
		r.mu.Lock()
		delete(r.traders, userID)
		r.mu.Unlock()
	}()

	if s, err := r.db.GetUserSettings(ctx, userID); err == nil {
		s.BotEnabled = true
		r.db.SaveUserSettings(ctx, s) //nolint:errcheck
	}

	for {
		r.traderCycle(ctx, userID)

		s, err := r.db.GetUserSettings(ctx, userID)
		if err != nil {
			return
		}
		interval := time.Duration(s.BotIntervalMinutes) * time.Minute
		if interval < time.Minute {
			interval = 60 * time.Minute
		}
		// If keys are not set yet, retry sooner instead of waiting the full interval
		if s.ClaudeAPIKey == "" || s.TInvestTradeToken == "" {
			interval = 5 * time.Minute
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (r *Runner) traderCycle(ctx context.Context, userID int64) {
	s, err := r.db.GetUserSettings(ctx, userID)
	if err != nil {
		r.addLog(ctx, userID, "error", "Ошибка загрузки настроек: "+err.Error())
		return
	}

	if s.ClaudeAPIKey == "" || s.TInvestTradeToken == "" {
		r.addLog(ctx, userID, "warn", "Ключи не заданы — укажите Claude API Key и Торговый токен T-Инвестиции во вкладке Развитие → API ключи. Повтор через 5 мин.")
		return
	}

	now := time.Now().In(moscowTZ)
	if !isMarketOpen(now) {
		r.addLog(ctx, userID, "info", fmt.Sprintf("[Трейдер] Биржа закрыта (%s MSK), цикл пропущен", now.Format("15:04")))
		return
	}

	r.addLog(ctx, userID, "info", fmt.Sprintf("[Трейдер] Цикл [%s MSK]", now.Format("02.01 15:04")))

	usdRUB, _ := r.db.GetRate(ctx, "USD", "RUB")
	if usdRUB < 10 {
		usdRUB = 90.0
	}

	tc := tinvest.New(s.TInvestTradeToken)
	cc := claude.New(s.ClaudeAPIKey)

	accountID, err := tc.GetFirstAccountID(ctx)
	if err != nil {
		r.addLog(ctx, userID, "error", "Счёт T-Инвестиции не найден: "+err.Error())
		return
	}

	portfolio, err := tc.GetLivePortfolio(ctx, accountID, usdRUB)
	if err != nil {
		r.addLog(ctx, userID, "error", "Ошибка получения портфеля: "+err.Error())
		return
	}

	if len(portfolio.Positions) == 0 {
		r.addLog(ctx, userID, "warn", "Портфель пуст — нечего анализировать")
		return
	}

	r.addLog(ctx, userID, "info", fmt.Sprintf(
		"[Трейдер] Портфель: %.0f ₽, позиций: %d", portfolio.TotalRUB, len(portfolio.Positions),
	))

	// Fetch MOEX quotes
	tickers := make([]string, 0, len(portfolio.Positions))
	for _, pos := range portfolio.Positions {
		tickers = append(tickers, pos.Ticker)
	}
	mc := moex.New()
	quotesCtx, quotesCancel := context.WithTimeout(ctx, 20*time.Second)
	quotes := mc.GetQuotes(quotesCtx, tickers)
	quotesCancel()

	// Fetch news headlines
	newsCtx, newsCancel := context.WithTimeout(ctx, 12*time.Second)
	headlines := news.FetchHeadlines(newsCtx, news.RussianFeeds, 15)
	newsCancel()
	if len(headlines) > 0 {
		r.addLog(ctx, userID, "info", fmt.Sprintf("[Трейдер] Новости: %d заголовков", len(headlines)))
	}

	analystPortfolio := portfolio

	// Margin info
	var marginInfo string
	if s.BotUseMargin {
		margin, err := tc.GetMarginAttributes(ctx, accountID)
		if err != nil {
			r.addLog(ctx, userID, "warn", "Маржинальные данные недоступны: "+err.Error())
		} else {
			marginInfo = fmt.Sprintf(
				"Ликвидный портфель %.0f ₽, маржа %.0f ₽, уровень %.2f",
				margin.LiquidPortfolio, margin.StartingMargin, margin.FundsSufficiencyLevel,
			)
			r.addLog(ctx, userID, "info", "Маржа: "+marginInfo)
		}
	}

	// Analyst with news context
	analystPrompt := buildTraderAnalystPrompt(s, analystPortfolio, quotes, headlines, marginInfo, now)
	r.addLog(ctx, userID, "info", "Запрос к Claude (аналитик)...")
	analystResp, err := cc.Ask(ctx, analystPrompt)
	if err != nil {
		r.addLog(ctx, userID, "error", "Claude API (аналитик): "+err.Error())
		return
	}

	analystRecs, err := parseRecs(analystResp)
	if err != nil || len(analystRecs) == 0 {
		r.addLog(ctx, userID, "info", "Аналитик: сделок не рекомендует")
		return
	}
	r.addLog(ctx, userID, "info", fmt.Sprintf("Аналитик предлагает %d сделок", len(analystRecs)))

	// Critic
	criticPrompt := buildCriticPrompt(s, analystPortfolio, analystRecs, now)
	r.addLog(ctx, userID, "info", "Запрос к Claude (критик)...")
	criticResp, err := cc.Ask(ctx, criticPrompt)
	var recs []models.TradeRecommendation
	if err != nil {
		recs = analystRecs
	} else {
		criticRecs, parseErr := parseRecs(criticResp)
		switch {
		case parseErr != nil:
			recs = analystRecs
		case len(criticRecs) == 0:
			r.addLog(ctx, userID, "info", "Критик отклонил все рекомендации")
			return
		default:
			r.addLog(ctx, userID, "info", fmt.Sprintf("Критик одобрил %d из %d", len(criticRecs), len(analystRecs)))
			recs = criticRecs
		}
	}

	r.executeOrders(ctx, userID, s, tc, accountID, portfolio, quotes, recs)

	time.Sleep(3 * time.Second)
	if newP, err := tc.GetLivePortfolio(ctx, accountID, usdRUB); err == nil {
		diff := newP.TotalRUB - portfolio.TotalRUB
		sign := "+"
		if diff < 0 {
			sign = ""
		}
		r.addLog(ctx, userID, "info", fmt.Sprintf("[Трейдер] Цикл завершён | %.0f ₽ (%s%.0f ₽)", newP.TotalRUB, sign, diff))
	}
}

// =============================================================================
// Investor bot goroutine (long-term rebalancing, no market-hours skip)
// =============================================================================

func (r *Runner) runInvestor(ctx context.Context, userID int64) {
	r.addLog(ctx, userID, "info", "▶ Инвестор-бот запущен")
	defer func() {
		r.mu.Lock()
		delete(r.investors, userID)
		r.mu.Unlock()
	}()

	if s, err := r.db.GetUserSettings(ctx, userID); err == nil {
		s.InvestorBotEnabled = true
		r.db.SaveUserSettings(ctx, s) //nolint:errcheck
	}

	for {
		r.investorCycle(ctx, userID)

		s, err := r.db.GetUserSettings(ctx, userID)
		if err != nil {
			return
		}
		hours := s.InvestorIntervalHours
		if hours < 1 {
			hours = 24
		}
		interval := time.Duration(hours) * time.Hour
		// If keys are not set yet, retry sooner instead of waiting the full interval
		if s.ClaudeAPIKey == "" || s.TInvestTradeToken == "" {
			interval = 5 * time.Minute
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (r *Runner) investorCycle(ctx context.Context, userID int64) {
	s, err := r.db.GetUserSettings(ctx, userID)
	if err != nil {
		r.addLog(ctx, userID, "error", "Ошибка загрузки настроек: "+err.Error())
		return
	}

	if s.ClaudeAPIKey == "" || s.TInvestTradeToken == "" {
		r.addLog(ctx, userID, "warn", "[Инвестор] Ключи не заданы — укажите Claude API Key и Торговый токен T-Инвестиции во вкладке Развитие → API ключи. Повтор через 5 мин.")
		return
	}

	now := time.Now().In(moscowTZ)
	r.addLog(ctx, userID, "info", fmt.Sprintf("[Инвестор] Анализ портфеля [%s MSK]", now.Format("02.01.2006 15:04")))

	usdRUB, _ := r.db.GetRate(ctx, "USD", "RUB")
	if usdRUB < 10 {
		usdRUB = 90.0
	}

	tc := tinvest.New(s.TInvestTradeToken)
	cc := claude.New(s.ClaudeAPIKey)

	accountID, err := tc.GetFirstAccountID(ctx)
	if err != nil {
		r.addLog(ctx, userID, "error", "Счёт T-Инвестиции не найден: "+err.Error())
		return
	}

	portfolio, err := tc.GetLivePortfolio(ctx, accountID, usdRUB)
	if err != nil {
		r.addLog(ctx, userID, "error", "Ошибка получения портфеля: "+err.Error())
		return
	}

	r.addLog(ctx, userID, "info", fmt.Sprintf(
		"[Инвестор] Портфель: %.0f ₽, позиций: %d", portfolio.TotalRUB, len(portfolio.Positions),
	))

	// Fetch MOEX quotes for all positions
	tickers := make([]string, 0, len(portfolio.Positions))
	for _, pos := range portfolio.Positions {
		tickers = append(tickers, pos.Ticker)
	}
	mc := moex.New()
	quotesCtx, quotesCancel := context.WithTimeout(ctx, 20*time.Second)
	quotes := mc.GetQuotes(quotesCtx, tickers)
	quotesCancel()

	// Long-term investor prompt
	prompt := buildInvestorPrompt(s, portfolio, quotes, now)
	r.addLog(ctx, userID, "info", "[Инвестор] Запрос к Claude...")
	resp, err := cc.Ask(ctx, prompt)
	if err != nil {
		r.addLog(ctx, userID, "error", "Claude API: "+err.Error())
		return
	}

	recs, err := parseRecs(resp)
	if err != nil || len(recs) == 0 {
		// Log Claude's analysis text even if no trades
		if len(resp) > 0 {
			// Trim to avoid flooding logs
			preview := resp
			if len(preview) > 300 {
				preview = preview[:300] + "..."
			}
			r.addLog(ctx, userID, "info", "[Инвестор] Анализ: "+preview)
		}
		r.addLog(ctx, userID, "info", "[Инвестор] Ребалансировка не требуется")
		return
	}

	r.addLog(ctx, userID, "info", fmt.Sprintf("[Инвестор] Рекомендует %d сделок для ребалансировки", len(recs)))

	if !isMarketOpen(now) {
		r.addLog(ctx, userID, "warn", "[Инвестор] Биржа закрыта — сделки отложены до открытия сессии")
		return
	}

	r.executeOrders(ctx, userID, s, tc, accountID, portfolio, quotes, recs)
	r.addLog(ctx, userID, "info", "[Инвестор] Ребалансировка завершена")
}

// =============================================================================
// Bybit bot goroutine (crypto, 24/7)
// =============================================================================

func (r *Runner) runBybit(ctx context.Context, userID int64) {
	r.addLog(ctx, userID, "info", "▶ Bybit-бот запущен")
	defer func() {
		r.mu.Lock()
		delete(r.bybitBots, userID)
		r.mu.Unlock()
	}()

	if s, err := r.db.GetUserSettings(ctx, userID); err == nil {
		s.BybitBotEnabled = true
		r.db.SaveUserSettings(ctx, s) //nolint:errcheck
	}

	for {
		r.bybitCycle(ctx, userID)

		s, err := r.db.GetUserSettings(ctx, userID)
		if err != nil {
			return
		}
		interval := time.Duration(s.BybitBotIntervalMin) * time.Minute
		if interval < time.Minute {
			interval = 60 * time.Minute
		}
		// If Claude key is not set yet, retry sooner instead of waiting the full interval
		if s.ClaudeAPIKey == "" {
			interval = 5 * time.Minute
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (r *Runner) bybitCycle(ctx context.Context, userID int64) {
	s, err := r.db.GetUserSettings(ctx, userID)
	if err != nil {
		r.addLog(ctx, userID, "error", "Ошибка загрузки настроек: "+err.Error())
		return
	}

	if s.ClaudeAPIKey == "" {
		r.addLog(ctx, userID, "warn", "[Bybit] Claude API Key не задан — укажите во вкладке Развитие → API ключи. Повтор через 5 мин.")
		return
	}

	now := time.Now().In(moscowTZ)
	r.addLog(ctx, userID, "info", fmt.Sprintf("[Bybit] Цикл [%s MSK]", now.Format("02.01 15:04")))

	// Fetch Bybit assets from DB (stored by aggregator)
	assets, _ := r.db.GetAllAssets(ctx, userID)
	var bybitAssets []models.Asset
	var bybitTotalUSD float64
	usdRUB, _ := r.db.GetRate(ctx, "USD", "RUB")
	if usdRUB < 10 {
		usdRUB = 90.0
	}
	for _, a := range assets {
		if strings.Contains(a.Source, "bybit") || strings.Contains(a.Source, "exchange") {
			bybitAssets = append(bybitAssets, a)
			bybitTotalUSD += a.AmountRUB / usdRUB
		}
	}

	if len(bybitAssets) == 0 {
		r.addLog(ctx, userID, "warn", "[Bybit] Нет данных о Bybit-портфеле — сначала обновите дашборд")
		return
	}

	r.addLog(ctx, userID, "info", fmt.Sprintf(
		"[Bybit] Портфель: ~%.0f USDT (%d активов)", bybitTotalUSD, len(bybitAssets),
	))

	// Fetch crypto news
	newsCtx, newsCancel := context.WithTimeout(ctx, 12*time.Second)
	headlines := news.FetchHeadlines(newsCtx, news.CryptoFeeds, 12)
	newsCancel()
	if len(headlines) > 0 {
		r.addLog(ctx, userID, "info", fmt.Sprintf("[Bybit] Крипто-новости: %d заголовков", len(headlines)))
	}

	cc := claude.New(s.ClaudeAPIKey)
	prompt := buildBybitPrompt(s, bybitAssets, bybitTotalUSD, headlines, now)
	r.addLog(ctx, userID, "info", "[Bybit] Запрос к Claude...")
	resp, err := cc.Ask(ctx, prompt)
	if err != nil {
		r.addLog(ctx, userID, "error", "Claude API: "+err.Error())
		return
	}

	recs, err := parseRecs(resp)
	if err != nil || len(recs) == 0 {
		if len(resp) > 0 {
			preview := resp
			if len(preview) > 300 {
				preview = preview[:300] + "..."
			}
			r.addLog(ctx, userID, "info", "[Bybit] Анализ: "+preview)
		}
		r.addLog(ctx, userID, "info", "[Bybit] Сделок не рекомендуется")
		return
	}

	r.addLog(ctx, userID, "info", fmt.Sprintf("[Bybit] Рекомендовано %d сделок", len(recs)))

	for _, rec := range recs {
		r.addLog(ctx, userID, "trade", fmt.Sprintf(
			"[Bybit] %s %s ×%d — %s", rec.Action, rec.Ticker, rec.Quantity, rec.Reasoning,
		))
	}
	r.addLog(ctx, userID, "warn", "[Bybit] Реальное исполнение ордеров Bybit — в разработке. Рекомендации выведены в лог.")
}

// =============================================================================
// Shared order execution (trader + investor bots, T-Invest)
// =============================================================================

func (r *Runner) executeOrders(
	ctx context.Context,
	userID int64,
	s *models.UserSettings,
	tc *tinvest.Client,
	accountID string,
	portfolio *tinvest.PortfolioSummary,
	quotes map[string]*moex.Quote,
	recs []models.TradeRecommendation,
) {
	maxLossRUB := portfolio.TotalRUB * s.MaxLossPct / 100.0
	var usedRUB float64

	for _, rec := range recs {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if rec.Action == "SELL" && usedRUB >= maxLossRUB {
			r.addLog(ctx, userID, "warn", fmt.Sprintf(
				"Лимит %.0f ₽ достигнут — %s %s пропущена", maxLossRUB, rec.Action, rec.Ticker,
			))
			continue
		}

		r.addLog(ctx, userID, "info", fmt.Sprintf(
			"Исполнение: %s %s ×%d — %s", rec.Action, rec.Ticker, rec.Quantity, rec.Reasoning,
		))

		figi, err := tc.FindFIGI(ctx, rec.Ticker)
		if err != nil {
			r.addLog(ctx, userID, "error", fmt.Sprintf("%s: тикер не найден (%s)", rec.Ticker, err.Error()))
			continue
		}

		direction := "ORDER_DIRECTION_BUY"
		if rec.Action == "SELL" {
			direction = "ORDER_DIRECTION_SELL"
		}

		orderID, err := tc.PlaceMarketOrder(ctx, accountID, figi, rec.Quantity, direction)
		if err != nil {
			msg := err.Error()
			if strings.Contains(msg, "90001") {
				msg += " — требуется подтверждение рисков в приложении T-Invest"
			}
			r.addLog(ctx, userID, "error", fmt.Sprintf("%s %s: ошибка ордера (%s)", rec.Action, rec.Ticker, msg))
			continue
		}

		r.addLog(ctx, userID, "trade", fmt.Sprintf(
			"✓ %s %s ×%d | orderID: %s", rec.Action, rec.Ticker, rec.Quantity, orderID,
		))

		if rec.Action == "BUY" {
			for _, pos := range portfolio.Positions {
				if strings.EqualFold(pos.Ticker, rec.Ticker) && pos.CurrentPriceRUB > 0 {
					stopPrice := pos.CurrentPriceRUB * (1.0 - s.MaxLossPct/100.0)
					slID, err := tc.PostStopOrder(ctx, accountID, figi, rec.Quantity, stopPrice, "STOP_ORDER_DIRECTION_SELL")
					if err != nil {
						r.addLog(ctx, userID, "warn", fmt.Sprintf("Стоп-лосс %s не выставлен: %s", rec.Ticker, err.Error()))
					} else {
						r.addLog(ctx, userID, "info", fmt.Sprintf("Стоп-лосс %s @ %.2f ₽ | %s", rec.Ticker, stopPrice, slID))
					}
					usedRUB += pos.CurrentPriceRUB * float64(rec.Quantity)
					break
				}
			}
		} else {
			for _, pos := range portfolio.Positions {
				if strings.EqualFold(pos.Ticker, rec.Ticker) {
					usedRUB += pos.CurrentPriceRUB * float64(rec.Quantity)
					break
				}
			}
		}

		time.Sleep(500 * time.Millisecond)
	}
}

// =============================================================================
// Prompt builders
// =============================================================================

func buildTraderAnalystPrompt(s *models.UserSettings, p *tinvest.PortfolioSummary, quotes map[string]*moex.Quote, headlines []news.Headline, marginInfo string, now time.Time) string {
	riskLabel := riskText(s.RiskLevel)
	var sb strings.Builder

	sb.WriteString("Ты — краткосрочный торговый аналитик MOEX. Автономный режим.\n\n")
	sb.WriteString(fmt.Sprintf("ВРЕМЯ: %s MSK\n", now.Format("02.01.2006 15:04")))
	sb.WriteString(fmt.Sprintf("ПРОФИЛЬ РИСКА: %s\n", riskLabel))
	sb.WriteString(fmt.Sprintf("МАКС. ПОТЕРЯ ЗА ЦИКЛ: %.1f%% (≈%.0f ₽)\n", s.MaxLossPct, p.TotalRUB*s.MaxLossPct/100))
	if marginInfo != "" {
		sb.WriteString("МАРЖА: " + marginInfo + "\n")
	}

	writePortfolio(&sb, p)
	writeQuotes(&sb, p, quotes)

	if len(headlines) > 0 {
		sb.WriteString("\nАКТУАЛЬНЫЕ НОВОСТИ (влияние на рынок):\n")
		for _, h := range headlines {
			sb.WriteString(fmt.Sprintf("  [%s] %s\n", h.Source, h.Title))
		}
	}

	sb.WriteString(`
ПРАВИЛА ТРЕЙДЕРА:
1. Не более 3 сделок за цикл
2. Учитывай новости при принятии решений
3. Количество в лотах MOEX
4. Если ничего не нужно — верни []
5. Стоп-лоссы выставляются автоматически

Ответь ТОЛЬКО JSON-массивом:
[{"action":"BUY","ticker":"SBER","quantity":1,"reasoning":"..."}]`)
	return sb.String()
}

func buildInvestorPrompt(s *models.UserSettings, p *tinvest.PortfolioSummary, quotes map[string]*moex.Quote, now time.Time) string {
	riskLabel := riskText(s.RiskLevel)
	var sb strings.Builder

	sb.WriteString("Ты — долгосрочный инвестиционный аналитик (стратегия: купи и держи + ребалансировка).\n\n")
	sb.WriteString(fmt.Sprintf("ДАТА АНАЛИЗА: %s\n", now.Format("02.01.2006")))
	sb.WriteString(fmt.Sprintf("ПРОФИЛЬ РИСКА: %s\n", riskLabel))
	sb.WriteString(fmt.Sprintf("ГОРИЗОНТ: долгосрочный (месяцы–годы)\n"))
	sb.WriteString(fmt.Sprintf("МАКС. ДОЛЯ ОДНОГО АКТИВА: 30%%\n"))

	writePortfolio(&sb, p)
	writeQuotes(&sb, p, quotes)

	sb.WriteString(`
ЗАДАЧА — РЕБАЛАНСИРОВКА ПОРТФЕЛЯ:
- Оцени текущую аллокацию (ETF, голубые фишки, облигации, другое)
- Рекомендуй докупки если доля актива сильно ниже целевой
- Рекомендуй продажи если позиция сильно выросла и занимает >35% портфеля
- Не рекомендуй частые сделки — только при значимом отклонении
- Не более 2 сделок

Если ребалансировка не нужна — верни [] и объясни кратко (одним предложением вне JSON).

Ответь ТОЛЬКО JSON-массивом (или [] если сделок нет):
[{"action":"BUY","ticker":"TMOS","quantity":5,"reasoning":"Доля ETF упала до 15%, целевая 30%"}]`)
	return sb.String()
}

func buildBybitPrompt(s *models.UserSettings, assets []models.Asset, totalUSD float64, headlines []news.Headline, now time.Time) string {
	riskLabel := riskText(s.RiskLevel)
	var sb strings.Builder

	sb.WriteString("Ты — крипто-инвестиционный аналитик. Анализируешь Bybit-портфель.\n\n")
	sb.WriteString(fmt.Sprintf("ВРЕМЯ: %s MSK\n", now.Format("02.01.2006 15:04")))
	sb.WriteString(fmt.Sprintf("ПРОФИЛЬ РИСКА: %s\n", riskLabel))
	sb.WriteString(fmt.Sprintf("ПОРТФЕЛЬ (итого ≈%.0f USDT):\n", totalUSD))

	for _, a := range assets {
		sb.WriteString(fmt.Sprintf("  %-8s | %.4f | ≈%.2f USDT\n", a.Currency, a.AmountRaw, a.AmountRUB/90.0))
	}

	if len(headlines) > 0 {
		sb.WriteString("\nКРИПТО-НОВОСТИ:\n")
		for _, h := range headlines {
			sb.WriteString(fmt.Sprintf("  [%s] %s\n", h.Source, h.Title))
		}
	}

	sb.WriteString(`
ЗАДАЧА:
- Оцени текущую аллокацию между BTC/ETH/альткоинами
- Рекомендуй ребалансировку если нужно (ticker = символ пары, например BTCUSDT)
- Учитывай новости
- Не более 2 сделок

Если ничего не нужно — верни [] и кратко объясни.

Ответь ТОЛЬКО JSON-массивом:
[{"action":"BUY","ticker":"BTCUSDT","quantity":1,"reasoning":"..."}]`)
	return sb.String()
}

func buildCriticPrompt(s *models.UserSettings, p *tinvest.PortfolioSummary, analystRecs []models.TradeRecommendation, now time.Time) string {
	riskLabel := riskText(s.RiskLevel)
	recsJSON, _ := json.Marshal(analystRecs)

	var sb strings.Builder
	sb.WriteString("Ты — риск-менеджер торгового бота. Критически оцени рекомендации аналитика.\n\n")
	sb.WriteString(fmt.Sprintf("ВРЕМЯ: %s MSK\n", now.Format("02.01.2006 15:04")))
	sb.WriteString(fmt.Sprintf("ПРОФИЛЬ РИСКА: %s\n", riskLabel))
	sb.WriteString(fmt.Sprintf("МАКС. ПОТЕРЯ ЗА ЦИКЛ: %.1f%% (≈%.0f ₽)\n\n", s.MaxLossPct, p.TotalRUB*s.MaxLossPct/100))
	sb.WriteString(fmt.Sprintf("ПОРТФЕЛЬ: %.0f ₽, позиций: %d\n\n", p.TotalRUB, len(p.Positions)))
	sb.WriteString("РЕКОМЕНДАЦИИ АНАЛИТИКА:\n")
	sb.Write(recsJSON)
	sb.WriteString(`

Отклони если:
- Одна позиция займёт >30% портфеля
- Нет убедительного обоснования
- Риск выше допустимого для профиля

Верни ТОЛЬКО JSON одобренных сделок. [] если все отклонить:
[{"action":"BUY","ticker":"SBER","quantity":1,"reasoning":"..."}]`)
	return sb.String()
}

// =============================================================================
// Helpers
// =============================================================================

func writePortfolio(sb *strings.Builder, p *tinvest.PortfolioSummary) {
	sb.WriteString(fmt.Sprintf("\nПОРТФЕЛЬ (итого: %.0f ₽):\n", p.TotalRUB))
	for _, pos := range p.Positions {
		pct := 0.0
		if p.TotalRUB > 0 {
			pct = pos.ValueRUB / p.TotalRUB * 100
		}
		sb.WriteString(fmt.Sprintf(
			"  %-10s | %6.0f шт | %8.2f ₽/шт | %10.0f ₽ (%4.1f%%) | P&L: %+.0f ₽\n",
			pos.Ticker, pos.Quantity, pos.CurrentPriceRUB, pos.ValueRUB, pct, pos.ExpectedYieldRUB,
		))
	}
}

func writeQuotes(sb *strings.Builder, p *tinvest.PortfolioSummary, quotes map[string]*moex.Quote) {
	if len(quotes) == 0 {
		return
	}
	sb.WriteString("\nМОЕХ КОТИРОВКИ:\n")
	for _, pos := range p.Positions {
		norm := moex.NormalizeTicker(pos.Ticker)
		if q, ok := quotes[norm]; ok {
			sign := "+"
			if q.ChangePct < 0 {
				sign = ""
			}
			sb.WriteString(fmt.Sprintf("  %-10s | %8.2f ₽ | %s%.2f%% | объём: %.0f\n",
				pos.Ticker, q.Last, sign, q.ChangePct, q.Volume))
		}
	}
}

func riskText(level string) string {
	m := map[string]string{
		"low":    "Низкий (ОФЗ, SBER, GAZP, LKOH — только голубые фишки)",
		"medium": "Средний (ETF TMOS/SBMX, 2-3 эшелон с хорошим фундаменталом)",
		"high":   "Высокий (технологии, малые компании, спекулятивные идеи)",
	}
	if v, ok := m[level]; ok {
		return v
	}
	return "Средний"
}

func isMarketOpen(now time.Time) bool {
	wd := now.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	mins := now.Hour()*60 + now.Minute()
	return (mins >= 9*60+50 && mins < 18*60+50) || (mins >= 19*60+5 && mins < 23*60+50)
}

func parseRecs(resp string) ([]models.TradeRecommendation, error) {
	s := resp
	if i := strings.Index(s, "["); i >= 0 {
		if j := strings.LastIndex(s, "]"); j > i {
			s = s[i : j+1]
		}
	}
	var recs []models.TradeRecommendation
	if err := json.Unmarshal([]byte(s), &recs); err != nil {
		return nil, err
	}
	return recs, nil
}
