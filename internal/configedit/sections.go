package configedit

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"sentinel/internal/config"
)

// The functions here edit the parts of the config that are not backup jobs: repositories,
// uptime checks, notifier channels and the general settings. They read and write the YAML
// nodes, so comments and keys they do not manage stay, and what is not in the file is not
// written (a default stays a default). Secrets are only ever environment variable names.

// Repository is one entry of repositories:.
type Repository struct {
	Name string
	URL  string
	// PasswordEnv names the variable holding the restic password; PasswordLiteral is set when
	// the file holds a literal one, which is kept unless PasswordEnv is set on save.
	PasswordEnv     string
	PasswordLiteral bool
	// Env maps a variable the repository needs (AWS_ACCESS_KEY_ID...) to the name of the
	// environment variable that holds its value. Literal values already in the file stay.
	Env map[string]string
	// UsedBy lists the jobs (and "defaults") that use the repository. Read only.
	UsedBy []string
}

// Uptime is one entry of uptime:. Numbers and durations are text so an empty input means
// "not set, use the default".
type Uptime struct {
	Name, Type                string
	URL, Address, Host        string
	Interval, Timeout         string
	ExpectStatus, Keyword     string
	CertExpiryDays, FailAfter string
}

// Notifier is one channel of notifiers:. Values holds each field of NotifierFields(Kind); a
// secret field holds the name of the environment variable, never the secret.
type Notifier struct {
	Kind    string
	Values  map[string]string
	Literal map[string]bool // secret fields that hold a literal value in the file (kept unless replaced)
}

// NotifierField describes one field of a channel.
type NotifierField struct {
	Key, Label string
	Secret     bool // the form asks for an environment variable name
	Required   bool
	Number     bool
	List       bool // comma separated
}

// NotifierKinds are the channels, in the order they are shown.
var NotifierKinds = []string{"telegram", "smtp", "discord", "slack"}

// NotifierFields are the fields of a channel kind.
func NotifierFields(kind string) []NotifierField {
	switch kind {
	case "telegram":
		return []NotifierField{{Key: "token", Label: "Bot token", Secret: true, Required: true}, {Key: "chat_id", Label: "Chat id", Required: true}}
	case "smtp":
		return []NotifierField{
			{Key: "host", Label: "Host", Required: true}, {Key: "port", Label: "Port", Number: true},
			{Key: "user", Label: "User"}, {Key: "pass", Label: "Password", Secret: true},
			{Key: "from", Label: "From"}, {Key: "to", Label: "To", Required: true, List: true},
		}
	case "discord", "slack":
		return []NotifierField{{Key: "webhook", Label: "Webhook URL", Secret: true, Required: true}}
	}
	return nil
}

// Settings are the general options.
type Settings struct {
	ServerName, Store   string
	Cooldown            string
	NotifySuccess       bool
	RepoCheckSchedule   string
	DefaultSchedule     string
	DefaultRepositories []string
	DefaultRetention    config.Retention
}

// Overview is everything Read finds in a config file besides the backup jobs.
type Overview struct {
	Repositories []Repository
	Uptime       []Uptime
	Notifiers    []Notifier
	Settings     Settings
}

// ---- reading

func text(mapping *yaml.Node, key string) string {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return ""
	}
	if node := mapGet(mapping, key); node != nil && node.Kind == yaml.ScalarNode {
		return node.Value
	}
	return ""
}

func listOf(mapping *yaml.Node, key string) []string {
	var items []string
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	if node := mapGet(mapping, key); node != nil && node.Kind == yaml.SequenceNode {
		for _, item := range node.Content {
			items = append(items, item.Value)
		}
	}
	return items
}

// secretOf splits a secret field into the environment variable name and whether the file
// holds a literal value instead.
func secretOf(value string) (envName string, literal bool) {
	if match := envRef.FindStringSubmatch(value); match != nil {
		return match[1], false
	}
	return "", value != ""
}

func retentionOf(mapping *yaml.Node) config.Retention {
	number := func(key string) int {
		n, _ := strconv.Atoi(text(mapping, key))
		return n
	}
	return config.Retention{Last: number("last"), Daily: number("daily"), Weekly: number("weekly"), Monthly: number("monthly"), Yearly: number("yearly")}
}

