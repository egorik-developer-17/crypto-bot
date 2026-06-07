package crypto

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const baseURL = "https://api.coingecko.com/api/v3"

type Coin struct {
	ID                string  `json:"id"`
	Symbol            string  `json:"symbol"`
	Name              string  `json:"name"`
	CurrentPrice      float64 `json:"current_price"`
	MarketCap         float64 `json:"market_cap"`
	MarketCapRank     int     `json:"market_cap_rank"`
	PriceChange24h    float64 `json:"price_change_percentage_24h"`
	PriceChange7d     float64 `json:"price_change_percentage_7d_in_currency"`
	PriceChange30d    float64 `json:"price_change_percentage_30d_in_currency"`
	ATH               float64 `json:"ath"`
	ATHChangePercent  float64 `json:"ath_change_percentage"`
	TotalVolume       float64 `json:"total_volume"`
}

type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Client) get(url string, out any) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 {
		return fmt.Errorf("CoinGecko rate limit: подождите минуту и попробуйте снова")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("CoinGecko вернул %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	return json.Unmarshal(body, out)
}

// TopCoins возвращает топ N монет по капитализации
func (c *Client) TopCoins(n int) ([]Coin, error) {
	url := fmt.Sprintf(
		"%s/coins/markets?vs_currency=usd&order=market_cap_desc&per_page=%d&page=1"+
			"&price_change_percentage=24h,7d,30d&sparkline=false",
		baseURL, n,
	)
	var coins []Coin
	return coins, c.get(url, &coins)
}

// PriceBySymbols возвращает цены для списка символов (из портфеля)
func (c *Client) PriceByIDs(ids []string) (map[string]float64, error) {
	if len(ids) == 0 {
		return map[string]float64{}, nil
	}
	url := fmt.Sprintf(
		"%s/simple/price?ids=%s&vs_currencies=usd",
		baseURL, strings.Join(ids, ","),
	)
	var raw map[string]map[string]float64
	if err := c.get(url, &raw); err != nil {
		return nil, err
	}
	result := make(map[string]float64, len(raw))
	for id, prices := range raw {
		result[id] = prices["usd"]
	}
	return result, nil
}

// Recommend анализирует монеты и возвращает рекомендации
func Recommend(coins []Coin) []Recommendation {
	var recs []Recommendation
	for _, c := range coins {
		score, reasons := analyze(c)
		if score >= 2 {
			recs = append(recs, Recommendation{
				Coin:    c,
				Score:   score,
				Reasons: reasons,
			})
		}
	}
	// Сортировка по убыванию score (простая)
	for i := 0; i < len(recs)-1; i++ {
		for j := i + 1; j < len(recs); j++ {
			if recs[j].Score > recs[i].Score {
				recs[i], recs[j] = recs[j], recs[i]
			}
		}
	}
	if len(recs) > 5 {
		recs = recs[:5]
	}
	return recs
}

type Recommendation struct {
	Coin    Coin
	Score   int
	Reasons []string
}

func analyze(c Coin) (int, []string) {
	// Исключаем стейблкоины
	stable := []string{"usdt", "usdc", "busd", "dai", "tusd", "usdp", "frax"}
	for _, s := range stable {
		if strings.EqualFold(c.Symbol, s) {
			return 0, nil
		}
	}

	score := 0
	var reasons []string

	// Просадка за 24ч — потенциальная точка входа
	if c.PriceChange24h <= -5 && c.PriceChange24h >= -20 {
		score += 2
		reasons = append(reasons, fmt.Sprintf("📉 Просадка за 24ч: %.1f%% (откат)", c.PriceChange24h))
	} else if c.PriceChange24h <= -20 {
		score += 1
		reasons = append(reasons, fmt.Sprintf("⚠️ Сильное падение за 24ч: %.1f%% (высокий риск)", c.PriceChange24h))
	}

	// Недельный тренд нейтральный/негативный, но не катастрофа
	if c.PriceChange7d <= -10 && c.PriceChange7d >= -35 {
		score += 2
		reasons = append(reasons, fmt.Sprintf("📊 Недельная просадка: %.1f%% (зона интереса)", c.PriceChange7d))
	}

	// Далеко от ATH — есть потенциал роста
	if c.ATHChangePercent <= -60 {
		score += 1
		reasons = append(reasons, fmt.Sprintf("🏔 От ATH: %.1f%% (большой потенциал)", c.ATHChangePercent))
	} else if c.ATHChangePercent <= -30 {
		score += 1
		reasons = append(reasons, fmt.Sprintf("📈 От ATH: %.1f%%", c.ATHChangePercent))
	}

	// Объём торгов относительно капитализации (высокий = интерес)
	if c.MarketCap > 0 && c.TotalVolume/c.MarketCap > 0.15 {
		score += 1
		reasons = append(reasons, "🔥 Высокий объём торгов")
	}

	// Топ-50 по капитализации — надёжнее
	if c.MarketCapRank <= 50 {
		score += 1
		reasons = append(reasons, fmt.Sprintf("🥇 Топ-%d по капитализации", c.MarketCapRank))
	}

	// Рост за 30д при текущем откате — хороший знак
	if c.PriceChange30d > 10 && c.PriceChange24h < -3 {
		score += 1
		reasons = append(reasons, fmt.Sprintf("↗️ Месячный тренд: +%.1f%% (откат на росте)", c.PriceChange30d))
	}

	return score, reasons
}
