package webapp

import (
	"context"
	"crypto-bot/bybit"
	"crypto-bot/crypto"
	"crypto-bot/db"
	"crypto-bot/news"
	"crypto-bot/unlocks"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed static
var staticFS embed.FS

const (
	maxBodyBytes    = 64 * 1024 // 64KB — формы маленькие
	maxSymbolLen    = 20
	maxAmount       = 1e15
	maxPrice        = 1e12
	rateLimitCount  = 30
	rateLimitWindow = time.Minute
)

type ctxKey string

const userCtxKey ctxKey = "tg_user"

// Server — HTTP API для Mini App
type Server struct {
	addr        string
	botToken    string
	allowedIDs  map[int64]bool
	db          *db.DB
	crypto      *crypto.Client
	news        *news.Client
	unlocks     *unlocks.Client

	// Простой rate-limiter на пользователя
	rlMu    sync.Mutex
	rlUsers map[int64][]time.Time
}

type Config struct {
	Addr       string
	BotToken   string
	AllowedIDs []int64
	DB         *db.DB
	Crypto     *crypto.Client
	News       *news.Client
	Unlocks    *unlocks.Client
}

func NewServer(cfg Config) *Server {
	allowed := make(map[int64]bool, len(cfg.AllowedIDs))
	for _, id := range cfg.AllowedIDs {
		allowed[id] = true
	}
	return &Server{
		addr:       cfg.Addr,
		botToken:   cfg.BotToken,
		allowedIDs: allowed,
		db:         cfg.DB,
		crypto:     cfg.Crypto,
		news:       cfg.News,
		unlocks:    cfg.Unlocks,
		rlUsers:    make(map[int64][]time.Time),
	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()

	// Статика (HTML/JS/CSS из embed)
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return err
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	// API endpoints — все требуют валидной initData
	mux.Handle("/api/market", s.auth(s.handleMarket))
	mux.Handle("/api/recommend", s.auth(s.handleRecommend))
	mux.Handle("/api/portfolio", s.auth(s.handlePortfolio))
	mux.Handle("/api/portfolio/add", s.auth(s.handleAddPosition))
	mux.Handle("/api/portfolio/remove", s.auth(s.handleRemovePosition))
	mux.Handle("/api/news", s.auth(s.handleNews))
	mux.Handle("/api/unlocks", s.auth(s.handleUnlocks))
	mux.Handle("/api/bybit", s.auth(s.handleBybit))
	mux.Handle("/api/bybit/connect", s.auth(s.handleBybitConnect))
	mux.Handle("/api/bybit/disconnect", s.auth(s.handleBybitDisconnect))

	srv := &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Printf("🌐 Mini App сервер слушает на %s", s.addr)
	return srv.ListenAndServe()
}

// ─── Middleware ──────────────────────────────────────────────────────────────

func (s *Server) auth(next func(http.ResponseWriter, *http.Request, *TelegramUser)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// initData передаётся в заголовке (отправляется фронтом)
		initData := r.Header.Get("X-Telegram-Init-Data")
		if initData == "" {
			writeError(w, http.StatusUnauthorized, "не авторизован")
			return
		}

		user, err := ValidateInitData(initData, s.botToken)
		if err != nil {
			log.Printf("auth fail: %v", err)
			writeError(w, http.StatusUnauthorized, "неверная авторизация")
			return
		}

		// Whitelist
		if len(s.allowedIDs) > 0 && !s.allowedIDs[user.ID] {
			writeError(w, http.StatusForbidden, "доступ запрещён")
			return
		}

		// Rate limit
		if !s.allowRequest(user.ID) {
			writeError(w, http.StatusTooManyRequests, "слишком много запросов")
			return
		}

		ctx := context.WithValue(r.Context(), userCtxKey, user)
		next(w, r.WithContext(ctx), user)
	})
}

func (s *Server) allowRequest(userID int64) bool {
	s.rlMu.Lock()
	defer s.rlMu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rateLimitWindow)
	hits := s.rlUsers[userID]
	// Фильтруем устаревшие
	fresh := hits[:0]
	for _, t := range hits {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) >= rateLimitCount {
		s.rlUsers[userID] = fresh
		return false
	}
	s.rlUsers[userID] = append(fresh, now)
	return true
}

// ─── Хендлеры ────────────────────────────────────────────────────────────────

