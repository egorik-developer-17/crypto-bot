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

// Используем CoinMarketCal API — бесплатный ключ на coinmarketcal.com
const (
	baseURL      = "https://api.coinmarketcal.com/v1"
	maxBodyBytes = 2 * 1024 * 1024
)

type Client struct {
	apiKey string
	http   *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		http:   &http.Client{Timeout: 12 * time.Second},
	}
}

// ─── Модели ───────────────────────────────────────────────────────────────

type Event struct {
	Title      string    `json:"title"`
	Coin       CoinInfo  `json:"coins"`
	DateEvent  string    `json:"date_event"`
	CreatedDate string   `json:"created_date"`
	Source     string    `json:"source"`
	CanOccur   bool      `json:"can_occur_before"`
	Percentage float64   `json:"percentage"` // % уверенности события
	Votes      VoteInfo  `json:"vote_count_up"`
}

type CoinInfo struct {
	FullName string `json:"fullname"`
	Symbol   string `json:"symbol"`
}

type VoteInfo struct {
	Up   int `json:"up"`
	Down int `json:"down"`
}

type apiResponse struct {
	Body []rawEvent `json:"body"`
}

type rawEvent struct {
	Title       string    `json:"title"`
	DateEvent   string    `json:"date_event"`
	CreatedDate string    `json:"created_date"`
	Source      string    `json:"source"`
	CanOccur    bool      `json:"can_occur_before"`
	Percentage  float64   `json:"percentage"`
	Coins       []CoinInfo `json:"coins"`
	VoteCount   struct {
		Up   int `json:"positive"`
		Down int `json:"negative"`
	} `json:"vote_count"`
}

// UnlockEvent — финальная структура разлока
type UnlockEvent struct {
	Name      string
	Symbol    string
	Title     string
	Date      time.Time
	DaysLeft  int
	Source    string
	VotesUp   int
	VotesDown int
}

// FetchUpcoming возвращает предстоящие разлоки токенов
func (c *Client) FetchUpcoming() ([]UnlockEvent, error) {
	now := time.Now()
	dateFrom := now.Format("2006-01-02")
	dateTo := now.AddDate(0, 2, 0).Format("2006-01-02") // следующие 2 месяца

	url := fmt.Sprintf(
		"%s/events?max=50&dateRangeStart=%s&dateRangeEnd=%s&categories=Token+Unlock&sortBy=date_event&page=1",
		baseURL, dateFrom, dateTo,
	)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("ошибка запроса: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ошибка сети: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("неверный API-ключ CoinMarketCal. Получите бесплатно на coinmarketcal.com")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("CoinMarketCal вернул статус %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения: %w", err)
	}

	var res apiResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("ошибка парсинга: %w", err)
	}

	var events []UnlockEvent
	for _, e := range res.Body {
		date, err := time.Parse("2006-01-02T15:04:05+00:00", e.DateEvent)
		if err != nil {
			date, err = time.Parse("2006-01-02", e.DateEvent)
			if err != nil {
				continue
			}
		}

		daysLeft := int(time.Until(date).Hours() / 24)
		if daysLeft < 0 {
			continue
		}

		name, symbol := "", ""
		if len(e.Coins) > 0 {
			name = e.Coins[0].FullName
			symbol = strings.ToUpper(e.Coins[0].Symbol)
		}

		events = append(events, UnlockEvent{
			Name:      name,
			Symbol:    symbol,
			Title:     e.Title,
			Date:      date,
			DaysLeft:  daysLeft,
			Source:    e.Source,
			VotesUp:   e.VoteCount.Up,
			VotesDown: e.VoteCount.Down,
		})
	}

	// Сортируем по дате
	sort.Slice(events, func(i, j int) bool {
		return events[i].Date.Before(events[j].Date)
	})

	return events, nil
}

// Format форматирует список разлоков для Telegram
func Format(events []UnlockEvent) string {
	if len(events) == 0 {
		return "📭 Предстоящих разлоков токенов не найдено на ближайшие 2 месяца."
	}

	var sb strings.Builder
	sb.WriteString("🔓 Предстоящие разлоки токенов\n(ближайшие 2 месяца)\n\n")

	for i, e := range events {
		if i >= 15 { // показываем максимум 15
			break
		}

		urgencyEmoji := urgency(e.DaysLeft)
		dateStr := e.Date.Format("02.01.2006")

		coinStr := e.Symbol
		if e.Name != "" && e.Name != e.Symbol {
			coinStr = fmt.Sprintf("%s (%s)", e.Name, e.Symbol)
		}

		sb.WriteString(fmt.Sprintf(
			"%s %s\n   📅 %s — через %d дн.\n   %s\n\n",
			urgencyEmoji,
			coinStr,
			dateStr,
			e.DaysLeft,
			truncate(e.Title, 80),
		))
	}

	sb.WriteString("─────────────────────────\n")
	sb.WriteString("🔴 <7 дней  🟡 <30 дней  🟢 >30 дней\n")
	sb.WriteString("\n⚠️ Крупные разлоки = давление продавцов. Учитывайте в стратегии!")
	return sb.String()
}

func urgency(days int) string {
	switch {
	case days <= 7:
		return "🔴"
	case days <= 30:
		return "🟡"
	default:
		return "🟢"
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
