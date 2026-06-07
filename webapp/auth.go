package webapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TelegramUser — пользователь из initData
type TelegramUser struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

// ValidateInitData проверяет подпись Telegram WebApp initData по HMAC-SHA256
// Возвращает данные пользователя при успехе.
// Спецификация: https://core.telegram.org/bots/webapps#validating-data-received-via-the-mini-app
func ValidateInitData(initData, botToken string) (*TelegramUser, error) {
	if initData == "" {
		return nil, errors.New("пустые initData")
	}

	// 1. Парсим URL-encoded query string
	values, err := url.ParseQuery(initData)
	if err != nil {
		return nil, errors.New("некорректные initData")
	}

	hash := values.Get("hash")
	if hash == "" {
		return nil, errors.New("отсутствует hash")
	}
	values.Del("hash")

	// 2. Строим data_check_string: пары "key=value", отсортированные по ключу, через \n
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(values.Get(k))
	}

	// 3. secret_key = HMAC_SHA256("WebAppData", bot_token)
	skMac := hmac.New(sha256.New, []byte("WebAppData"))
	skMac.Write([]byte(botToken))
	secretKey := skMac.Sum(nil)

	// 4. expected = HMAC_SHA256(secret_key, data_check_string)
	mac := hmac.New(sha256.New, secretKey)
	mac.Write([]byte(sb.String()))
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(hash)) {
		return nil, errors.New("неверная подпись initData")
	}

	// 5. Проверяем актуальность (не старше 24 часов — защита от replay)
	if authDateStr := values.Get("auth_date"); authDateStr != "" {
		if authDate, err := strconv.ParseInt(authDateStr, 10, 64); err == nil {
			if time.Since(time.Unix(authDate, 0)) > 24*time.Hour {
				return nil, errors.New("initData устарел")
			}
		}
	}

	// 6. Извлекаем пользователя
	userJSON := values.Get("user")
	if userJSON == "" {
		return nil, errors.New("отсутствует user")
	}
	var user TelegramUser
	if err := json.Unmarshal([]byte(userJSON), &user); err != nil {
		return nil, errors.New("ошибка парсинга user")
	}
	if user.ID == 0 {
		return nil, errors.New("user.id = 0")
	}

	return &user, nil
}
