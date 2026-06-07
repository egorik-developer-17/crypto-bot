package unlocks

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// CoinGecko — бесплатно, без ключей. Используем для оценки потенциала дилюции токенов:
// чем больше разница между total_supply и circulating_supply, тем больше монет
// ещё предстоит выпустить в обращение (= потенциальное давление разлоков).
const (
	marketsURL   = "https://api.coingecko.com/api/v3/coins/markets"
	maxBodyBytes = 5 * 1024 * 1024
)

type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: 15 * time.Second}}
}

// ─── Модели ────────────────────────────────────────────────────────────────

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
	ATL             float64 `json:"atl"`
}

// UnlockRisk — оценка риска разлоков для конкретной монеты
type UnlockRisk struct {
	Name             string
	Symbol           string
	Rank             int
	Price            float64
	Circulating      float64
	TotalSupply      float64
	LockedTokens     float64 // total_supply - circulating_supply
	LockedPercent    float64 // % от total_supply, который ещё не в обращении
	DilutionPotential float64 // во сколько раз может вырасти supply
	LockedUSDValue   float64 // потенциальное давление продажи в USD
	RiskLevel        string  // 🟢 low, 🟡 medium, 🔴 high, ⚫ extreme
}

// ─── Получение данных ────────────────────────────────────────────────────

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

	// Стейблкоины не интересуют
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

		// Нужны данные по supply
		supply := coin.TotalSupply
		if supply == 0 {
			supply = coin.MaxSupply
		}
		if supply == 0 || coin.CirculatingSupp == 0 {
			continue
		}
		if coin.CirculatingSupp >= supply {
			continue // нечего разлочивать
		}

		locked := supply - coin.CirculatingSupp
		lockedPct := (locked / supply) * 100
		dilution := supply / coin.CirculatingSupp
		lockedUSD := locked * coin.CurrentPrice

		// Фильтруем шум: минимум $10M потенциальной дилюции
		if lockedUSD < 10_000_000 {
			continue
		}

		risks = append(risks, UnlockRisk{
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

	// Сортируем по проценту заблокированных токенов
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

// ─── Форматирование ───────────────────────────────────────────────────────

func Format(risks []UnlockRisk) string {
	if len(risks) == 0 {
		return "📭 Данные по разлокам недоступны. Попробуйте позже."
	}

	var sb strings.Builder
	sb.WriteString("🔓 Риск разлоков токенов\n")
	sb.WriteString("(топ-100 монет по риску дилюции — источник: CoinGecko)\n\n")

	limit := 12
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
			"   🔒 Заблокировано: %s (%.1f%% от supply)\n",
			formatLargeNumber(r.LockedTokens), r.LockedPercent,
		))
		sb.WriteString(fmt.Sprintf(
			"   💰 Потенциал давления: ~$%s\n",
			formatLargeNumber(r.LockedUSDValue),
		))
		sb.WriteString(fmt.Sprintf(
			"   📈 Возможная дилюция: x%.1f\n\n",
			r.DilutionPotential,
		))
	}

	sb.WriteString("─────────────────────────\n")
	sb.WriteString("⚫ ЭКСТРЕМАЛЬНЫЙ — >70% supply заблокировано\n")
	sb.WriteString("🔴 ВЫСОКИЙ — 50-70% заблокировано\n")
	sb.WriteString("🟡 СРЕДНИЙ — 30-50% заблокировано\n")
	sb.WriteString("🟢 НИЗКИЙ — <30% заблокировано\n\n")
	sb.WriteString("⚠️ Чем больше заблокированных токенов, тем выше риск\n")
	sb.WriteString("    давления продавцов при будущих разлоках.\n")
	sb.WriteString("    Учитывайте это в долгосрочной стратегии!")

	return sb.String()
}

func formatLargeNumber(n float64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", n/1_000_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", n/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.2fK", n/1_000)
	default:
		return fmt.Sprintf("%.2f", n)
	}
}