// Read returns the repositories, uptime checks, notifier channels and settings of the file.
func Read(path string) (Overview, error) {
	var overview Overview
	err := readConfig(path, func(root *yaml.Node, current *config.Config) error {
		usedBy := repositoryUsers(current)
		if repositories := mapGet(root, "repositories"); repositories != nil && repositories.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(repositories.Content); i += 2 {
				entry := repositories.Content[i+1]
				repository := Repository{Name: repositories.Content[i].Value, URL: text(entry, "url"), Env: map[string]string{}, UsedBy: usedBy[repositories.Content[i].Value]}
				repository.PasswordEnv, repository.PasswordLiteral = secretOf(text(entry, "password"))
				if env := mapGet(entry, "env"); env != nil && env.Kind == yaml.MappingNode {
					for j := 0; j+1 < len(env.Content); j += 2 {
						if name, literal := secretOf(env.Content[j+1].Value); !literal && name != "" {
							repository.Env[env.Content[j].Value] = name
						}
					}
				}
				overview.Repositories = append(overview.Repositories, repository)
			}
		}
		if checks := mapGet(root, "uptime"); checks != nil && checks.Kind == yaml.SequenceNode {
			for _, entry := range checks.Content {
				overview.Uptime = append(overview.Uptime, Uptime{
					Name: text(entry, "name"), Type: text(entry, "type"), URL: text(entry, "url"), Address: text(entry, "address"), Host: text(entry, "host"),
					Interval: text(entry, "interval"), Timeout: text(entry, "timeout"), ExpectStatus: text(entry, "expect_status"), Keyword: text(entry, "keyword"),
					CertExpiryDays: text(entry, "cert_expiry_days"), FailAfter: text(entry, "fail_after"),
				})
			}
		}
		notifiers := mapGet(root, "notifiers")
		for _, kind := range NotifierKinds {
			entry := mapGet(notifiers, kind)
			if notifiers == nil || entry == nil || entry.Kind != yaml.MappingNode {
				continue
			}
			notifier := Notifier{Kind: kind, Values: map[string]string{}, Literal: map[string]bool{}}
			for _, field := range NotifierFields(kind) {
				switch {
				case field.Secret:
					name, literal := secretOf(text(entry, field.Key))
					notifier.Values[field.Key], notifier.Literal[field.Key] = name, literal
				case field.List:
					notifier.Values[field.Key] = strings.Join(listOf(entry, field.Key), ", ")
				default:
					notifier.Values[field.Key] = text(entry, field.Key)
				}
			}
			overview.Notifiers = append(overview.Notifiers, notifier)
		}
		defaults := mapGet(root, "defaults")
		overview.Settings = Settings{
			ServerName: text(root, "server_name"), Store: text(root, "store"),
			Cooldown: text(mapGet(root, "alert"), "cooldown"), NotifySuccess: text(mapGet(root, "alert"), "notify_success") == "true",
			RepoCheckSchedule: text(root, "repo_check_schedule"),
			DefaultSchedule:   text(defaults, "schedule"), DefaultRepositories: listOf(defaults, "repositories"),
			DefaultRetention: retentionOf(mapGet(defaults, "retention")),
		}
		return nil
	})
	return overview, err
}

// repositoryUsers maps each repository to the jobs that use it ("defaults" for the defaults block).
func repositoryUsers(cfg *config.Config) map[string][]string {
	users := map[string][]string{}
	for _, job := range cfg.Backups {
		for _, name := range job.Repositories {
			users[name] = append(users[name], job.Name)
		}
	}
	for _, name := range cfg.Defaults.Repositories {
		users[name] = append(users[name], "defaults")
	}
	return users
}

// ---- writing helpers

func ensureMapping(parent *yaml.Node, key string, style yaml.Style) *yaml.Node {
	if node := mapGet(parent, key); node != nil && node.Kind == yaml.MappingNode {
		return node
	}
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: style}
	mapSet(parent, key, node)
	return node
}

func ensureSequence(parent *yaml.Node, key string) *yaml.Node {
	if node := mapGet(parent, key); node != nil && node.Kind == yaml.SequenceNode {
		return node
	}
	node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	mapSet(parent, key, node)
	return node
}

// setText sets a string key, or removes it when value is empty.
func setText(mapping *yaml.Node, key, value string) {
	setOrDelete(mapping, key, value)
}

// setNumber sets an integer key written from text, or removes it when empty.
func setNumber(mapping *yaml.Node, key, value string) {
	if value == "" {
		mapDelete(mapping, key)
		return
	}
	n, _ := strconv.Atoi(value) // validated by the caller
	mapSet(mapping, key, intNode(n))
}

// setEnvRef writes ${NAME}, or removes the key (keepLiteral keeps a literal value in the file).
func setEnvRef(mapping *yaml.Node, key, name string, keepLiteral bool) {
	switch {
	case name != "":
		mapSet(mapping, key, scalar("${"+name+"}", yaml.DoubleQuotedStyle))
	case !keepLiteral:
		mapDelete(mapping, key)
	}
}

