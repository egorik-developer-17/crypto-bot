package bot

import (
	"encoding/json"
	"fmt"

	tele "gopkg.in/telebot.v3"
)

// SetWebAppMenuButton устанавливает дефолтную кнопку Menu бота (без привязки к чату) —
// при нажатии открывается Mini App по указанному HTTPS URL.
//
// Используем сырой вызов API: telebot.SetMenuButton требует Recipient и крашится на nil.
func SetWebAppMenuButton(b *tele.Bot, url string) error {
	payload := map[string]any{
		"menu_button": map[string]any{
			"type": "web_app",
			"text": "🚀 Открыть",
			"web_app": map[string]any{
				"url": url,
			},
		},
	}
	raw, err := b.Raw("setChatMenuButton", payload)
	if err != nil {
		return err
	}
	var resp struct {
		Ok          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return err
	}
	if !resp.Ok {
		return fmt.Errorf("telegram: %s", resp.Description)
	}
	return nil
}
