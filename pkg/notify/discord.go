package notify

import (
	"context"
	"strings"

	"sentinel/pkg/termtext"
)

type Discord struct{ URL string }

func (d *Discord) Name() string { return "discord" }

func (d *Discord) Send(ctx context.Context, msg Message) error {
	// The subject is Sentinel's own; the body quotes outside text. Clean first: escaping "[" before an
	// escape sequence would turn it into "ESC \", the end of a string sequence, and swallow the backslash.
	msg.Body = discordEscape(termtext.Clean(msg.Body))
	return postJSON(ctx, d.URL, map[string]any{
		"content": truncate(msg.Text(), 2000),
		// An empty parse list pings nobody: a file name with @everyone in an error must not notify the channel.
		"allowed_mentions": map[string]any{"parse": []string{}},
	})
}

// discordEscape escapes "[", which every [text](url) link needs, so a file name cannot make a link that
// shows one address and opens another.
func discordEscape(text string) string {
	return strings.ReplaceAll(text, "[", `\[`)
}

var discordKind = kind{
	name:   "discord",
	fields: []Field{webhookField},
	build: func(settings Settings) Notifier {
		if settings.Discord == nil {
			return nil
		}
		return &Discord{URL: settings.Discord.URL}
	},
}