func readConfig(path string, read func(root *yaml.Node, current *config.Config) error) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	current, err := config.ParseWith(raw, config.ParseOptions{ExpandEnv: false})
	if err != nil {
		return fmt.Errorf("config does not parse: %w", err)
	}
	_, root, err := parseDoc(raw)
	if err != nil {
		return err
	}
	return read(root, current)
}

func checkEnvName(field, name string) error {
	if name != "" && !envName.MatchString(name) {
		return &InvalidError{Message: fmt.Sprintf("%q is not an environment variable name (the secret itself is never entered here)", name), Field: field}
	}
	return nil
}

func fieldError(field, format string, args ...any) error {
	return &InvalidError{Message: fmt.Sprintf(format, args...), Field: field}
}

// ---- repositories

func validateRepository(repository Repository) error {
	switch {
	case !jobName.MatchString(repository.Name):
		return fieldError("name", "invalid repository name %q: use letters, digits, '.', '_' or '-'", repository.Name)
	case strings.TrimSpace(repository.URL) == "" || strings.ContainsAny(repository.URL, "\x00\n\r"):
		return fieldError("url", "the repository location is required")
	case repository.PasswordEnv == "" && !repository.PasswordLiteral:
		return fieldError("password_env", "name the environment variable that holds the repository password")
	}
	if err := checkEnvName("password_env", repository.PasswordEnv); err != nil {
		return err
	}
	for key, name := range repository.Env {
		if !envName.MatchString(key) {
			return fieldError("env", "%q is not a variable name", key)
		}
		if err := checkEnvName("env", name); err != nil {
			return err
		}
	}
	return nil
}

// SaveRepository adds the repository (original == "") or changes the one named original.
// A repository cannot be renamed: jobs refer to it by name.
func SaveRepository(path, original string, repository Repository) error {
	return editFile(path, func(root *yaml.Node, _ *config.Config) error {
		if err := validateRepository(repository); err != nil {
			return err
		}
		if original != "" && original != repository.Name {
			return invalid("a repository cannot be renamed: jobs refer to it by name (add a new one and move the jobs)")
		}
		repositories := ensureMapping(root, "repositories", 0)
		entry := mapGet(repositories, repository.Name)
		switch {
		case original == "" && entry != nil:
			return fmt.Errorf("%q: %w", repository.Name, ErrExists)
		case original != "" && entry == nil:
			return fmt.Errorf("repository %q: %w", original, ErrNotFound)
		case entry == nil:
			entry = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
			repositories.Content = append(repositories.Content, scalar(repository.Name, 0), entry)
		}
		mapSet(entry, "url", scalar(repository.URL, yaml.DoubleQuotedStyle))
		setEnvRef(entry, "password", repository.PasswordEnv, repository.PasswordLiteral)
		env := mapGet(entry, "env")
		if env == nil || env.Kind != yaml.MappingNode {
			env = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
		}
		for i := 0; i+1 < len(env.Content); { // drop the variable references that are no longer wanted
			key, value := env.Content[i].Value, env.Content[i+1].Value
			if reference, _ := secretOf(value); reference != "" && repository.Env[key] == "" {
				env.Content = append(env.Content[:i], env.Content[i+2:]...)
				continue
			}
			i += 2
		}
		for _, key := range slices.Sorted(maps.Keys(repository.Env)) {
			if repository.Env[key] != "" {
				mapSet(env, key, scalar("${"+repository.Env[key]+"}", yaml.DoubleQuotedStyle))
			}
		}
		if len(env.Content) == 0 {
			mapDelete(entry, "env")
		} else {
			mapSet(entry, "env", env)
		}
		return nil
	})
}

// DeleteRepository removes a repository that no job and no default uses. Its snapshots stay where they are.
func DeleteRepository(path, name string) error {
	return editFile(path, func(root *yaml.Node, current *config.Config) error {
		if users := repositoryUsers(current)[name]; len(users) > 0 {
			return invalid("repository %q is used by %s; change them first", name, strings.Join(users, ", "))
		}
		repositories := mapGet(root, "repositories")
		if repositories == nil || mapGet(repositories, name) == nil {
			return fmt.Errorf("repository %q: %w", name, ErrNotFound)
		}
		mapDelete(repositories, name)
		return nil
	})
}

// ---- uptime checks

