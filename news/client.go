package news

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Источники крипто-новостей (RSS — бесплатно, без ключей)
var feeds = []feedSource{
	{Name: "Cointelegraph", URL: "https://cointelegraph.com/rss"},
	{Name: "Decrypt", URL: "https://decrypt.co/feed"},
	{Name: "CoinDesk", URL: "https://www.coindesk.com/arc/outboundfeeds/rss/?outputType=xml"},
	{Name: "Bitcoin Magazine", URL: "https://bitcoinmagazine.com/.rss/full/"},
}

const maxBodyBytes = 5 * 1024 * 1024

type feedSource struct {
	Name string
	URL  string
}

type Article struct {
	Title       string
	Description string
	URL         string
	Source      string
	PublishedAt time.Time
}

type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{
		http: &http.Client{
			Timeout: 12 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
	}
}

// ─── RSS-парсинг ──────────────────────────────────────────────────────────

type rssFeed struct {
	XMLName xml.Name  `xml:"rss"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Items []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
}

// FetchLatest параллельно тянет новости из всех источников
func (c *Client) FetchLatest() ([]Article, error) {
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		all      []Article
		errCount int
	)

	for _, f := range feeds {
		wg.Add(1)
		go func(src feedSource) {
			defer wg.Done()
			articles, err := c.fetchOne(src)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errCount++
				return
			}
			all = append(all, articles...)
		}(f)
	}
	wg.Wait()

	if len(all) == 0 {
		return nil, fmt.Errorf("все источники новостей недоступны (%d ошибок)", errCount)
	}

	// Сортируем по дате — самые свежие первые
	sort.Slice(all, func(i, j int) bool {
		return all[i].PublishedAt.After(all[j].PublishedAt)
	})

	// Ограничиваем 60 самыми свежими
	if len(all) > 60 {
		all = all[:60]
	}

	return all, nil
}

func (c *Client) fetchOne(src feedSource) ([]Article, error) {
	req, err := http.NewRequest("GET", src.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/rss+xml, application/xml, text/xml")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; crypto-bot/1.0)")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("статус %d от %s", resp.StatusCode, src.Name)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, err
	}

	var feed rssFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("парсинг XML %s: %w", src.Name, err)
	}

	var articles []Article
	for _, item := range feed.Channel.Items {
		pubTime := parsePubDate(item.PubDate)
		// Игнорируем статьи старше 7 дней
		if time.Since(pubTime) > 7*24*time.Hour {
			continue
		}
		articles = append(articles, Article{
			Title:       cleanText(item.Title),
			Description: cleanText(item.Description),
			URL:         strings.TrimSpace(item.Link),
			Source:      src.Name,
			PublishedAt: pubTime,
		})
	}
	return articles, nil
}

var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

func cleanText(s string) string {
	s = htmlTagRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	return strings.TrimSpace(s)
}

func parsePubDate(s string) time.Time {
	formats := []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC822Z,
		time.RFC822,
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05-07:00",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, strings.TrimSpace(s)); err == nil {
			return t
		}
	}
	return time.Now()
}

// ─── Анализ китовых сигналов ───────────────────────────────────────────────

type Signal struct {
	Article    Article
	WhaleScore int
	Sentiment  int
	Label      string
}

var bullishKeywords = []string{
	"whale buying", "whale accumulation", "whales accumulate", "whales buy",
	"institutional buying", "large purchase", "massive buy", "billion-dollar buy",
	"bullish", "accumulate", "inflow", "buy the dip", "spot etf inflow",
	"record inflow", "etf inflow", "treasury purchase", "smart money",
	"long position", "leverage long", "bullish bet", "moves to wallet",
	"withdraw from exchange", "exchange outflow", "hodl",
}

var bearishKeywords = []string{
	"whale selling", "whale dump", "whale transfer to exchange",
	"large withdrawal", "exchange inflow", "sell-off", "selling pressure",
	"bearish", "dump", "outflow from etf", "panic sell", "etf outflow",
	"liquidation", "massive sell", "exchange deposit", "moves to exchange",
	"deposit to exchange", "short position", "leverage short",
	"unlock pressure", "selling pressure", "capitulation",
}

var whaleKeywords = []string{
	"whale", "whales", "large holder", "big player", "mega whale",
	"institutional", "billion", "million btc", "million eth", "million in",
	"on-chain", "wallet", "cold wallet", "exchange transfer", "movement",
	"satoshi-era", "satoshi era", "unlock", "vesting", "large transaction",
	"dormant", "moved", "transferred", "long-term holder", "lth",
	"micro strategy", "microstrategy", "blackrock", "fidelity", "grayscale",
	"etf", "spot etf", "treasury", "fund", "hedge fund",
}

// Analyze фильтрует и оценивает статьи про китов
func Analyze(articles []Article) []Signal {
	var signals []Signal

	for _, a := range articles {
		text := strings.ToLower(a.Title + " " + a.Description)

		whaleScore := countKeywords(text, whaleKeywords)
		if whaleScore == 0 {
			continue
		}

		bullScore := countKeywords(text, bullishKeywords)
		bearScore := countKeywords(text, bearishKeywords)
		sentiment := bullScore - bearScore

		label := "🟡 HOLD"
		switch {
		case sentiment > 0:
			label = "🟢 BUY"
		case sentiment < 0:
			label = "🔴 SELL"
		}

		signals = append(signals, Signal{
			Article:    a,
			WhaleScore: whaleScore,
			Sentiment:  sentiment,
			Label:      label,
		})
	}

	// Сортировка: сначала по whaleScore, потом по новизне
	sort.Slice(signals, func(i, j int) bool {
		if signals[i].WhaleScore != signals[j].WhaleScore {
			return signals[i].WhaleScore > signals[j].WhaleScore
		}
		return signals[i].Article.PublishedAt.After(signals[j].Article.PublishedAt)
	})

	if len(signals) > 7 {
		signals = signals[:7]
	}
	return signals
}

func OverallRecommendation(signals []Signal) string {
	if len(signals) == 0 {
		return "🤷 Китовой активности в новостях не обнаружено. Рынок спокоен."
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
		return ""
	}

	var sb strings.Builder
	for i, s := range signals {
		t := s.Article.PublishedAt.UTC().Format("02.01 15:04")
		title := s.Article.Title
		if len(title) > 95 {
			title = title[:92] + "..."
		}
		sb.WriteString(fmt.Sprintf(
			"%d. %s  [%s]\n   📰 %s | 🕒 %s UTC\n   🐋 %s\n   🔗 %s\n\n",
			i+1, title, s.Label,
			s.Article.Source, t,
			whaleScoreBar(s.WhaleScore),
			s.Article.URL,
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
	switch {
	case score >= 5:
		return "█████ высокая активность"
	case score >= 3:
		return "███░░ средняя активность"
	default:
		return "█░░░░ низкая активность"
	}
}
