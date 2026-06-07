package bybit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	baseURL      = "https://api.bybit.com"
	recvWindow   = "5000"
	maxBodyBytes = 2 * 1024 * 1024
)

type Client struct {
	apiKey    string
	apiSecret string
	http      *http.Client
}

func NewClient(apiKey, apiSecret string) *Client {
	return &Client{
		apiKey:    apiKey,
		apiSecret: apiSecret,
		http:      &http.Client{Timeout: 10 * time.Second},
	}
}

// sign формирует подпись запроса по алгоритму Bybit API v5
func (c *Client) sign(timestamp, queryString string) string {
	payload := timestamp + c.apiKey + recvWindow + queryString
	h := hmac.New(sha256.New, []byte(c.apiSecret))
	h.Write([]byte(payload))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (c *Client) get(path, query string, out any) error {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	signature := c.sign(timestamp, query)

	url := baseURL + path
	if query != "" {
		url += "?" + query
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("ошибка формирования запроса: %w", err)
	}
	req.Header.Set("X-BAPI-API-KEY", c.apiKey)
	req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
	req.Header.Set("X-BAPI-SIGN", signature)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка сети: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("ошибка чтения ответа: %w", err)
	}

	var base struct {
		RetCode int             `json:"retCode"`
		RetMsg  string          `json:"retMsg"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &base); err != nil {
		return fmt.Errorf("ошибка парсинга: %w", err)
	}
	if base.RetCode != 0 {
		return fmt.Errorf("Bybit API ошибка: %s (код %d)", base.RetMsg, base.RetCode)
	}

	return json.Unmarshal(base.Result, out)
}

// --- Модели ответов ---

type WalletBalance struct {
	List []AccountBalance `json:"list"`
}

type AccountBalance struct {
	AccountType       string      `json:"accountType"`
	TotalEquity       string      `json:"totalEquity"`        // общий баланс в USD
	TotalWalletBalance string     `json:"totalWalletBalance"` // кошелёк без нереализованного PnL
	TotalUnrealisedPnl string     `json:"totalUnrealisedPnl"`
	Coins             []CoinBalance `json:"coin"`
}

type CoinBalance struct {
	Coin            string `json:"coin"`
	WalletBalance   string `json:"walletBalance"`
	UsdValue        string `json:"usdValue"`
	UnrealisedPnl   string `json:"unrealisedPnl"`
	AvailableToWithdraw string `json:"availableToWithdraw"`
}

// GetWalletBalance возвращает баланс Unified аккаунта
func (c *Client) GetWalletBalance() (*WalletBalance, error) {
	var result WalletBalance
	err := c.get("/v5/account/wallet-balance", "accountType=UNIFIED", &result)
	if err != nil {
		// Пробуем SPOT если UNIFIED не доступен
		err2 := c.get("/v5/account/wallet-balance", "accountType=SPOT", &result)
		if err2 != nil {
			return nil, err // возвращаем оригинальную ошибку
		}
	}
	return &result, nil
}

// Validate проверяет что ключи рабочие (минимальный запрос к API)
func (c *Client) Validate() error {
	var result json.RawMessage
	return c.get("/v5/account/wallet-balance", "accountType=UNIFIED", &result)
}

// FormatBalance форматирует баланс для отправки в Telegram
func FormatBalance(wb *WalletBalance) string {
	if len(wb.List) == 0 {
		return "Баланс пуст или аккаунт не активирован."
	}

	var sb strings.Builder
	sb.WriteString("💎 Баланс Bybit\n\n")

	for _, acc := range wb.List {
		equity := parseFloat(acc.TotalEquity)
		pnl := parseFloat(acc.TotalUnrealisedPnl)

		sb.WriteString(fmt.Sprintf("Аккаунт: %s\n", acc.AccountType))
		sb.WriteString(fmt.Sprintf("Общий баланс: $%.2f\n", equity))

		if pnl != 0 {
			emoji := "🟢"
			if pnl < 0 {
				emoji = "🔴"
			}
			sb.WriteString(fmt.Sprintf("%s Нереализованный PnL: %+.2f$\n", emoji, pnl))
		}

		// Монеты с ненулевым балансом
		var coins []CoinBalance
		for _, coin := range acc.Coins {
			if parseFloat(coin.WalletBalance) > 0 {
				coins = append(coins, coin)
			}
		}

		if len(coins) > 0 {
			sb.WriteString("\nАктивы:\n")
			for _, coin := range coins {
				bal := parseFloat(coin.WalletBalance)
				usd := parseFloat(coin.UsdValue)
				line := fmt.Sprintf("  %s: %s", coin.Coin, formatCoinAmount(bal, coin.Coin))
				if usd > 0 {
					line += fmt.Sprintf(" (~$%.2f)", usd)
				}
				coinPnl := parseFloat(coin.UnrealisedPnl)
				if coinPnl != 0 {
					line += fmt.Sprintf(" PnL: %+.2f$", coinPnl)
				}
				sb.WriteString(line + "\n")
			}
		}
		sb.WriteString("\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func formatCoinAmount(amount float64, coin string) string {
	// Стейблкоины показываем с 2 знаками, остальные — умно
	stables := map[string]bool{"USDT": true, "USDC": true, "BUSD": true, "DAI": true}
	if stables[strings.ToUpper(coin)] {
		return fmt.Sprintf("%.2f", amount)
	}
	if amount >= 1 {
		return fmt.Sprintf("%.4f", amount)
	}
	return fmt.Sprintf("%.8f", amount)
}