func validateUptime(check *Uptime) error {
	if !jobName.MatchString(check.Name) {
		return fieldError("name", "invalid check name %q: use letters, digits, '.', '_' or '-'", check.Name)
	}
	switch check.Type {
	case config.CheckHTTP:
		if !strings.HasPrefix(check.URL, "http://") && !strings.HasPrefix(check.URL, "https://") {
			return fieldError("url", "the url must start with http:// or https://")
		}
		check.Address, check.Host = "", ""
	case config.CheckTCP:
		if !strings.Contains(check.Address, ":") || strings.ContainsAny(check.Address, " \n") {
			return fieldError("address", "use host:port, e.g. 10.0.0.5:5432")
		}
		check.URL, check.Host, check.ExpectStatus, check.Keyword, check.CertExpiryDays = "", "", "", "", ""
	case config.CheckPing:
		if !hostName.MatchString(check.Host) {
			return fieldError("host", "give a host name or address to ping")
		}
		check.URL, check.Address, check.ExpectStatus, check.Keyword, check.CertExpiryDays = "", "", "", "", ""
	default:
		return fieldError("type", "type must be http, tcp or ping")
	}
	for field, value := range map[string]string{"interval": check.Interval, "timeout": check.Timeout} {
		if value == "" {
			continue
		}
		if duration, err := time.ParseDuration(value); err != nil || duration <= 0 {
			return fieldError(field, "%s: %q is not a duration like 60s or 5m", field, value)
		}
	}
	for field, value := range map[string]string{"expect_status": check.ExpectStatus, "cert_expiry_days": check.CertExpiryDays, "fail_after": check.FailAfter} {
		if n, err := strconv.Atoi(value); value != "" && (err != nil || n < 0) {
			return fieldError(field, "%s: %q is not a number", field, value)
		}
	}
	return nil
}

func findByName(sequence *yaml.Node, name string) int {
	for i, item := range sequence.Content {
		if item.Kind == yaml.MappingNode && text(item, "name") == name {
			return i
		}
	}
	return -1
}

// SaveUptime adds an uptime check (original == "") or replaces the settings of the one named original.
func SaveUptime(path, original string, check Uptime) error {
	return editFile(path, func(root *yaml.Node, _ *config.Config) error {
		if err := validateUptime(&check); err != nil {
			return err
		}
		sequence := ensureSequence(root, "uptime")
		var entry *yaml.Node
		if original != "" {
			i := findByName(sequence, original)
			if i < 0 {
				return fmt.Errorf("uptime check %q: %w", original, ErrNotFound)
			}
			entry = sequence.Content[i]
		}
		if i := findByName(sequence, check.Name); i >= 0 && (entry == nil || sequence.Content[i] != entry) {
			return fmt.Errorf("%q: %w", check.Name, ErrExists)
		}
		if entry == nil {
			entry = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			sequence.Content = append(sequence.Content, entry)
		}
		mapSet(entry, "name", scalar(check.Name, 0))
		mapSet(entry, "type", scalar(check.Type, 0))
		setText(entry, "url", check.URL)
		setText(entry, "address", check.Address)
		setText(entry, "host", check.Host)
		setText(entry, "interval", check.Interval)
		setText(entry, "timeout", check.Timeout)
		setNumber(entry, "expect_status", check.ExpectStatus)
		setText(entry, "keyword", check.Keyword)
		setNumber(entry, "cert_expiry_days", check.CertExpiryDays)
		setNumber(entry, "fail_after", check.FailAfter)
		return nil
	})
}

// DeleteUptime removes an uptime check; its recorded history stays in the store until it ages out.
func DeleteUptime(path, name string) error {
	return editFile(path, func(root *yaml.Node, _ *config.Config) error {
		sequence := mapGet(root, "uptime")
		if sequence == nil || sequence.Kind != yaml.SequenceNode || findByName(sequence, name) < 0 {
			return fmt.Errorf("uptime check %q: %w", name, ErrNotFound)
		}
		i := findByName(sequence, name)
		sequence.Content = append(sequence.Content[:i], sequence.Content[i+1:]...)
		if len(sequence.Content) == 0 {
			mapDelete(root, "uptime")
		}
		return nil
	})
}

// ---- notifier channels

