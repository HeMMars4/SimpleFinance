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

// Runner manages per-user trading bot goroutines.
type Runner struct {
	db   *storage.DB
	mu   sync.RWMutex
	bots map[int64]context.CancelFunc
}

func New(db *storage.DB) *Runner {
	return &Runner{
		db:   db,
		bots: make(map[int64]context.CancelFunc),
	}
}

func (r *Runner) IsRunning(userID int64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.bots[userID]
	return ok
}

// Start launches a bot goroutine for the user if one is not already running.
func (r *Runner) Start(userID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.bots[userID]; ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.bots[userID] = cancel
	go r.run(ctx, userID)
}

// Stop cancels the bot goroutine for the user.
func (r *Runner) Stop(userID int64) {
	r.mu.Lock()
	if cancel, ok := r.bots[userID]; ok {
		cancel()
		delete(r.bots, userID)
	}
	r.mu.Unlock()
	// Persist stopped state
	go func() {
		s, err := r.db.GetUserSettings(context.Background(), userID)
		if err == nil {
			s.BotEnabled = false
			r.db.SaveUserSettings(context.Background(), s) //nolint:errcheck
		}
		r.addLog(context.Background(), userID, "info", "Торговый бот остановлен")
	}()
}

func (r *Runner) addLog(ctx context.Context, userID int64, level, msg string) {
	slog.Info("bot", "user", userID, "level", level, "msg", msg)
	r.db.AddBotLog(ctx, userID, level, msg) //nolint:errcheck
}

