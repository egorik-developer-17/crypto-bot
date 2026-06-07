package bot

import (
	"crypto-bot/crypto"
	"crypto-bot/db"
	"fmt"
	"strconv"
	"strings"

	tele "gopkg.in/telebot.v3"
)

type Bot struct {
	tele   *tele.Bot
	db     *db.DB
	crypto *crypto.Client
}

func New(b *tele.Bot, d *db.DB, c *crypto.Client) *Bot {
	return &Bot{tele: b, db: d, crypto: c}
}

func (bot *Bot) Register() {
	bot.tele.Handle("/start", bot.handleStart)
	bot.tele.Handle("/market", bot.handleMarket)
	bot.tele.Handle("/recommend", bot.handleRecommend)
	bot.tele.Handle("/portfolio", bot.handlePortfolio)
	bot.tele.Handle("/add", bot.handleAdd)
	bot.tele.Handle("/remove", bot.handleRemove)
	bot.tele.Handle("/help", bot.handleHelp)
}

func send(c tele.Context, text string) error {
	return c.Send(text)
}

func (bot *Bot) handleStart(c tele.Context) error {
	return send(c, "👋 Crypto Bot запущен!\n\n"+helpText())
}

func (bot *Bot) handleHelp(c tele.Context) error {
	return send(c, helpText())
}

func helpText() string {
	return `Команды:

📊 /market — топ-20 монет по капитализации
🎯 /recommend — рекомендации к покупке
💼 /portfolio — ваш портфель и P&L
➕ /add <символ> <кол-во> <цена> — добавить позицию
❌ /remove <ID> — удалить позицию по ID

Пример: /add btc 0.5 65000`
}

func (bot *Bot) handleMarket(c tele.Context) error {
	_ = c.Notify(tele.Typing)
	coins, err := bot.crypto.TopCoins(20)
	if err != nil {
		return send(c, "❌ Ошибка получения данных: "+err.Error())
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
		return send(c, "❌ Ошибка получения данных: "+err.Error())
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
	positions, err := bot.db.GetPortfolio()
	if err != nil {
		return send(c, "❌ Ошибка БД: "+err.Error())
	}
	if len(positions) == 0 {
		return send(c, "💼 Портфель пуст.\n\nДобавьте позицию: /add btc 0.5 65000")
	}

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
		prices = map[string]float64{}
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
			"[#%d] %s (%s)\n  Кол-во: %s\n  Цена входа: $%s\n  Вложено: $%.2f%s\n\n",
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

	sb.WriteString("\n\nУдалить позицию: /remove <ID>")
	return send(c, sb.String())
}

func (bot *Bot) handleAdd(c tele.Context) error {
	args := c.Args()
	if len(args) < 3 {
		return send(c, "Использование: /add <символ> <количество> <цена покупки>\nПример: /add btc 0.5 65000")
	}

	symbol := strings.ToLower(args[0])
	amount, err := strconv.ParseFloat(args[1], 64)
	if err != nil || amount <= 0 {
		return send(c, "❌ Некорректное количество: "+args[1])
	}
	buyPrice, err := strconv.ParseFloat(args[2], 64)
	if err != nil || buyPrice <= 0 {
		return send(c, "❌ Некорректная цена: "+args[2])
	}

	// Ищем монету в CoinGecko для получения названия и ID
	coins, err := bot.crypto.TopCoins(250)
	name := strings.ToUpper(symbol)
	if err == nil {
		for _, coin := range coins {
			if strings.EqualFold(coin.Symbol, symbol) {
				name = coin.Name
				symbol = coin.ID
				break
			}
		}
	}

	if err := bot.db.AddPosition(symbol, name, amount, buyPrice); err != nil {
		return send(c, "❌ Ошибка сохранения: "+err.Error())
	}

	invested := amount * buyPrice
	return send(c, fmt.Sprintf(
		"✅ Добавлено в портфель:\n%s x%s по $%s\nВложено: $%.2f",
		name, formatAmount(amount), formatPrice(buyPrice), invested,
	))
}

func (bot *Bot) handleRemove(c tele.Context) error {
	args := c.Args()
	if len(args) < 1 {
		return send(c, "Использование: /remove <ID>\nID видно в /portfolio")
	}
	id, err := strconv.Atoi(args[0])
	if err != nil || id <= 0 {
		return send(c, "❌ Некорректный ID: "+args[0])
	}
	if err := bot.db.RemovePosition(id); err != nil {
		return send(c, "❌ "+err.Error())
	}
	return send(c, fmt.Sprintf("✅ Позиция #%d удалена из портфеля.", id))
}

// helpers

func formatPrice(p float64) string {
	if p >= 1000 {
		return fmt.Sprintf("%.2f", p)
	} else if p >= 1 {
		return fmt.Sprintf("%.4f", p)
	}
	return fmt.Sprintf("%.8f", p)
}

func formatAmount(a float64) string {
	if a == float64(int64(a)) {
		return fmt.Sprintf("%.0f", a)
	}
	return strconv.FormatFloat(a, 'f', -1, 64)
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