// SaveNotifier adds the channel or replaces its settings.
func SaveNotifier(path string, notifier Notifier) error {
	return editFile(path, func(root *yaml.Node, _ *config.Config) error {
		fields := NotifierFields(notifier.Kind)
		if fields == nil {
			return fieldError("kind", "channel must be one of %s", strings.Join(NotifierKinds, ", "))
		}
		for _, field := range fields {
			value := strings.TrimSpace(notifier.Values[field.Key])
			switch {
			case field.Required && value == "" && !notifier.Literal[field.Key]:
				return fieldError(field.Key, "%s is required", strings.ToLower(field.Label))
			case field.Secret:
				if err := checkEnvName(field.Key, value); err != nil {
					return err
				}
			case field.Number:
				if n, err := strconv.Atoi(value); value != "" && (err != nil || n < 1 || n > 65535) {
					return fieldError(field.Key, "%s must be a port number", strings.ToLower(field.Label))
				}
			}
		}
		channels := ensureMapping(root, "notifiers", 0)
		entry := mapGet(channels, notifier.Kind)
		if entry == nil || entry.Kind != yaml.MappingNode {
			entry = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			mapSet(channels, notifier.Kind, entry)
		}
		for _, field := range fields {
			value := strings.TrimSpace(notifier.Values[field.Key])
			switch {
			case field.Secret:
				setEnvRef(entry, field.Key, value, notifier.Literal[field.Key])
			case field.List:
				list := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
				for _, item := range strings.Split(value, ",") {
					if item = strings.TrimSpace(item); item != "" {
						list.Content = append(list.Content, scalar(item, 0))
					}
				}
				if len(list.Content) == 0 {
					mapDelete(entry, field.Key)
				} else {
					mapSet(entry, field.Key, list)
				}
			case field.Number:
				setNumber(entry, field.Key, value)
			case field.Key == "chat_id":
				if value == "" {
					mapDelete(entry, field.Key)
				} else {
					mapSet(entry, field.Key, scalar(value, yaml.DoubleQuotedStyle)) // a negative id must stay text
				}
			default:
				setText(entry, field.Key, value)
			}
		}
		return nil
	})
}

// DeleteNotifier removes a channel.
func DeleteNotifier(path, kind string) error {
	return editFile(path, func(root *yaml.Node, _ *config.Config) error {
		channels := mapGet(root, "notifiers")
		if channels == nil || mapGet(channels, kind) == nil {
			return fmt.Errorf("notifier %q: %w", kind, ErrNotFound)
		}
		mapDelete(channels, kind)
		if len(channels.Content) == 0 {
			mapDelete(root, "notifiers")
		}
		return nil
	})
}

// ---- settings

// SaveSettings writes the general options; an empty value removes the key so the default applies.
func SaveSettings(path string, settings Settings) error {
	return editFile(path, func(root *yaml.Node, _ *config.Config) error {
		if settings.Cooldown != "" {
			if duration, err := time.ParseDuration(settings.Cooldown); err != nil || duration <= 0 {
				return fieldError("cooldown", "cooldown: %q is not a duration like 1h or 30m", settings.Cooldown)
			}
		}
		for _, n := range []int{settings.DefaultRetention.Last, settings.DefaultRetention.Daily, settings.DefaultRetention.Weekly, settings.DefaultRetention.Monthly, settings.DefaultRetention.Yearly} {
			if n < 0 {
				return fieldError("retention", "retention values cannot be negative")
			}
		}
		setText(root, "server_name", settings.ServerName)
		setText(root, "store", settings.Store)
		setText(root, "repo_check_schedule", settings.RepoCheckSchedule)

		alert := ensureMapping(root, "alert", 0)
		setText(alert, "cooldown", settings.Cooldown)
		if settings.NotifySuccess {
			mapSet(alert, "notify_success", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
		} else {
			mapDelete(alert, "notify_success")
		}
		if len(alert.Content) == 0 {
			mapDelete(root, "alert")
		}

		defaults := ensureMapping(root, "defaults", 0)
		setText(defaults, "schedule", settings.DefaultSchedule)
		if len(settings.DefaultRepositories) == 0 {
			mapDelete(defaults, "repositories")
		} else {
			mapSet(defaults, "repositories", stringList(settings.DefaultRepositories))
		}
		retention := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
		for _, part := range []struct {
			key   string
			value int
		}{{"last", settings.DefaultRetention.Last}, {"daily", settings.DefaultRetention.Daily}, {"weekly", settings.DefaultRetention.Weekly}, {"monthly", settings.DefaultRetention.Monthly}, {"yearly", settings.DefaultRetention.Yearly}} {
			if part.value > 0 {
				retention.Content = append(retention.Content, scalar(part.key, 0), intNode(part.value))
			}
		}
		if len(retention.Content) == 0 {
			mapDelete(defaults, "retention")
		} else {
			mapSet(defaults, "retention", retention)
		}
		if len(defaults.Content) == 0 {
			mapDelete(root, "defaults")
		}
		return nil
	})
}
