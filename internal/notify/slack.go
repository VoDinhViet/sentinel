package notify

import (
	"context"
	"strings"

	"sentinel/internal/config"
)

type Slack struct{ URL string }

func (s *Slack) Name() string { return "slack" }

func (s *Slack) Send(ctx context.Context, msg Message) error {
	return postJSON(ctx, s.URL, map[string]any{"text": truncate(slackEscape(msg.Text()), 3000)})
}

// slackEscape escapes the three characters Slack gives a meaning to (<!channel>, <https://x|label>), as its
// documentation asks for text that is not markup.
var slackEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace

var slackKind = kind{
	name:   "slack",
	fields: []Field{webhookField},
	build: func(settings config.Notifiers) Notifier {
		if settings.Slack == nil {
			return nil
		}
		return &Slack{URL: settings.Slack.URL}
	},
}
