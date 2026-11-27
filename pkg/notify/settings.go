package notify

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

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

// Validate reports every problem in the settings at once. It is the check a program without a config
// validator makes (the control plane); the agent checks the same rules with tags on these types. A
// webhook URL must be https, because it usually is the secret itself.
func (s Settings) Validate() error {
	var errs []error
	if t := s.Telegram; t != nil {
		if t.Token == "" {
			errs = append(errs, errors.New("notifiers.telegram.token is required"))
		}
		if t.ChatID == "" {
			errs = append(errs, errors.New("notifiers.telegram.chat_id is required"))
		}
	}
	if m := s.SMTP; m != nil {
		if m.Host == "" {
			errs = append(errs, errors.New("notifiers.smtp.host is required"))
		}
		if len(m.To) == 0 {
			errs = append(errs, errors.New("notifiers.smtp.to needs at least 1 entry"))
		}
	}
	for name, hook := range map[string]*WebhookSettings{"discord": s.Discord, "slack": s.Slack} {
		if hook != nil && !strings.HasPrefix(hook.URL, "https://") {
			errs = append(errs, fmt.Errorf("notifiers.%s.webhook must be an https:// URL", name))
		}
	}
	return errors.Join(errs...)
}

// Validate checks the heartbeat settings: an https URL (or none, which turns it off) and an interval of
// a minute or more (or none, for the default).
func (h HeartbeatSettings) Validate() error {
	var errs []error
	if h.URL != "" && !strings.HasPrefix(h.URL, "https://") {
		errs = append(errs, errors.New("heartbeat.url must be an https:// URL"))
	}
	if h.Every != 0 && h.Every < time.Minute {
		errs = append(errs, errors.New("heartbeat.every must be at least 1m"))
	}
	return errors.Join(errs...)
}
