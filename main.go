package main

import (
	"crypto-bot/bot"
	"crypto-bot/crypto"
	"crypto-bot/db"
	"crypto-bot/news"
	"crypto-bot/unlocks"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	tele "gopkg.in/telebot.v3"
	"gopkg.in/telebot.v3/middleware"
)

func main() {
	_ = godotenv.Load()

	token := mustEnv("BOT_TOKEN")
	dbPath := getEnv("DB_PATH", "portfolio.db")

	database, err := db.New(dbPath)
	if err != nil {
		log.Fatalf("БД: %v", err)
	}
	log.Println("✅ SQLite база данных готова:", dbPath)

	pref := tele.Settings{
		Token:  token,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	}
	b, err := tele.NewBot(pref)
	if err != nil {
		log.Fatalf("Telegram: %v", err)
	}
	log.Printf("✅ Бот запущен: @%s", b.Me.Username)

	// Белый список пользователей
	allowedIDs := parseIDs(os.Getenv("ALLOWED_USERS"))
	if len(allowedIDs) > 0 {
		b.Use(middleware.Whitelist(allowedIDs...))
		log.Printf("🔒 Доступ разрешён для %d пользователей: %v", len(allowedIDs), allowedIDs)
	} else {
		log.Println("⚠️  ALLOWED_USERS не задан — бот доступен всем!")
	}

	// Новости (бесплатно, без ключа)
	newsClient := news.NewClient()
	log.Println("✅ Модуль новостей готов")

	// Разлоки токенов — DeFiLlama (бесплатно, без ключей)
	unlocksClient := unlocks.NewClient()
	log.Println("✅ Модуль разлоков токенов готов (DeFiLlama)")

	cryptoClient := crypto.NewClient()
	handler := bot.New(b, database, cryptoClient, newsClient, unlocksClient)
	handler.Register()

	b.Start()
}

func parseIDs(raw string) []int64 {
	var ids []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			log.Printf("⚠️  Некорректный ID в ALLOWED_USERS: %q", part)
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("Переменная окружения %s не задана", key)
	}
	return v
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