func (s *Server) handleMarket(w http.ResponseWriter, r *http.Request, _ *TelegramUser) {
	coins, err := s.crypto.TopCoins(20)
	if err != nil {
		log.Printf("market: %v", err)
		writeError(w, http.StatusBadGateway, "не удалось загрузить рынок")
		return
	}
	writeJSON(w, map[string]any{"coins": coins})
}

func (s *Server) handleRecommend(w http.ResponseWriter, r *http.Request, _ *TelegramUser) {
	coins, err := s.crypto.TopCoins(100)
	if err != nil {
		log.Printf("recommend: %v", err)
		writeError(w, http.StatusBadGateway, "не удалось загрузить рынок")
		return
	}
	recs := crypto.Recommend(coins)
	writeJSON(w, map[string]any{"recommendations": recs})
}

type positionView struct {
	ID            int     `json:"id"`
	Symbol        string  `json:"symbol"`
	Name          string  `json:"name"`
	Amount        float64 `json:"amount"`
	BuyPrice      float64 `json:"buy_price"`
	CurrentPrice  float64 `json:"current_price"`
	Invested      float64 `json:"invested"`
	CurrentValue  float64 `json:"current_value"`
	PnL           float64 `json:"pnl"`
	PnLPercent    float64 `json:"pnl_percent"`
	HasCurrent    bool    `json:"has_current"`
}

func (s *Server) handlePortfolio(w http.ResponseWriter, r *http.Request, u *TelegramUser) {
	positions, err := s.db.GetPortfolio(u.ID)
	if err != nil {
		log.Printf("portfolio: %v", err)
		writeError(w, http.StatusInternalServerError, "не удалось загрузить портфель")
		return
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

	prices, err := s.crypto.PriceByIDs(ids)
	if err != nil {
		log.Printf("prices: %v", err)
		prices = map[string]float64{}
	}

	var (
		views          = make([]positionView, 0, len(positions))
		totalInvested  float64
		totalCurrent   float64
		anyHasCurrent  bool
	)
	for _, p := range positions {
		sym := strings.ToLower(p.Symbol)
		invested := p.Amount * p.BuyPrice
		totalInvested += invested

		v := positionView{
			ID:       p.ID,
			Symbol:   p.Symbol,
			Name:     p.Name,
			Amount:   p.Amount,
			BuyPrice: p.BuyPrice,
			Invested: invested,
		}
		if cp, ok := prices[sym]; ok && cp > 0 {
			anyHasCurrent = true
			current := p.Amount * cp
			totalCurrent += current
			v.CurrentPrice = cp
			v.CurrentValue = current
			v.PnL = current - invested
			if invested > 0 {
				v.PnLPercent = (v.PnL / invested) * 100
			}
			v.HasCurrent = true
		}
		views = append(views, v)
	}

	result := map[string]any{
		"positions":      views,
		"total_invested": totalInvested,
		"has_current":    anyHasCurrent,
	}
	if anyHasCurrent {
		totalPnL := totalCurrent - totalInvested
		totalPct := 0.0
		if totalInvested > 0 {
			totalPct = (totalPnL / totalInvested) * 100
		}
		result["total_current"] = totalCurrent
		result["total_pnl"] = totalPnL
		result["total_pnl_percent"] = totalPct
	}
	writeJSON(w, result)
}

type addPositionReq struct {
	Symbol   string  `json:"symbol"`
	Amount   float64 `json:"amount"`
	BuyPrice float64 `json:"buy_price"`
}

func (s *Server) handleAddPosition(w http.ResponseWriter, r *http.Request, u *TelegramUser) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req addPositionReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	symbol := strings.ToLower(strings.TrimSpace(req.Symbol))
	if symbol == "" || len(symbol) > maxSymbolLen {
		writeError(w, http.StatusBadRequest, "некорректный символ")
		return
	}
	if req.Amount <= 0 || req.Amount > maxAmount {
		writeError(w, http.StatusBadRequest, "некорректное количество")
		return
	}
	if req.BuyPrice <= 0 || req.BuyPrice > maxPrice {
		writeError(w, http.StatusBadRequest, "некорректная цена")
		return
	}

	// Ищем точное название и id монеты
	name := strings.ToUpper(symbol)
	coinID := symbol
	if coins, err := s.crypto.TopCoins(250); err == nil {
		for _, c := range coins {
			if strings.EqualFold(c.Symbol, symbol) {
				name = c.Name
				coinID = c.ID
				break
			}
		}
	}

	if err := s.db.AddPosition(u.ID, coinID, name, req.Amount, req.BuyPrice); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, map[string]any{
		"ok":       true,
		"name":     name,
		"symbol":   coinID,
		"invested": req.Amount * req.BuyPrice,
	})
}

