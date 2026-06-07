package crypto

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	baseURL       = "https://api.coingecko.com/api/v3"
	maxBodyBytes  = 5 * 1024 * 1024 // 5 MB — защита от OOM
)

type Coin struct {
	ID               string  `json:"id"`
	Symbol           string  `json:"symbol"`
	Name             string  `json:"name"`
	CurrentPrice     float64 `json:"current_price"`
	MarketCap        float64 `json:"market_cap"`
	MarketCapRank    int     `json:"market_cap_rank"`
	PriceChange24h   float64 `json:"price_change_percentage_24h"`
	PriceChange7d    float64 `json:"price_change_percentage_7d_in_currency"`
	PriceChange30d   float64 `json:"price_change_percentage_30d_in_currency"`
	ATH              float64 `json:"ath"`
	ATHChangePercent float64 `json:"ath_change_percentage"`
	TotalVolume      float64 `json:"total_volume"`
}

type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Client) get(url string, out any) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("ошибка формирования запроса: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка сети: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		return fmt.Errorf("CoinGecko rate limit — подождите минуту")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("CoinGecko вернул статус %d", resp.StatusCode)
	}

	// Ограничиваем размер ответа — защита от OOM
	limited := io.LimitReader(resp.Body, maxBodyBytes)
	body, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("ошибка чтения ответа: %w", err)
	}

	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("ошибка парсинга ответа: %w", err)
	}
	return nil
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

// PriceByIDs возвращает текущие цены для списка CoinGecko ID
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

// Recommend анализирует монеты и возвращает топ-5 рекомендаций
func Recommend(coins []Coin) []Recommendation {
	var recs []Recommendation
	for _, c := range coins {
		score, reasons := analyze(c)
		if score >= 2 {
			recs = append(recs, Recommendation{Coin: c, Score: score, Reasons: reasons})
		}
	}
	// Сортировка по убыванию score
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
	stable := []string{"usdt", "usdc", "busd", "dai", "tusd", "usdp", "frax"}
	for _, s := range stable {
		if strings.EqualFold(c.Symbol, s) {
			return 0, nil
		}
	}

	score := 0
	var reasons []string

	if c.PriceChange24h <= -5 && c.PriceChange24h >= -20 {
		score += 2
		reasons = append(reasons, fmt.Sprintf("📉 Просадка за 24ч: %.1f%% (откат)", c.PriceChange24h))
	} else if c.PriceChange24h <= -20 {
		score += 1
		reasons = append(reasons, fmt.Sprintf("⚠️ Сильное падение за 24ч: %.1f%% (высокий риск)", c.PriceChange24h))
	}

	if c.PriceChange7d <= -10 && c.PriceChange7d >= -35 {
		score += 2
		reasons = append(reasons, fmt.Sprintf("📊 Недельная просадка: %.1f%% (зона интереса)", c.PriceChange7d))
	}

	if c.ATHChangePercent <= -60 {
		score += 1
		reasons = append(reasons, fmt.Sprintf("🏔 От ATH: %.1f%% (большой потенциал)", c.ATHChangePercent))
	} else if c.ATHChangePercent <= -30 {
		score += 1
		reasons = append(reasons, fmt.Sprintf("📈 От ATH: %.1f%%", c.ATHChangePercent))
	}

	if c.MarketCap > 0 && c.TotalVolume/c.MarketCap > 0.15 {
		score += 1
		reasons = append(reasons, "🔥 Высокий объём торгов")
	}

	if c.MarketCapRank <= 50 {
		score += 1
		reasons = append(reasons, fmt.Sprintf("🥇 Топ-%d по капитализации", c.MarketCapRank))
	}

	if c.PriceChange30d > 10 && c.PriceChange24h < -3 {
		score += 1
		reasons = append(reasons, fmt.Sprintf("↗️ Месячный тренд: +%.1f%% (откат на росте)", c.PriceChange30d))
	}

	return score, reasons
}
