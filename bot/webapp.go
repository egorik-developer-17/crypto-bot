package bot

import (
	tele "gopkg.in/telebot.v3"
)

// SetWebAppMenuButton устанавливает кнопку Menu бота слева от поля ввода —
// при нажатии открывается Mini App по указанному HTTPS URL.
func SetWebAppMenuButton(b *tele.Bot, url string) error {
	return b.SetMenuButton(nil, &tele.MenuButton{
		Type: tele.MenuButtonWebApp,
		Text: "🚀 Открыть",
		WebApp: &tele.WebApp{
			URL: url,
		},
	})
}
