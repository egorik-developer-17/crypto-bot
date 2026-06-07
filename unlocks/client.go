package unlocks

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// CoinGecko — бесплатно, без ключей.
// Анализ разлоков: сравниваем total_supply vs circulating_supply (риск дилюции)
// + парсим 90-дневный market chart для обнаружения прошлых разлоков и прогноза следующих.
const (
	marketsURL   = "https://api.coingecko.com/api/v3/coins/markets"
	chartURL     = "https://api.coingecko.com/api/v3/coins/%s/market_chart"
	maxBodyBytes = 5 * 1024 * 1024
)

type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: 20 * time.Second}}
}

// ─── Модели (риск дилюции) ──────────────────────────────────────────────────

type coin struct {
	ID              string  `json:"id"`
	Symbol          string  `json:"symbol"`
	Name            string  `json:"name"`
	CurrentPrice    float64 `json:"current_price"`
	MarketCap       float64 `json:"market_cap"`
	MarketCapRank   int     `json:"market_cap_rank"`
	CirculatingSupp float64 `json:"circulating_supply"`
	TotalSupply     float64 `json:"total_supply"`
	MaxSupply       float64 `json:"max_supply"`
}

// UnlockRisk — оценка риска разлоков для конкретной монеты
type UnlockRisk struct {
	CoinID            string // CoinGecko ID для исторических запросов
	Name              string
	Symbol            string
	Rank              int
	Price             float64
	Circulating       float64
	TotalSupply       float64
	LockedTokens      float64 // total_supply - circulating_supply
	LockedPercent     float64 // % от total_supply, который ещё не в обращении
	DilutionPotential float64 // во сколько раз может вырасти supply
	LockedUSDValue    float64 // потенциальное давление продажи в USD
	RiskLevel         string  // 🟢 low, 🟡 medium, 🔴 high, ⚫ extreme
}

// ─── Модели (прогноз дат) ───────────────────────────────────────────────────

type marketChartData struct {
	Prices     [][]float64 `json:"prices"`      // [timestamp_ms, price]
	MarketCaps [][]float64 `json:"market_caps"` // [timestamp_ms, market_cap]
}

type supplyPoint struct {
	Date   time.Time
	Supply float64
}

type supplyJump struct {
	Date        time.Time
	TokensAdded float64
	PctIncrease float64
}

// EstimatedUnlock — прогноз следующей разблокировки на основе исторических данных
type EstimatedUnlock struct {
	Name       string
	Symbol     string
	EstDate    time.Time
	Tokens     float64
	USDValue   float64
	Frequency  string // "ежедневно", "еженедельно", "ежемесячно", etc.
	LastUnlock time.Time
	DaysUntil  int
	HasData    bool
}

// ─── Получение топ-100 монет ─────────────────────────────────────────────────

func (c *Client) fetchMarkets() ([]coin, error) {
	url := fmt.Sprintf(
		"%s?vs_currency=usd&order=market_cap_desc&per_page=100&page=1&sparkline=false",
		marketsURL,
	)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "crypto-bot/1.0")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ошибка сети: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		return nil, fmt.Errorf("CoinGecko rate limit — подождите минуту")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("CoinGecko вернул статус %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, err
	}

	var coins []coin
	if err := json.Unmarshal(body, &coins); err != nil {
		return nil, fmt.Errorf("ошибка парсинга: %w", err)
	}
	return coins, nil
}

