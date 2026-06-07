package news

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	newsURL      = "https://min-api.cryptocompare.com/data/v2/news/?lang=EN&sortOrder=latest&extraParams=crypto-bot"
	maxBodyBytes = 3 * 1024 * 1024
)

type Article struct {
	Title       string `json:"title"`
	Body        string `json:"body"`
	URL         string `json:"url"`
	Source      string `json:"source_info"`
	PublishedOn int64  `json:"published_on"`
}

type response struct {
	Data []Article `json:"Data"`
}

type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: 12 * time.Second}}
}

func (c *Client) FetchLatest() ([]Article, error) {
	req, err := http.NewRequest("GET", newsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("ошибка запроса: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ошибка сети: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API вернул статус %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения: %w", err)
	}

	var res response
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("ошибка парсинга: %w", err)
	}
	return res.Data, nil
}

// ─── Анализ китовых сигналов ───────────────────────────────────────────────

type Signal struct {
	Article    Article
	WhaleScore int    // насколько статья про китов (0-10)
	Sentiment  int    // >0 бычий, <0 медвежий, 0 нейтральный
	Label      string // BUY / SELL / HOLD
}

var bullishKeywords = []string{
	"whale buying", "whale accumulation", "whales accumulate",
	"institutional buying", "large purchase", "massive buy",
	"bullish", "accumulate", "inflow", "buy the dip",
	"record inflow", "etf inflow", "spot etf",
}

var bearishKeywords = []string{
	"whale selling", "whale dump", "whale transfer",
	"large withdrawal", "exchange inflow", "sell-off",
	"bearish", "dump", "outflow", "panic sell",
	"liquidation", "massive sell", "exchange deposit",
}

var whaleKeywords = []string{
	"whale", "whales", "large holder", "big player",
	"institutional", "billion", "million btc", "million eth",
	"on-chain", "wallet", "cold wallet", "exchange transfer",
	"satoshi", "unlock", "vesting", "large transaction",
}

func Analyze(articles []Article) []Signal {
	var signals []Signal

	for _, a := range articles {
		text := strings.ToLower(a.Title + " " + a.Body)

		whaleScore := countKeywords(text, whaleKeywords)
		if whaleScore == 0 {
			continue // не про китов — пропускаем
		}

		bullScore := countKeywords(text, bullishKeywords)
		bearScore := countKeywords(text, bearishKeywords)
		sentiment := bullScore - bearScore

		label := "🟡 HOLD"
		if sentiment > 0 {
			label = "🟢 BUY"
		} else if sentiment < 0 {
			label = "🔴 SELL"
		}

		signals = append(signals, Signal{
			Article:    a,
			WhaleScore: whaleScore,
			Sentiment:  sentiment,
			Label:      label,
		})
	}

	// Сортируем по whale score убыванию
	for i := 0; i < len(signals)-1; i++ {
		for j := i + 1; j < len(signals); j++ {
			if signals[j].WhaleScore > signals[i].WhaleScore {
				signals[i], signals[j] = signals[j], signals[i]
			}
		}
	}

	if len(signals) > 7 {
		signals = signals[:7]
	}
	return signals
}

// OverallRecommendation формирует итоговую оценку по всем сигналам
func OverallRecommendation(signals []Signal) string {
	if len(signals) == 0 {
		return "🤷 Китовой активности не обнаружено. Рынок спокоен."
	}

	totalSentiment := 0
	buyCount, sellCount, holdCount := 0, 0, 0
	for _, s := range signals {
		totalSentiment += s.Sentiment
		switch {
		case s.Sentiment > 0:
			buyCount++
		case s.Sentiment < 0:
			sellCount++
		default:
			holdCount++
		}
	}

	var verdict, advice string
	switch {
	case totalSentiment >= 3:
		verdict = "🟢 ПОКУПАТЬ"
		advice = "Киты активно накапливают позиции. Вероятен рост."
	case totalSentiment >= 1:
		verdict = "🟢 СЛАБЫЙ BUY"
		advice = "Небольшое преобладание бычьих сигналов. Осторожный вход."
	case totalSentiment <= -3:
		verdict = "🔴 ПРОДАВАТЬ"
		advice = "Киты выходят из позиций. Возможна коррекция."
	case totalSentiment <= -1:
		verdict = "🔴 СЛАБЫЙ SELL"
		advice = "Небольшое преобладание медвежьих сигналов. Осторожность."
	default:
		verdict = "🟡 ДЕРЖАТЬ"
		advice = "Сигналы противоречивые. Наблюдайте за рынком."
	}

	return fmt.Sprintf(
		"Итоговая оценка: %s\n%s\n(BUY: %d | SELL: %d | HOLD: %d)",
		verdict, advice, buyCount, sellCount, holdCount,
	)
}

func FormatSignals(signals []Signal) string {
	if len(signals) == 0 {
		return "Новостей о движениях китов не найдено."
	}

	var sb strings.Builder
	for i, s := range signals {
		t := time.Unix(s.Article.PublishedOn, 0).UTC().Format("02.01 15:04")
		// Обрезаем заголовок если слишком длинный
		title := s.Article.Title
		if len(title) > 90 {
			title = title[:87] + "..."
		}
		sb.WriteString(fmt.Sprintf(
			"%d. %s  [%s]\n   %s\n   🐋 Активность китов: %s | Время: %s UTC\n\n",
			i+1, title, s.Label,
			s.Article.URL,
			whaleScoreBar(s.WhaleScore),
			t,
		))
	}
	return sb.String()
}

func countKeywords(text string, keywords []string) int {
	count := 0
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			count++
		}
	}
	return count
}

func whaleScoreBar(score int) string {
	if score >= 5 {
		return "█████ высокая"
	} else if score >= 3 {
		return "███░░ средняя"
	}
	return "█░░░░ низкая"
}
