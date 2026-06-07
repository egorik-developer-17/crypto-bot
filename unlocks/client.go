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

// DeFiLlama Emissions API — бесплатно, без регистрации и ключей
const (
	listURL      = "https://defillama-datasets.llama.fi/emissionsProtocolsList"
	overviewURL  = "https://defillama-datasets.llama.fi/emissionsProtocolOverview/%s.json"
	maxBodyBytes = 5 * 1024 * 1024
	maxProtocols = 40 // проверяем топ-40 протоколов
)

type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

// UnlockEvent — одно событие разлока
type UnlockEvent struct {
	Protocol  string
	Label     string  // название токена/проекта
	Date      time.Time
	DaysLeft  int
	Amount    float64 // кол-во токенов
	USDValue  float64 // ~стоимость в USD (если есть)
	EventType string  // cliff, linear, etc.
}

// ─── Модели DeFiLlama ─────────────────────────────────────────────────────

type protocolOverview struct {
	Name           string           `json:"name"`
	Symbol         string           `json:"symbol"`
	CoinGeckoID    string           `json:"coinGeckoId"`
	Unlockschedule []unlockSchedule `json:"unlockSchedule"`
	Price          float64          `json:"price"`
}

type unlockSchedule struct {
	Timestamp int64   `json:"timestamp"`
	Amount    float64 `json:"amount"`
	NoOfCoins float64 `json:"noOfCoins"`
}

// ─── Получение данных ────────────────────────────────────────────────────

func (c *Client) fetchJSON(url string, out any) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "crypto-bot/1.0")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("статус %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// FetchUpcoming возвращает предстоящие разлоки из DeFiLlama
func (c *Client) FetchUpcoming() ([]UnlockEvent, error) {
	// Шаг 1: получаем список протоколов
	var protocols []string
	if err := c.fetchJSON(listURL, &protocols); err != nil {
		return nil, fmt.Errorf("не удалось получить список протоколов: %w", err)
	}

	if len(protocols) > maxProtocols {
		protocols = protocols[:maxProtocols]
	}

	now := time.Now()
	horizon := now.AddDate(0, 2, 0) // следующие 2 месяца

	var events []UnlockEvent

	// Шаг 2: для каждого протокола смотрим расписание разлоков
	for _, slug := range protocols {
		var overview protocolOverview
		url := fmt.Sprintf(overviewURL, slug)
		if err := c.fetchJSON(url, &overview); err != nil {
			continue // пропускаем ошибочные
		}

		for _, sched := range overview.Unlockschedule {
			eventTime := time.Unix(sched.Timestamp, 0).UTC()

			// Только будущие события в горизонте 2 месяца
			if eventTime.Before(now) || eventTime.After(horizon) {
				continue
			}

			amount := sched.NoOfCoins
			if amount == 0 {
				amount = sched.Amount
			}

			usdValue := 0.0
			if overview.Price > 0 {
				usdValue = amount * overview.Price
			}

			label := overview.Name
			if overview.Symbol != "" {
				label = fmt.Sprintf("%s (%s)", overview.Name, strings.ToUpper(overview.Symbol))
			}

			events = append(events, UnlockEvent{
				Protocol:  slug,
				Label:     label,
				Date:      eventTime,
				DaysLeft:  int(time.Until(eventTime).Hours() / 24),
				Amount:    amount,
				USDValue:  usdValue,
				EventType: "unlock",
			})
		}
	}

	// Сортируем по дате
	sort.Slice(events, func(i, j int) bool {
		return events[i].Date.Before(events[j].Date)
	})

	// Убираем дубли (один проект может иметь несколько событий)
	events = deduplicate(events)

	return events, nil
}

// deduplicate оставляет для каждого протокола только ближайшее событие
func deduplicate(events []UnlockEvent) []UnlockEvent {
	seen := map[string]bool{}
	var result []UnlockEvent
	for _, e := range events {
		if !seen[e.Protocol] {
			seen[e.Protocol] = true
			result = append(result, e)
		}
	}
	return result
}

// ─── Форматирование ───────────────────────────────────────────────────────

func Format(events []UnlockEvent) string {
	if len(events) == 0 {
		return "📭 Предстоящих разлоков токенов не найдено на ближайшие 2 месяца."
	}

	var sb strings.Builder
	sb.WriteString("🔓 Предстоящие разлоки токенов\n")
	sb.WriteString("(источник: DeFiLlama • ближайшие 2 месяца)\n\n")

	limit := 15
	if len(events) < limit {
		limit = len(events)
	}

	for i := 0; i < limit; i++ {
		e := events[i]
		emoji := urgencyEmoji(e.DaysLeft)
		dateStr := e.Date.Format("02.01.2006")

		line := fmt.Sprintf("%s %s\n   📅 %s — через %d дн.\n",
			emoji, e.Label, dateStr, e.DaysLeft)

		// Объём разлока
		if e.Amount > 0 {
			line += fmt.Sprintf("   💰 Объём: %s токенов", formatLargeNumber(e.Amount))
			if e.USDValue > 0 {
				line += fmt.Sprintf(" (~$%s)", formatLargeNumber(e.USDValue))
			}
			line += "\n"
		}

		// Предупреждение о давлении продавцов
		if e.USDValue > 50_000_000 { // >$50M — крупный разлок
			line += "   ⚠️ Крупный разлок — возможное давление продавцов!\n"
		}

		sb.WriteString(line + "\n")
	}

	sb.WriteString("─────────────────────────\n")
	sb.WriteString("🔴 <7 дн.  🟡 <30 дн.  🟢 >30 дн.\n")
	sb.WriteString("⚠️ Крупные разлоки = риск коррекции. Учитывайте в стратегии!")
	return sb.String()
}

func urgencyEmoji(days int) string {
	switch {
	case days <= 7:
		return "🔴"
	case days <= 30:
		return "🟡"
	default:
		return "🟢"
	}
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