// FetchHighRisk возвращает монеты с наибольшим риском разлоков
func (c *Client) FetchHighRisk() ([]UnlockRisk, error) {
	coins, err := c.fetchMarkets()
	if err != nil {
		return nil, err
	}

	stables := map[string]bool{
		"tether": true, "usd-coin": true, "binance-usd": true,
		"dai": true, "true-usd": true, "frax": true, "paxos-standard": true,
		"first-digital-usd": true, "ethena-usde": true, "usdd": true,
	}

	var risks []UnlockRisk

	for _, coin := range coins {
		if stables[coin.ID] {
			continue
		}

		supply := coin.TotalSupply
		if supply == 0 {
			supply = coin.MaxSupply
		}
		if supply == 0 || coin.CirculatingSupp == 0 {
			continue
		}
		if coin.CirculatingSupp >= supply {
			continue
		}

		locked := supply - coin.CirculatingSupp
		lockedPct := (locked / supply) * 100
		dilution := supply / coin.CirculatingSupp
		lockedUSD := locked * coin.CurrentPrice

		if lockedUSD < 10_000_000 {
			continue
		}

		risks = append(risks, UnlockRisk{
			CoinID:            coin.ID,
			Name:              coin.Name,
			Symbol:            strings.ToUpper(coin.Symbol),
			Rank:              coin.MarketCapRank,
			Price:             coin.CurrentPrice,
			Circulating:       coin.CirculatingSupp,
			TotalSupply:       supply,
			LockedTokens:      locked,
			LockedPercent:     lockedPct,
			DilutionPotential: dilution,
			LockedUSDValue:    lockedUSD,
			RiskLevel:         calcRiskLevel(lockedPct, dilution),
		})
	}

	sort.Slice(risks, func(i, j int) bool {
		return risks[i].LockedPercent > risks[j].LockedPercent
	})

	return risks, nil
}

func calcRiskLevel(lockedPct, dilution float64) string {
	switch {
	case lockedPct >= 70 || dilution >= 5:
		return "⚫ ЭКСТРЕМАЛЬНЫЙ"
	case lockedPct >= 50 || dilution >= 3:
		return "🔴 ВЫСОКИЙ"
	case lockedPct >= 30 || dilution >= 1.8:
		return "🟡 СРЕДНИЙ"
	default:
		return "🟢 НИЗКИЙ"
	}
}

// ─── Исторический анализ supply для прогноза дат ───────────────────────────

// fetchMarketChart получает 90-дневную историю market cap и цен для монеты
func (c *Client) fetchMarketChart(coinID string) (*marketChartData, error) {
	url := fmt.Sprintf("%s?vs_currency=usd&days=90&interval=daily", fmt.Sprintf(chartURL, coinID))

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "crypto-bot/1.0")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		return nil, fmt.Errorf("rate limit")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("статус %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, err
	}

	var data marketChartData
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

// computeDailySupply вычисляет обращающееся supply = market_cap / price за каждый день
func computeDailySupply(data *marketChartData) []supplyPoint {
	// Строим map timestamp → price
	priceMap := make(map[int64]float64, len(data.Prices))
	for _, p := range data.Prices {
		if len(p) < 2 {
			continue
		}
		ts := int64(p[0]) / 1000 // ms → s
		// округляем до суток
		day := (ts / 86400) * 86400
		priceMap[day] = p[1]
	}

	var points []supplyPoint
	for _, mc := range data.MarketCaps {
		if len(mc) < 2 {
			continue
		}
		ts := int64(mc[0]) / 1000
		day := (ts / 86400) * 86400
		price, ok := priceMap[day]
		if !ok || price < 1e-12 {
			continue
		}
		supply := mc[1] / price
		if supply <= 0 || math.IsNaN(supply) || math.IsInf(supply, 0) {
			continue
		}
		points = append(points, supplyPoint{
			Date:   time.Unix(day, 0).UTC(),
			Supply: supply,
		})
	}

	// Сортируем по дате
	sort.Slice(points, func(i, j int) bool {
		return points[i].Date.Before(points[j].Date)
	})

	// Дедупликация (один день = одна точка, берём последнюю)
	if len(points) == 0 {
		return nil
	}
	deduped := []supplyPoint{points[0]}
	for i := 1; i < len(points); i++ {
		if points[i].Date.Equal(deduped[len(deduped)-1].Date) {
			deduped[len(deduped)-1] = points[i]
		} else {
			deduped = append(deduped, points[i])
		}
	}
	return deduped
}

