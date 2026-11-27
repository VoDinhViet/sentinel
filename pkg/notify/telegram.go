package notify

import (
	"context"
)

type Telegram struct {
	Token  string
	ChatID string
	// BaseURL overrides https://api.telegram.org (tests).
	BaseURL string
}

func (t *Telegram) Name() string { return "telegram" }

func (t *Telegram) Send(ctx context.Context, msg Message) error {
	baseURL := t.BaseURL
	if baseURL == "" {
		baseURL = "https://api.telegram.org"
	}
	return postJSON(ctx, baseURL+"/bot"+t.Token+"/sendMessage", map[string]any{
		"chat_id":                  t.ChatID,
		"text":                     truncate(msg.Text(), 4096),
		"disable_web_page_preview": true,
	})
}

var telegramKind = kind{
	name: "telegram",
	fields: []Field{
		{Key: "token", Label: "Bot token", Secret: true, Required: true},
		{Key: "chat_id", Label: "Chat id", Required: true},
	},
	build: func(settings Settings) Notifier {
		if settings.Telegram == nil {
			return nil
		}
		return &Telegram{Token: settings.Telegram.Token, ChatID: settings.Telegram.ChatID}
	},
}
