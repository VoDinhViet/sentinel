package notify

import "time"

// Settings are the notifiers: section of a config file. A notifier that is not there is nil.
type Settings struct {
	Telegram *TelegramSettings `yaml:"telegram"`
	SMTP     *SMTPSettings     `yaml:"smtp"`
	Discord  *WebhookSettings  `yaml:"discord"`
	Slack    *WebhookSettings  `yaml:"slack"`
}

type TelegramSettings struct {
	Token  string `yaml:"token" validate:"required"`
	ChatID string `yaml:"chat_id" validate:"required"`
}

type SMTPSettings struct {
	Host string   `yaml:"host" validate:"required"`
	Port int      `yaml:"port"`
	User string   `yaml:"user"`
	Pass string   `yaml:"pass"`
	From string   `yaml:"from"`
	To   []string `yaml:"to" validate:"min=1"`
	// InsecurePlaintext lets a mail go out unencrypted when a server that is not this machine offers no
	// STARTTLS. Off by default: the alert text quotes file names and error output.
	InsecurePlaintext bool `yaml:"insecure_plaintext"`
}

type WebhookSettings struct {
	URL string `yaml:"webhook" validate:"required,https_url"`
}

// DefaultHeartbeatEvery is how often the heartbeat URL is requested when the config sets no interval.
const DefaultHeartbeatEvery = 5 * time.Minute

// HeartbeatSettings is a URL the daemon requests on a schedule, so a service outside the server
// (healthchecks.io, an Uptime Kuma push monitor) can tell the daemon is alive: when the requests
// stop, that service sends the alert that Sentinel, being down, cannot. It is off when URL is empty.
type HeartbeatSettings struct {
	URL   string        `yaml:"url" validate:"omitempty,https_url"`
	Every time.Duration `yaml:"every" validate:"omitempty,gte=1m"`
}
