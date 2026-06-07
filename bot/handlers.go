package bot

import (
	"crypto-bot/bybit"
	"crypto-bot/crypto"
	"crypto-bot/db"
	"crypto-bot/news"
	"crypto-bot/unlocks"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tele "gopkg.in/telebot.v3"
)

const (
	maxSymbolLen    = 20          // макс. длина символа монеты от пользователя
	maxAmount       = 1e15        // макс. количество монет
	maxPrice        = 1e12        // макс. цена покупки
	rateLimitCount  = 5           // запросов...
	rateLimitWindow = time.Minute // ...в минуту на пользователя
)

// ─── Inline-меню ─────────────────────────────────────────────────────────────

var mainMenu = &tele.ReplyMarkup{}

var (
	btnMarket    = mainMenu.Data("📊 Рынок", "cb_market")
	btnRecommend = mainMenu.Data("🎯 Рекомендации", "cb_recommend")
	btnPortfolio = mainMenu.Data("💼 Портфель", "cb_portfolio")
	btnBybit     = mainMenu.Data("💎 Bybit", "cb_bybit")
	btnNews      = mainMenu.Data("📰 Новости китов", "cb_news")
	btnUnlocks   = mainMenu.Data("🔓 Разлоки", "cb_unlocks")
	btnHelp      = mainMenu.Data("❓ Помощь", "cb_help")
)

func init() {
	mainMenu.Inline(
		mainMenu.Row(btnMarket, btnRecommend),
		mainMenu.Row(btnPortfolio, btnBybit),
		mainMenu.Row(btnNews, btnUnlocks),
		mainMenu.Row(btnHelp),
	)
}

// ─── Bot struct ───────────────────────────────────────────────────────────────

type Bot struct {
	tele    *tele.Bot
	db      *db.DB
	crypto  *crypto.Client
	news    *news.Client
	unlocks *unlocks.Client
	limiter *RateLimiter
}

func New(b *tele.Bot, d *db.DB, c *crypto.Client, n *news.Client, u *unlocks.Client) *Bot {
	return &Bot{
		tele:    b,
		db:      d,
		crypto:  c,
		news:    n,
		unlocks: u,
		limiter: NewRateLimiter(rateLimitCount, rateLimitWindow),
	}
}

func (bot *Bot) Register() {
	// Текстовые команды
	bot.tele.Handle("/start", bot.protected(bot.handleStart))
	bot.tele.Handle("/market", bot.protected(bot.handleMarket))
	bot.tele.Handle("/recommend", bot.protected(bot.handleRecommend))
	bot.tele.Handle("/portfolio", bot.protected(bot.handlePortfolio))
	bot.tele.Handle("/add", bot.protected(bot.handleAdd))
	bot.tele.Handle("/remove", bot.protected(bot.handleRemove))
	bot.tele.Handle("/help", bot.protected(bot.handleHelp))
	bot.tele.Handle("/bybit", bot.protected(bot.handleBybit))
	bot.tele.Handle("/news", bot.protected(bot.handleNews))
	bot.tele.Handle("/unlock-tokens", bot.protected(bot.handleUnlockTokens))
	bot.tele.Handle("/unlock", bot.protected(bot.handleUnlockTokens))  // алиас
	bot.tele.Handle("/unlocks", bot.protected(bot.handleUnlockTokens)) // алиас

	// Callback-кнопки inline-меню
	bot.tele.Handle(&btnMarket, bot.protected(bot.handleMarketCallback))
	bot.tele.Handle(&btnRecommend, bot.protected(bot.handleRecommendCallback))
	bot.tele.Handle(&btnPortfolio, bot.protected(bot.handlePortfolioCallback))
	bot.tele.Handle(&btnBybit, bot.protected(bot.handleBybitCallback))
	bot.tele.Handle(&btnNews, bot.protected(bot.handleNewsCallback))
	bot.tele.Handle(&btnUnlocks, bot.protected(bot.handleUnlocksCallback))
	bot.tele.Handle(&btnHelp, bot.protected(bot.handleHelpCallback))
}