type removePositionReq struct {
	Symbol string `json:"symbol"`
}

func (s *Server) handleRemovePosition(w http.ResponseWriter, r *http.Request, u *TelegramUser) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req removePositionReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	symbol := strings.TrimSpace(req.Symbol)
	if symbol == "" || len(symbol) > maxSymbolLen {
		writeError(w, http.StatusBadRequest, "некорректный символ")
		return
	}
	name, err := s.db.RemoveBySymbol(u.ID, symbol)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "name": name})
}

func (s *Server) handleNews(w http.ResponseWriter, r *http.Request, _ *TelegramUser) {
	articles, err := s.news.FetchLatest()
	if err != nil {
		log.Printf("news: %v", err)
		writeError(w, http.StatusBadGateway, "не удалось загрузить новости")
		return
	}
	signals := news.Analyze(articles)
	overall := news.OverallRecommendation(signals)

	writeJSON(w, map[string]any{
		"signals": signals,
		"overall": overall,
	})
}

func (s *Server) handleUnlocks(w http.ResponseWriter, r *http.Request, _ *TelegramUser) {
	risks, err := s.unlocks.FetchHighRisk()
	if err != nil {
		log.Printf("unlocks: %v", err)
		writeError(w, http.StatusBadGateway, "не удалось загрузить данные")
		return
	}
	if len(risks) > 12 {
		risks = risks[:12]
	}
	schedule := s.unlocks.FetchSchedule(risks)
	writeJSON(w, map[string]any{
		"risks":    risks,
		"schedule": schedule,
	})
}

type bybitConnectReq struct {
	APIKey    string `json:"api_key"`
	APISecret string `json:"api_secret"`
}

func (s *Server) handleBybit(w http.ResponseWriter, r *http.Request, u *TelegramUser) {
	apiKey, apiSecret, err := s.db.GetBybitKeys(u.ID)
	if err != nil {
		log.Printf("bybit keys: %v", err)
		writeError(w, http.StatusInternalServerError, "ошибка загрузки ключей")
		return
	}
	if apiKey == "" {
		writeJSON(w, map[string]any{"connected": false})
		return
	}
	client := bybit.NewClient(apiKey, apiSecret)
	wb, err := client.GetWalletBalance()
	if err != nil {
		log.Printf("bybit balance: %v", err)
		writeError(w, http.StatusBadGateway, "не удалось получить баланс")
		return
	}
	writeJSON(w, map[string]any{
		"connected": true,
		"balance":   wb,
	})
}

func (s *Server) handleBybitConnect(w http.ResponseWriter, r *http.Request, u *TelegramUser) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req bybitConnectReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.APIKey = strings.TrimSpace(req.APIKey)
	req.APISecret = strings.TrimSpace(req.APISecret)
	if len(req.APIKey) < 10 || len(req.APIKey) > 100 ||
		len(req.APISecret) < 10 || len(req.APISecret) > 100 {
		writeError(w, http.StatusBadRequest, "некорректные ключи")
		return
	}

	client := bybit.NewClient(req.APIKey, req.APISecret)
	if err := client.Validate(); err != nil {
		log.Printf("bybit validate [%d]: %v", u.ID, err)
		writeError(w, http.StatusBadRequest, "ключи не работают: "+err.Error())
		return
	}
	if err := s.db.SaveBybitKeys(u.ID, req.APIKey, req.APISecret); err != nil {
		log.Printf("bybit save [%d]: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "не удалось сохранить")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleBybitDisconnect(w http.ResponseWriter, r *http.Request, u *TelegramUser) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if err := s.db.DeleteBybitKeys(u.ID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func readJSON(r *http.Request, dst any) error {
	body := io.LimitReader(r.Body, maxBodyBytes)
	if err := json.NewDecoder(body).Decode(dst); err != nil {
		return errors.New("некорректный JSON")
	}
	return nil
}