func (r *Runner) run(ctx context.Context, userID int64) {
	r.addLog(ctx, userID, "info", "Торговый бот запущен")
	defer func() {
		r.mu.Lock()
		delete(r.bots, userID)
		r.mu.Unlock()
	}()

	// Persist enabled state
	if s, err := r.db.GetUserSettings(ctx, userID); err == nil {
		s.BotEnabled = true
		r.db.SaveUserSettings(ctx, s) //nolint:errcheck
	}

	for {
		r.cycle(ctx, userID)

		s, err := r.db.GetUserSettings(ctx, userID)
		if err != nil {
			return
		}
		interval := time.Duration(s.BotIntervalMinutes) * time.Minute
		if interval < time.Minute {
			interval = 60 * time.Minute
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (r *Runner) cycle(ctx context.Context, userID int64) {
	s, err := r.db.GetUserSettings(ctx, userID)
	if err != nil {
		r.addLog(ctx, userID, "error", "Ошибка загрузки настроек: "+err.Error())
		return
	}

	if s.ClaudeAPIKey == "" || s.TInvestTradeToken == "" {
		r.addLog(ctx, userID, "warn", "Не заданы Claude API Key или торговый токен T-Инвестиции")
		return
	}

	now := time.Now().In(moscowTZ)
	if !isMarketOpen(now) {
		r.addLog(ctx, userID, "info", fmt.Sprintf("Биржа закрыта (%s MSK), цикл пропущен", now.Format("15:04")))
		return
	}

	r.addLog(ctx, userID, "info", fmt.Sprintf("▶ Цикл [%s MSK]", now.Format("02.01 15:04")))

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
		"Портфель: %.0f ₽, позиций: %d", portfolio.TotalRUB, len(portfolio.Positions),
	))

	// Margin check
	var marginInfo string
	if s.BotUseMargin {
		margin, err := tc.GetMarginAttributes(ctx, accountID)
		if err != nil {
			r.addLog(ctx, userID, "warn", "Маржинальные данные недоступны: "+err.Error())
		} else {
			marginInfo = fmt.Sprintf(
				"Ликвидный портфель %.0f ₽, маржа %.0f ₽, уровень обеспечения %.2f",
				margin.LiquidPortfolio, margin.StartingMargin, margin.FundsSufficiencyLevel,
			)
			r.addLog(ctx, userID, "info", "Маржа: "+marginInfo)
			if margin.FundsSufficiencyLevel < 1.0 {
				r.addLog(ctx, userID, "warn", "⚠ Уровень обеспечения ниже нормы — покупки заблокированы")
			}
		}
	}

	prompt := buildPrompt(s, portfolio, marginInfo, now)

	r.addLog(ctx, userID, "info", "Запрос к Claude AI...")
	aiResp, err := cc.Ask(ctx, prompt)
	if err != nil {
		r.addLog(ctx, userID, "error", "Claude API: "+err.Error())
		return
	}

	recs, err := parseRecs(aiResp)
	if err != nil {
		r.addLog(ctx, userID, "warn", "Не удалось разобрать ответ Claude: "+err.Error())
		return
	}

	if len(recs) == 0 {
		r.addLog(ctx, userID, "info", "Claude: никаких сделок не рекомендует")
		return
	}

	r.addLog(ctx, userID, "info", fmt.Sprintf("Claude рекомендует %d сделок", len(recs)))

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
			r.addLog(ctx, userID, "error", fmt.Sprintf("%s %s: ошибка ордера (%s)", rec.Action, rec.Ticker, err.Error()))
			continue
		}

		r.addLog(ctx, userID, "trade", fmt.Sprintf(
			"✓ %s %s ×%d | orderID: %s", rec.Action, rec.Ticker, rec.Quantity, orderID,
		))

		// Auto stop-loss on BUY
		if rec.Action == "BUY" {
			for _, pos := range portfolio.Positions {
				if strings.EqualFold(pos.Ticker, rec.Ticker) && pos.CurrentPriceRUB > 0 {
					stopPrice := pos.CurrentPriceRUB * (1.0 - s.MaxLossPct/100.0)
					slID, err := tc.PostStopOrder(ctx, accountID, figi, rec.Quantity, stopPrice, "STOP_ORDER_DIRECTION_SELL")
					if err != nil {
						r.addLog(ctx, userID, "warn", fmt.Sprintf("Стоп-лосс %s не выставлен: %s", rec.Ticker, err.Error()))
					} else {
						r.addLog(ctx, userID, "info", fmt.Sprintf(
							"Стоп-лосс %s на %.2f ₽ | stopID: %s", rec.Ticker, stopPrice, slID,
						))
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

	// Verify result after trades settle
	time.Sleep(3 * time.Second)
	newPortfolio, err := tc.GetLivePortfolio(ctx, accountID, usdRUB)
	if err == nil {
		diff := newPortfolio.TotalRUB - portfolio.TotalRUB
		sign := "+"
		if diff < 0 {
			sign = ""
		}
		r.addLog(ctx, userID, "info", fmt.Sprintf(
			"◀ Цикл завершён | Портфель: %.0f ₽ (%s%.0f ₽)", newPortfolio.TotalRUB, sign, diff,
		))
	} else {
		r.addLog(ctx, userID, "info", "◀ Цикл завершён")
	}
}

// isMarketOpen checks MOEX trading hours (Moscow time).
// Main session: 09:50–18:50, evening session: 19:05–23:50, Mon–Fri only.
func isMarketOpen(now time.Time) bool {
	wd := now.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	mins := now.Hour()*60 + now.Minute()
	return (mins >= 9*60+50 && mins < 18*60+50) || (mins >= 19*60+5 && mins < 23*60+50)
}

func buildPrompt(s *models.UserSettings, p *tinvest.PortfolioSummary, marginInfo string, now time.Time) string {
	riskLabel := map[string]string{
		"low":    "Низкий (ОФЗ, SBER, GAZP, LKOH — только голубые фишки)",
		"medium": "Средний (ETF TMOS/SBMX, 2-3 эшелон с хорошим фундаменталом)",
		"high":   "Высокий (технологии, малые компании, спекулятивные идеи)",
	}[s.RiskLevel]
	if riskLabel == "" {
		riskLabel = "Средний"
	}

	var sb strings.Builder
	sb.WriteString("Ты — торговый бот на Московской бирже (MOEX). Автономный режим.\n\n")
	sb.WriteString(fmt.Sprintf("ВРЕМЯ: %s MSK\n", now.Format("02.01.2006 15:04")))
	sb.WriteString(fmt.Sprintf("ПРОФИЛЬ РИСКА: %s\n", riskLabel))
	sb.WriteString(fmt.Sprintf("МАКС. ПОТЕРЯ ЗА ЦИКЛ: %.1f%% (≈%.0f ₽)\n", s.MaxLossPct, p.TotalRUB*s.MaxLossPct/100))
	if marginInfo != "" {
		sb.WriteString(fmt.Sprintf("МАРЖИНАЛЬНАЯ ТОРГОВЛЯ: %s\n", marginInfo))
	}
	sb.WriteString(fmt.Sprintf("\nПОРТФЕЛЬ (итого: %.0f ₽):\n", p.TotalRUB))
	for _, pos := range p.Positions {
		pct := 0.0
		if p.TotalRUB > 0 {
			pct = pos.ValueRUB / p.TotalRUB * 100
		}
		sb.WriteString(fmt.Sprintf(
			"  %-10s | %-8s | %6.0f шт | %8.2f ₽/шт | %10.0f ₽ (%4.1f%%) | P&L: %+.0f ₽\n",
			pos.Ticker, pos.InstrumentType, pos.Quantity, pos.CurrentPriceRUB, pos.ValueRUB, pct, pos.ExpectedYieldRUB,
		))
	}
	sb.WriteString(`
ПРАВИЛА:
1. Не более 3 сделок за один цикл
2. Стоп-лоссы выставляются автоматически при покупке (на уровне макс. потери)
3. Количество в ЛОТАХ MOEX (минимальная торговая единица, обычно 1-10 акций)
4. Если ничего не нужно делать — верни пустой массив []
5. Не продавай позицию целиком без веской причины
6. Учитывай текущее время и фазу торговой сессии

Ответь ТОЛЬКО JSON-массивом, без дополнительного текста:
[{"action":"BUY","ticker":"SBER","quantity":1,"reasoning":"Краткое обоснование на русском"}]`)
	return sb.String()
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