// protected — middleware: nil-проверка sender + rate limit
func (bot *Bot) protected(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		if c.Sender() == nil {
			return nil // игнорируем анонимные сообщения
		}
		userID := c.Sender().ID
		if !bot.limiter.Allow(userID) {
			return c.Send("⏳ Слишком много запросов. Подождите минуту.")
		}
		if err := next(c); err != nil {
			// Логируем внутренние ошибки, пользователю — общий ответ
			log.Printf("handler error [user=%d]: %v", userID, err)
		}
		return nil
	}
}

func send(c tele.Context, text string) error {
	return c.Send(text)
}

// sendMenu отправляет текст с inline-меню
func sendMenu(c tele.Context, text string) error {
	return c.Send(text, mainMenu)
}

// ─── Обработчики команд ───────────────────────────────────────────────────────

func (bot *Bot) handleStart(c tele.Context) error {
	return sendMenu(c, "👋 Добро пожаловать в Crypto Bot!\n\nВыберите действие в меню или введите команду вручную:")
}

func (bot *Bot) handleHelp(c tele.Context) error {
	return sendMenu(c, helpText())
}

func helpText() string {
	return `Команды:

📊 /market — топ-20 монет по капитализации
🎯 /recommend — рекомендации к покупке
💼 /portfolio — ваш личный портфель и P&L
➕ /add <символ> <кол-во> <цена> — добавить позицию
❌ /remove <символ> — удалить позицию по названию монеты
💎 /bybit — баланс вашего Bybit аккаунта
📰 /news — новости китов + рекомендации
🔓 /unlock-tokens — предстоящие разлоки токенов

Bybit интеграция:
/bybit connect <api_key> <api_secret> — подключить аккаунт
/bybit disconnect — отключить аккаунт

Примеры:
/add btc 0.5 65000
/remove btc`
}

// ─── Callback-обработчики кнопок ─────────────────────────────────────────────

// callbackAnswer подтверждает нажатие кнопки (убирает "часики" в Telegram)
func callbackAnswer(c tele.Context) {
	_ = c.Respond()
}

func (bot *Bot) handleMarketCallback(c tele.Context) error {
	callbackAnswer(c)
	return bot.handleMarket(c)
}

func (bot *Bot) handleRecommendCallback(c tele.Context) error {
	callbackAnswer(c)
	return bot.handleRecommend(c)
}

func (bot *Bot) handlePortfolioCallback(c tele.Context) error {
	callbackAnswer(c)
	return bot.handlePortfolio(c)
}

func (bot *Bot) handleBybitCallback(c tele.Context) error {
	callbackAnswer(c)
	return bot.handleBybit(c)
}

func (bot *Bot) handleNewsCallback(c tele.Context) error {
	callbackAnswer(c)
	return bot.handleNews(c)
}

func (bot *Bot) handleUnlocksCallback(c tele.Context) error {
	callbackAnswer(c)
	return bot.handleUnlockTokens(c)
}

func (bot *Bot) handleHelpCallback(c tele.Context) error {
	callbackAnswer(c)
	return sendMenu(c, helpText())
}

func (bot *Bot) handleMarket(c tele.Context) error {
	_ = c.Notify(tele.Typing)
	coins, err := bot.crypto.TopCoins(20)
	if err != nil {
		log.Printf("TopCoins error: %v", err)
		return send(c, "❌ Не удалось получить данные рынка. Попробуйте позже.")
	}

	var sb strings.Builder
	sb.WriteString("📊 Топ-20 по капитализации\n\n")
	for i, coin := range coins {
		arrow := "🟢 +"
		if coin.PriceChange24h < 0 {
			arrow = "🔴 "
		}
		sb.WriteString(fmt.Sprintf(
			"%d. %s (%s)\n   $%s  %s%.2f%%\n\n",
			i+1,
			coin.Name,
			strings.ToUpper(coin.Symbol),
			formatPrice(coin.CurrentPrice),
			arrow,
			coin.PriceChange24h,
		))
	}
	return send(c, sb.String())
}