// detectSupplyJumps находит дни, когда supply резко вырос (= разлок токенов)
// Порог: рост > 0.8% за сутки — исключает ценовой шум, ловит реальные разлоки
func detectSupplyJumps(points []supplyPoint) []supplyJump {
	const threshold = 0.008 // 0.8%

	var jumps []supplyJump
	for i := 1; i < len(points); i++ {
		prev := points[i-1].Supply
		curr := points[i].Supply
		if prev <= 0 {
			continue
		}
		pct := (curr - prev) / prev
		if pct > threshold {
			jumps = append(jumps, supplyJump{
				Date:        points[i].Date,
				TokensAdded: curr - prev,
				PctIncrease: pct * 100,
			})
		}
	}
	return jumps
}

// analyzeOne анализирует один токен и возвращает прогноз
func (c *Client) analyzeOne(risk UnlockRisk) EstimatedUnlock {
	est := EstimatedUnlock{
		Name:   risk.Name,
		Symbol: risk.Symbol,
	}

	chart, err := c.fetchMarketChart(risk.CoinID)
	if err != nil {
		return est
	}

	points := computeDailySupply(chart)
	if len(points) < 14 { // нужно минимум 2 недели данных
		return est
	}

	jumps := detectSupplyJumps(points)
	if len(jumps) == 0 {
		return est
	}

	est.HasData = true
	lastJump := jumps[len(jumps)-1]
	est.LastUnlock = lastJump.Date

	if len(jumps) == 1 {
		// Один разлок — скорее всего разовый клифф, или данных мало
		// Берём среднюю токенов и не прогнозируем дату
		est.Tokens = lastJump.TokensAdded
		est.USDValue = est.Tokens * risk.Price
		est.Frequency = "разовый / нет паттерна"
		return est
	}

	// Несколько разлоков — вычисляем среднюю частоту и объём
	totalInterval := 0.0
	totalTokens := 0.0
	for i := 1; i < len(jumps); i++ {
		days := jumps[i].Date.Sub(jumps[i-1].Date).Hours() / 24
		totalInterval += days
	}
	for _, j := range jumps {
		totalTokens += j.TokensAdded
	}

	avgDays := totalInterval / float64(len(jumps)-1)
	avgTokens := totalTokens / float64(len(jumps))

	est.Tokens = avgTokens
	est.USDValue = avgTokens * risk.Price

	nextUnlock := lastJump.Date.Add(time.Duration(avgDays * 24 * float64(time.Hour)))
	est.EstDate = nextUnlock
	est.DaysUntil = int(math.Round(time.Until(nextUnlock).Hours() / 24))

	switch {
	case avgDays < 3:
		est.Frequency = "ежедневно"
	case avgDays < 10:
		est.Frequency = "еженедельно"
	case avgDays < 20:
		est.Frequency = "каждые 2 недели"
	case avgDays < 45:
		est.Frequency = "ежемесячно"
	case avgDays < 100:
		est.Frequency = "ежеквартально"
	default:
		est.Frequency = fmt.Sprintf("каждые ~%.0f дней", avgDays)
	}

	return est
}

// FetchSchedule параллельно анализирует топ-N рискованных монет
// и возвращает прогнозы дат следующих разлоков
func (c *Client) FetchSchedule(risks []UnlockRisk) []EstimatedUnlock {
	const maxCoins = 6 // не больше 6, чтобы уложиться в rate limit CoinGecko

	limit := maxCoins
	if len(risks) < limit {
		limit = len(risks)
	}
	target := risks[:limit]

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results []EstimatedUnlock
	)

	for _, r := range target {
		wg.Add(1)
		go func(risk UnlockRisk) {
			defer wg.Done()
			est := c.analyzeOne(risk)
			mu.Lock()
			results = append(results, est)
			mu.Unlock()
		}(r)
	}
	wg.Wait()

	// Сортировка: сначала те, у кого есть дата и она ближе
	sort.Slice(results, func(i, j int) bool {
		hi, hj := results[i].HasData && !results[i].EstDate.IsZero(), results[j].HasData && !results[j].EstDate.IsZero()
		if hi && hj {
			return results[i].EstDate.Before(results[j].EstDate)
		}
		return hi && !hj
	})

	return results
}

