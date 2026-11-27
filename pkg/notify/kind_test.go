package notify

import (
	"slices"
	"testing"
)

func TestEveryKindIsKnownByItsName(t *testing.T) {
	want := []string{"telegram", "smtp", "discord", "slack"}
	if got := Kinds(); !slices.Equal(got, want) {
		t.Fatalf("Kinds() = %v, want %v", got, want)
	}
	for _, name := range Kinds() {
		if len(FieldsOf(name)) == 0 {
			t.Errorf("%s has no fields", name)
		}
	}
	if FieldsOf("pagerduty") != nil {
		t.Error("an unknown kind has fields")
	}
}

func TestFromConfigBuildsOnlyWhatIsConfiguredInTheOrderOfKinds(t *testing.T) {
	settings := Settings{
		Slack:    &WebhookSettings{URL: "https://hooks.slack.test/x"},
		Telegram: &TelegramSettings{Token: "t", ChatID: "1"},
	}
	var names []string
	for _, notifier := range FromConfig(settings) {
		names = append(names, notifier.Name())
	}
	if want := []string{"telegram", "slack"}; !slices.Equal(names, want) {
		t.Fatalf("notifiers = %v, want %v", names, want)
	}
	if got := FromConfig(Settings{}); len(got) != 0 {
		t.Fatalf("an empty config built %d notifiers", len(got))
	}
}
