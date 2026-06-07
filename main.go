package main

import (
	"crypto-bot/bot"
	"crypto-bot/crypto"
	"crypto-bot/db"
	"log"
	"os"
	"strconv"
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

	// Ограничение по owner (если задан)
	ownerIDStr := os.Getenv("OWNER_ID")
	if ownerIDStr != "" {
		ownerID, err := strconv.ParseInt(ownerIDStr, 10, 64)
		if err != nil {
			log.Fatalf("OWNER_ID должен быть числом: %v", err)
		}
		b.Use(middleware.Whitelist(ownerID))
		log.Printf("🔒 Доступ ограничен: owner ID %d", ownerID)
	} else {
		log.Println("⚠️  OWNER_ID не задан — бот доступен всем. Добавьте OWNER_ID в .env!")
	}

	cryptoClient := crypto.NewClient()
	handler := bot.New(b, database, cryptoClient)
	handler.Register()

	b.Start()
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