// ─── Форматирование ────────────────────────────────────────────────────────

func Format(risks []UnlockRisk, schedule []EstimatedUnlock) string {
	if len(risks) == 0 {
		return "📭 Данные по разлокам недоступны. Попробуйте позже."
	}

	var sb strings.Builder

	// ── Секция 1: Риск дилюции ──
	sb.WriteString("🔓 Риск разлоков токенов\n")
	sb.WriteString("(топ-100 монет, источник: CoinGecko)\n\n")

	limit := 10
	if len(risks) < limit {
		limit = len(risks)
	}

	for i := 0; i < limit; i++ {
		r := risks[i]
		sb.WriteString(fmt.Sprintf(
			"%d. %s (%s) — #%d по капе\n",
			i+1, r.Name, r.Symbol, r.Rank,
		))
		sb.WriteString(fmt.Sprintf("   %s\n", r.RiskLevel))
		sb.WriteString(fmt.Sprintf(
			"   🔒 Заблокировано: %.1f%% supply (~$%s)\n",
			r.LockedPercent, formatLargeNumber(r.LockedUSDValue),
		))
		sb.WriteString(fmt.Sprintf(
			"   📈 Дилюция: x%.1f\n\n",
			r.DilutionPotential,
		))
	}

	sb.WriteString("⚫>70% | 🔴 50-70% | 🟡 30-50% | 🟢<30%\n")

	// ── Секция 2: Прогноз дат ──
	if len(schedule) > 0 {
		sb.WriteString("\n📅 Ближайшие разблокировки\n")
		sb.WriteString("(прогноз по on-chain паттернам за 90 дней)\n\n")

		hasAny := false
		for _, s := range schedule {
			if !s.HasData {
				continue
			}
			hasAny = true

			sb.WriteString(fmt.Sprintf("▸ %s (%s)\n", s.Name, s.Symbol))

			if !s.EstDate.IsZero() {
				dateStr := s.EstDate.UTC().Format("02 Jan 2006")
				daysStr := ""
				if s.DaysUntil > 0 {
					daysStr = fmt.Sprintf(" (через %d дн.)", s.DaysUntil)
				} else if s.DaysUntil == 0 {
					daysStr = " (сегодня!)"
				} else {
					daysStr = fmt.Sprintf(" (%d дн. назад)", -s.DaysUntil)
				}
				sb.WriteString(fmt.Sprintf("   📆 ~%s%s\n", dateStr, daysStr))
			}

			if s.Tokens > 0 {
				sb.WriteString(fmt.Sprintf(
					"   💰 ~%s токенов (~$%s)\n",
					formatLargeNumber(s.Tokens), formatLargeNumber(s.USDValue),
				))
			}

			if s.Frequency != "" {
				sb.WriteString(fmt.Sprintf("   🔄 %s\n", s.Frequency))
			}

			lastStr := s.LastUnlock.UTC().Format("02 Jan")
			sb.WriteString(fmt.Sprintf("   ⏱ Последний разлок: %s\n\n", lastStr))
		}

		if !hasAny {
			sb.WriteString("Паттерны разлоков не обнаружены за 90 дней.\n\n")
		}

		sb.WriteString("⚠️ Прогноз основан на исторических данных supply.\n")
		sb.WriteString("   Точные даты — на tokenomist.ai\n")
	}

	return sb.String()
}

func formatLargeNumber(n float64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", n/1_000_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", n/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", n/1_000)
	default:
		return fmt.Sprintf("%.2f", n)
	}
}