func (bot *Bot) handleRecommend(c tele.Context) error {
	_ = c.Notify(tele.Typing)
	coins, err := bot.crypto.TopCoins(100)
	if err != nil {
		log.Printf("TopCoins error: %v", err)
		return send(c, "❌ Не удалось получить данные рынка. Попробуйте позже.")
	}

	recs := crypto.Recommend(coins)
	if len(recs) == 0 {
		return send(c, "🤷 Сейчас нет явных сигналов к покупке. Рынок нейтрален.")
	}

	var sb strings.Builder
	sb.WriteString("🎯 Рекомендации к покупке\n(топ-100 монет, технический анализ)\n\n")
	for i, r := range recs {
		stars := strings.Repeat("⭐", r.Score)
		sb.WriteString(fmt.Sprintf(
			"%d. %s (%s) — $%s\n%s\n",
			i+1,
			r.Coin.Name,
			strings.ToUpper(r.Coin.Symbol),
			formatPrice(r.Coin.CurrentPrice),
			stars,
		))
		for _, reason := range r.Reasons {
			sb.WriteString("  • " + reason + "\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("⚠️ Это не финансовый совет. DYOR.")
	return send(c, sb.String())
}

func (bot *Bot) handlePortfolio(c tele.Context) error {
	_ = c.Notify(tele.Typing)
	userID := c.Sender().ID

	positions, err := bot.db.GetPortfolio(userID)
	if err != nil {
		log.Printf("GetPortfolio error [user=%d]: %v", userID, err)
		return send(c, "❌ Не удалось загрузить портфель. Попробуйте позже.")
	}
	if len(positions) == 0 {
		return send(c, "💼 Ваш портфель пуст.\n\nДобавьте позицию: /add btc 0.5 65000")
	}

	// Собираем уникальные ID для запроса цен
	ids := make([]string, 0, len(positions))
	seen := map[string]bool{}
	for _, p := range positions {
		sym := strings.ToLower(p.Symbol)
		if !seen[sym] {
			ids = append(ids, sym)
			seen[sym] = true
		}
	}

	prices, err := bot.crypto.PriceByIDs(ids)
	if err != nil {
		log.Printf("PriceByIDs error: %v", err)
		prices = map[string]float64{} // показываем портфель без текущих цен
	}

	var sb strings.Builder
	sb.WriteString("💼 Ваш портфель\n\n")

	totalInvested := 0.0
	totalCurrent := 0.0

	for _, p := range positions {
		sym := strings.ToLower(p.Symbol)
		invested := p.Amount * p.BuyPrice
		totalInvested += invested

		currentPrice, hasCurrent := prices[sym]
		pnlStr := ""
		if hasCurrent && currentPrice > 0 {
			current := p.Amount * currentPrice
			totalCurrent += current
			pnl := current - invested
			pnlPct := (pnl / invested) * 100
			pnlEmoji := "🟢"
			if pnl < 0 {
				pnlEmoji = "🔴"
			}
			pnlStr = fmt.Sprintf("\n  %s P&L: %+.2f$ (%.1f%%)", pnlEmoji, pnl, pnlPct)
		}

		sb.WriteString(fmt.Sprintf(
			"%d. %s (%s)\n  Кол-во: %s\n  Цена входа: $%s\n  Вложено: $%.2f%s\n\n",
			p.ID,
			p.Name,
			strings.ToUpper(p.Symbol),
			formatAmount(p.Amount),
			formatPrice(p.BuyPrice),
			invested,
			pnlStr,
		))
	}

	sb.WriteString(fmt.Sprintf("💰 Итого вложено: $%.2f\n", totalInvested))
	if totalCurrent > 0 {
		totalPnl := totalCurrent - totalInvested
		totalPct := (totalPnl / totalInvested) * 100
		emoji := "🟢"
		if totalPnl < 0 {
			emoji = "🔴"
		}
		sb.WriteString(fmt.Sprintf("📈 Текущая стоимость: $%.2f\n", totalCurrent))
		sb.WriteString(fmt.Sprintf("%s Общий P&L: %+.2f$ (%.1f%%)", emoji, totalPnl, totalPct))
	}

	sb.WriteString("\n\nУдалить позицию: /remove <символ>  (например: /remove btc)")
	return send(c, sb.String())
}

func (bot *Bot) handleAdd(c tele.Context) error {
	args := c.Args()
	if len(args) < 3 {
		return send(c, "Использование: /add <символ> <количество> <цена покупки>\nПример: /add btc 0.5 65000")
	}

	userID := c.Sender().ID

	// Валидация символа
	symbol := strings.ToLower(strings.TrimSpace(args[0]))
	if utf8.RuneCountInString(symbol) > maxSymbolLen {
		return send(c, fmt.Sprintf("❌ Символ монеты слишком длинный (максимум %d символов)", maxSymbolLen))
	}
	if symbol == "" {
		return send(c, "❌ Укажите символ монеты")
	}

	// Валидация количества
	amount, err := strconv.ParseFloat(args[1], 64)
	if err != nil || amount <= 0 {
		return send(c, "❌ Некорректное количество. Пример: 0.5")
	}
	if amount > maxAmount {
		return send(c, "❌ Слишком большое количество")
	}

	// Валидация цены
	buyPrice, err := strconv.ParseFloat(args[2], 64)
	if err != nil || buyPrice <= 0 {
		return send(c, "❌ Некорректная цена. Пример: 65000")
	}
	if buyPrice > maxPrice {
		return send(c, "❌ Слишком большая цена")
	}

	// Ищем монету в CoinGecko для получения официального названия и ID
	_ = c.Notify(tele.Typing)
	coins, err := bot.crypto.TopCoins(250)
	name := strings.ToUpper(symbol)
	coinID := symbol
	if err == nil {
		for _, coin := range coins {
			if strings.EqualFold(coin.Symbol, symbol) {
				name = coin.Name
				coinID = coin.ID
				break
			}
		}
	} else {
		log.Printf("TopCoins error in /add [user=%d]: %v", userID, err)
	}

	if err := bot.db.AddPosition(userID, coinID, name, amount, buyPrice); err != nil {
		return send(c, "❌ "+err.Error())
	}

	invested := amount * buyPrice
	return send(c, fmt.Sprintf(
		"✅ Добавлено в ваш портфель:\n%s x%s по $%s\nВложено: $%.2f",
		name, formatAmount(amount), formatPrice(buyPrice), invested,
	))
}

func (bot *Bot) handleRemove(c tele.Context) error {
	args := c.Args()
	if len(args) < 1 {
		return send(c, "Использование: /remove <символ>\nПример: /remove btc")
	}

	symbol := strings.ToLower(strings.TrimSpace(args[0]))
	if utf8.RuneCountInString(symbol) > maxSymbolLen {
		return send(c, "❌ Слишком длинный символ")
	}

	name, err := bot.db.RemoveBySymbol(c.Sender().ID, symbol)
	if err != nil {
		return send(c, "❌ "+err.Error())
	}
	return send(c, fmt.Sprintf("✅ %s удалён из вашего портфеля.", name))
}

func (bot *Bot) handleBybit(c tele.Context) error {
	args := c.Args()
	userID := c.Sender().ID

	// /bybit connect <key> <secret>
	if len(args) >= 1 && args[0] == "connect" {
		if len(args) < 3 {
			return send(c, "Использование: /bybit connect <api_key> <api_secret>\n\n"+
				"Как получить ключи:\n"+
				"1. bybit.com → Профиль → API Management\n"+
				"2. Create New Key → System-generated\n"+
				"3. Permissions: Read-Only → Unified Trading\n"+
				"4. Скопируйте API Key и Secret")
		}

		apiKey := strings.TrimSpace(args[1])
		apiSecret := strings.TrimSpace(args[2])

		// Валидация длины ключей
		if len(apiKey) < 10 || len(apiSecret) < 10 {
			return send(c, "❌ Некорректные ключи. Проверьте правильность копирования.")
		}
		if len(apiKey) > 100 || len(apiSecret) > 100 {
			return send(c, "❌ Слишком длинные ключи.")
		}

		// Проверяем что ключи рабочие перед сохранением
		_ = c.Notify(tele.Typing)
		client := bybit.NewClient(apiKey, apiSecret)
		if err := client.Validate(); err != nil {
			log.Printf("Bybit validate error [user=%d]: %v", userID, err)
			return send(c, "❌ Не удалось подключиться к Bybit. Проверьте ключи и разрешения.\n\nОшибка: "+err.Error())
		}

		if err := bot.db.SaveBybitKeys(userID, apiKey, apiSecret); err != nil {
			log.Printf("SaveBybitKeys error [user=%d]: %v", userID, err)
			return send(c, "❌ Не удалось сохранить ключи. Попробуйте позже.")
		}

		return send(c, "✅ Bybit аккаунт успешно подключён!\n\nТеперь используйте /bybit для просмотра баланса.")
	}

	// /bybit disconnect
	if len(args) >= 1 && args[0] == "disconnect" {
		if err := bot.db.DeleteBybitKeys(userID); err != nil {
			return send(c, "❌ "+err.Error())
		}
		return send(c, "✅ Bybit аккаунт отключён.")
	}

	// /bybit — показать баланс
	_ = c.Notify(tele.Typing)
	apiKey, apiSecret, err := bot.db.GetBybitKeys(userID)
	if err != nil {
		log.Printf("GetBybitKeys error [user=%d]: %v", userID, err)
		return send(c, "❌ Ошибка загрузки ключей. Попробуйте позже.")
	}
	if apiKey == "" {
		return send(c, "💎 Bybit аккаунт не подключён.\n\nПодключите командой:\n/bybit connect <api_key> <api_secret>")
	}

	client := bybit.NewClient(apiKey, apiSecret)
	wb, err := client.GetWalletBalance()
	if err != nil {
		log.Printf("GetWalletBalance error [user=%d]: %v", userID, err)
		return send(c, "❌ Не удалось получить баланс Bybit.\n\nВозможные причины:\n• Истёк срок действия ключей\n• Нет разрешения на чтение\n• Проблемы с сетью")
	}

	return send(c, bybit.FormatBalance(wb))
}

func (bot *Bot) handleNews(c tele.Context) error {
	_ = c.Notify(tele.Typing)

	articles, err := bot.news.FetchLatest()
	if err != nil {
		log.Printf("news fetch error: %v", err)
		return send(c, "❌ Не удалось загрузить новости. Попробуйте позже.")
	}

	signals := news.Analyze(articles)
	overall := news.OverallRecommendation(signals)
	formatted := news.FormatSignals(signals)

	var sb strings.Builder
	sb.WriteString("📰 Новости китов и крупных держателей\n\n")

	if formatted != "" {
		sb.WriteString(formatted)
		sb.WriteString("─────────────────────────\n")
	}

	sb.WriteString("📊 Итоговая оценка:\n")
	sb.WriteString(overall)
	sb.WriteString("\n\n⚠️ Это не финансовый совет. DYOR.")

	// Telegram лимит 4096 символов — режем если нужно
	msg := sb.String()
	if len(msg) > 4000 {
		msg = msg[:4000] + "\n...(сокращено)"
	}
	return send(c, msg)
}

func (bot *Bot) handleUnlockTokens(c tele.Context) error {
	_ = c.Notify(tele.Typing)

	risks, err := bot.unlocks.FetchHighRisk()
	if err != nil {
		log.Printf("unlock-tokens error: %v", err)
		return send(c, "❌ Не удалось загрузить данные. Попробуйте позже.")
	}

	msg := unlocks.Format(risks)
	if len(msg) > 4000 {
		msg = msg[:3990] + "\n...(сокращено)"
	}
	return send(c, msg)
}

// helpers

func formatPrice(p float64) string {
	switch {
	case p >= 1000:
		return fmt.Sprintf("%.2f", p)
	case p >= 1:
		return fmt.Sprintf("%.4f", p)
	default:
		return fmt.Sprintf("%.8f", p)
	}
}

func formatAmount(a float64) string {
	if a == float64(int64(a)) {
		return fmt.Sprintf("%.0f", a)
	}
	return strconv.FormatFloat(a, 'f', -1, 64)
}
