package notify

import (
	"slices"

	"sentinel/agent/internal/config"
)

// Field is one setting of a notifier in the config file, as the editors show it.
type Field struct {
	Key, Label string
	Secret     bool // the form asks for an environment variable name
	Required   bool
	Number     bool
	List       bool // comma separated
}

// webhookField is the only setting of Discord and Slack.
var webhookField = Field{Key: "webhook", Label: "Webhook URL", Secret: true, Required: true}

// kind describes one kind of notifier: the name it has under notifiers: in the config, the settings
// the editors ask for, and how to build it from the config. Each kind is in its own file.
type kind struct {
	name   string
	fields []Field
	// build returns nil when the config has no notifier of this kind.
	build func(settings config.Notifiers) Notifier
}

// kinds are the notifiers, in the order the editors show them.
var kinds = []kind{telegramKind, smtpKind, discordKind, slackKind}

// Kinds are the names of the notifiers, in the order they are shown.
func Kinds() []string {
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = k.name
	}
	return names
}

// FieldsOf are the settings of a kind of notifier; nil for a name that is none.
func FieldsOf(name string) []Field {
	i := slices.IndexFunc(kinds, func(k kind) bool { return k.name == name })
	if i < 0 {
		return nil
	}
	return kinds[i].fields
}
