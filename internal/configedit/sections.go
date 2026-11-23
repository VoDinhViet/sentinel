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
	"sentinel/internal/schedule"
)

// The functions here edit the parts of the config that are not backup jobs: repositories,
// uptime checks, notifier notifiers and the general settings. They read and write the YAML
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

// Notifier is one notifier of notifiers:. Values holds each field of NotifierFields(Kind); a
// secret field holds the name of the environment variable, never the secret.
type Notifier struct {
	Kind    string
	Values  map[string]string
	Literal map[string]bool // secret fields that hold a literal value in the file (kept unless replaced)
}

// NotifierField describes one field of a notifier.
type NotifierField struct {
	Key, Label string
	Secret     bool // the form asks for an environment variable name
	Required   bool
	Number     bool
	List       bool // comma separated
}

// NotifierKinds are the notifiers, in the order they are shown.
var NotifierKinds = []string{"telegram", "smtp", "discord", "slack"}

// NotifierFields are the fields of a notifier kind.
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
	ServerName, Store string
	Cooldown          string
	NotifySuccess     bool
	VerifySchedule    string
	ShutdownWait      string
	// Priority is "", "low" or "normal"; MaxParallel and UploadLimitKiB are numbers or "".
	Priority, MaxParallel, UploadLimitKiB string
	DefaultSchedule                       string
	DefaultRepositories                   []string
	DefaultRetention                      config.Retention
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
		for _, child := range node.Content {
			items = append(items, child.Value)
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
		num, _ := strconv.Atoi(text(mapping, key))
		return num
	}
	return config.Retention{Last: number("last"), Daily: number("daily"), Weekly: number("weekly"), Monthly: number("monthly"), Yearly: number("yearly")}
}

// Read returns the repositories, uptime checks, notifier notifiers and settings of the file.
func Read(path string) (Overview, error) {
	var overview Overview
	err := readConfig(path, func(root *yaml.Node, current *config.Config) error {
		usedBy := repositoryUsers(current)
		if repositories := mapGet(root, "repositories"); repositories != nil && repositories.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(repositories.Content); i += 2 {
				repoNode := repositories.Content[i+1]
				repository := Repository{Name: repositories.Content[i].Value, URL: text(repoNode, "url"), Env: map[string]string{}, UsedBy: usedBy[repositories.Content[i].Value]}
				repository.PasswordEnv, repository.PasswordLiteral = secretOf(text(repoNode, "password"))
				if env := mapGet(repoNode, "env"); env != nil && env.Kind == yaml.MappingNode {
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
			for _, checkNode := range checks.Content {
				overview.Uptime = append(overview.Uptime, Uptime{
					Name: text(checkNode, "name"), Type: text(checkNode, "type"), URL: text(checkNode, "url"), Address: text(checkNode, "address"), Host: text(checkNode, "host"),
					Interval: text(checkNode, "interval"), Timeout: text(checkNode, "timeout"), ExpectStatus: text(checkNode, "expect_status"), Keyword: text(checkNode, "keyword"),
					CertExpiryDays: text(checkNode, "cert_expiry_days"), FailAfter: text(checkNode, "fail_after"),
				})
			}
		}
		notifiers := mapGet(root, "notifiers")
		for _, kind := range NotifierKinds {
			notifierNode := mapGet(notifiers, kind)
			if notifiers == nil || notifierNode == nil || notifierNode.Kind != yaml.MappingNode {
				continue
			}
			notifier := Notifier{Kind: kind, Values: map[string]string{}, Literal: map[string]bool{}}
			for _, field := range NotifierFields(kind) {
				switch {
				case field.Secret:
					name, literal := secretOf(text(notifierNode, field.Key))
					notifier.Values[field.Key], notifier.Literal[field.Key] = name, literal
				case field.List:
					notifier.Values[field.Key] = strings.Join(listOf(notifierNode, field.Key), ", ")
				default:
					notifier.Values[field.Key] = text(notifierNode, field.Key)
				}
			}
			overview.Notifiers = append(overview.Notifiers, notifier)
		}
		defaults := mapGet(root, "defaults")
		overview.Settings = Settings{
			ServerName: text(root, "server_name"), Store: text(root, "store"),
			Cooldown: text(mapGet(root, "alert"), "cooldown"), NotifySuccess: text(mapGet(root, "alert"), "notify_success") == "true",
			VerifySchedule: text(root, "verify_schedule"), ShutdownWait: text(root, "shutdown_wait"),
			Priority: text(mapGet(root, "resources"), "priority"), MaxParallel: text(mapGet(root, "resources"), "max_parallel"), UploadLimitKiB: text(mapGet(root, "resources"), "upload_limit_kib"),
			DefaultSchedule: text(defaults, "schedule"), DefaultRepositories: listOf(defaults, "repositories"),
			DefaultRetention: retentionOf(mapGet(defaults, "retention")),
		}
		return nil
	})
	return overview, err
}

// repositoryUsers maps each repository to the jobs that use it ("defaults" for the defaults block).
func repositoryUsers(cfg *config.Config) map[string][]string {
	users := map[string][]string{}
	for _, job := range cfg.Jobs {
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
	num, _ := strconv.Atoi(value) // validated by the caller
	mapSet(mapping, key, intNode(num))
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
		return fmt.Errorf("config không đọc được: %w", err)
	}
	_, root, err := parseDoc(raw)
	if err != nil {
		return err
	}
	return read(root, current)
}

func checkEnvName(field, name string) error {
	if name != "" && !envNameRe.MatchString(name) {
		return &InvalidError{Message: fmt.Sprintf("%q không phải tên biến môi trường (secret không bao giờ nhập ở đây)", name), Field: field}
	}
	return nil
}

func fieldError(field, format string, args ...any) error {
	return &InvalidError{Message: fmt.Sprintf(format, args...), Field: field}
}

// ---- repositories

// RepositoryProblems lists what is wrong with a repository entry, at most one problem per field.
func RepositoryProblems(repository Repository) []*InvalidError {
	var problems []*InvalidError
	add := func(field, format string, args ...any) {
		problems = append(problems, &InvalidError{Message: fmt.Sprintf(format, args...), Field: field})
	}
	if !nameRe.MatchString(repository.Name) {
		add("name", "tên repository %q không hợp lệ: dùng chữ cái, chữ số, '.', '_' hoặc '-'", repository.Name)
	}
	if strings.TrimSpace(repository.URL) == "" || strings.ContainsAny(repository.URL, "\x00\n\r") {
		add("url", "cần nhập vị trí repository")
	}
	switch {
	case repository.PasswordEnv == "" && !repository.PasswordLiteral:
		add("password_env", "nhập tên biến môi trường chứa mật khẩu repository")
	case repository.PasswordEnv != "" && !envNameRe.MatchString(repository.PasswordEnv):
		add("password_env", "%q không phải tên biến môi trường (mật khẩu không bao giờ nhập ở đây)", repository.PasswordEnv)
	}
	for _, key := range slices.Sorted(maps.Keys(repository.Env)) {
		if !envNameRe.MatchString(key) {
			add("env", "%q không phải tên biến", key)
		} else if name := repository.Env[key]; name != "" && !envNameRe.MatchString(name) {
			add("env", "%q không phải tên biến môi trường (secret không bao giờ nhập ở đây)", name)
		}
	}
	return problems
}

// ValidateRepository returns the first problem of RepositoryProblems.
func ValidateRepository(repository Repository) error {
	if problems := RepositoryProblems(repository); len(problems) > 0 {
		return problems[0]
	}
	return nil
}

// SaveRepository adds the repository (original == "") or changes the one named original.
// A repository cannot be renamed: jobs refer to it by name.
func SaveRepository(path, original string, repository Repository) error {
	return editFile(path, func(root *yaml.Node, _ *config.Config) error {
		if err := ValidateRepository(repository); err != nil {
			return err
		}
		if original != "" && original != repository.Name {
			return invalid("không đổi tên được repository vì job gọi repository theo tên (hãy thêm repository mới rồi chuyển các job sang)")
		}
		repositories := ensureMapping(root, "repositories", 0)
		repoNode := mapGet(repositories, repository.Name)
		switch {
		case original == "" && repoNode != nil:
			return fmt.Errorf("%q: %w", repository.Name, ErrExists)
		case original != "" && repoNode == nil:
			return fmt.Errorf("repository %q: %w", original, ErrNotFound)
		case repoNode == nil:
			repoNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
			repositories.Content = append(repositories.Content, scalar(repository.Name, 0), repoNode)
		}
		mapSet(repoNode, "url", scalar(repository.URL, yaml.DoubleQuotedStyle))
		setEnvRef(repoNode, "password", repository.PasswordEnv, repository.PasswordLiteral)
		env := mapGet(repoNode, "env")
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
			mapDelete(repoNode, "env")
		} else {
			mapSet(repoNode, "env", env)
		}
		return nil
	})
}

// DeleteRepository removes a repository that no job and no default uses. Its snapshots stay where they are.
func DeleteRepository(path, name string) error {
	return editFile(path, func(root *yaml.Node, current *config.Config) error {
		if users := repositoryUsers(current)[name]; len(users) > 0 {
			return invalid("repository %q đang được dùng bởi %s; hãy sửa các job đó trước", name, strings.Join(users, ", "))
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

// ValidateUptime checks an uptime check and clears the fields its type does not use.
func ValidateUptime(check *Uptime) error {
	if !nameRe.MatchString(check.Name) {
		return fieldError("name", "tên uptime check %q không hợp lệ: dùng chữ cái, chữ số, '.', '_' hoặc '-'", check.Name)
	}
	switch check.Type {
	case config.CheckHTTP:
		if !strings.HasPrefix(check.URL, "http://") && !strings.HasPrefix(check.URL, "https://") {
			return fieldError("url", "url phải bắt đầu bằng http:// hoặc https://")
		}
		check.Address, check.Host = "", ""
	case config.CheckTCP:
		if !strings.Contains(check.Address, ":") || strings.ContainsAny(check.Address, " \n") {
			return fieldError("address", "dùng dạng host:port, ví dụ 10.0.0.5:5432")
		}
		check.URL, check.Host, check.ExpectStatus, check.Keyword, check.CertExpiryDays = "", "", "", "", ""
	case config.CheckPing:
		if !hostRe.MatchString(check.Host) {
			return fieldError("host", "nhập tên host hoặc địa chỉ cần ping")
		}
		check.URL, check.Address, check.ExpectStatus, check.Keyword, check.CertExpiryDays = "", "", "", "", ""
	default:
		return fieldError("type", "type phải là http, tcp hoặc ping")
	}
	for field, value := range map[string]string{"interval": check.Interval, "timeout": check.Timeout} {
		if value == "" {
			continue
		}
		if duration, err := time.ParseDuration(value); err != nil || duration <= 0 {
			return fieldError(field, "%s: %q không phải khoảng thời gian như 60s hoặc 5m", field, value)
		}
	}
	for field, value := range map[string]string{"expect_status": check.ExpectStatus, "cert_expiry_days": check.CertExpiryDays, "fail_after": check.FailAfter} {
		if num, err := strconv.Atoi(value); value != "" && (err != nil || num < 0) {
			return fieldError(field, "%s: %q không phải là số", field, value)
		}
	}
	return nil
}

func findByName(seq *yaml.Node, name string) int {
	for i, node := range seq.Content {
		if node.Kind == yaml.MappingNode && text(node, "name") == name {
			return i
		}
	}
	return -1
}

// SaveUptime adds an uptime check (original == "") or replaces the settings of the one named original.
func SaveUptime(path, original string, check Uptime) error {
	return editFile(path, func(root *yaml.Node, _ *config.Config) error {
		if err := ValidateUptime(&check); err != nil {
			return err
		}
		seq := ensureSequence(root, "uptime")
		var checkNode *yaml.Node
		if original != "" {
			i := findByName(seq, original)
			if i < 0 {
				return fmt.Errorf("uptime check %q: %w", original, ErrNotFound)
			}
			checkNode = seq.Content[i]
		}
		if i := findByName(seq, check.Name); i >= 0 && (checkNode == nil || seq.Content[i] != checkNode) {
			return fmt.Errorf("%q: %w", check.Name, ErrExists)
		}
		if checkNode == nil {
			checkNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			seq.Content = append(seq.Content, checkNode)
		}
		mapSet(checkNode, "name", scalar(check.Name, 0))
		mapSet(checkNode, "type", scalar(check.Type, 0))
		setText(checkNode, "url", check.URL)
		setText(checkNode, "address", check.Address)
		setText(checkNode, "host", check.Host)
		setText(checkNode, "interval", check.Interval)
		setText(checkNode, "timeout", check.Timeout)
		setNumber(checkNode, "expect_status", check.ExpectStatus)
		setText(checkNode, "keyword", check.Keyword)
		setNumber(checkNode, "cert_expiry_days", check.CertExpiryDays)
		setNumber(checkNode, "fail_after", check.FailAfter)
		return nil
	})
}

// DeleteUptime removes an uptime check; its recorded history stays in the store until it ages out.
func DeleteUptime(path, name string) error {
	return editFile(path, func(root *yaml.Node, _ *config.Config) error {
		seq := mapGet(root, "uptime")
		if seq == nil || seq.Kind != yaml.SequenceNode || findByName(seq, name) < 0 {
			return fmt.Errorf("uptime check %q: %w", name, ErrNotFound)
		}
		i := findByName(seq, name)
		seq.Content = append(seq.Content[:i], seq.Content[i+1:]...)
		if len(seq.Content) == 0 {
			mapDelete(root, "uptime")
		}
		return nil
	})
}

// ---- notifier notifiers

// ValidateNotifier checks a notifier's fields; the error names the field at fault.
func ValidateNotifier(notifier Notifier) error {
	fields := NotifierFields(notifier.Kind)
	if fields == nil {
		return fieldError("kind", "notifier phải là một trong %s", strings.Join(NotifierKinds, ", "))
	}
	for _, field := range fields {
		value := strings.TrimSpace(notifier.Values[field.Key])
		switch {
		case field.Required && value == "" && !notifier.Literal[field.Key]:
			return fieldError(field.Key, "%s là bắt buộc", field.Label)
		case field.Secret:
			if err := checkEnvName(field.Key, value); err != nil {
				return err
			}
		case field.Number:
			if num, err := strconv.Atoi(value); value != "" && (err != nil || num < 1 || num > 65535) {
				return fieldError(field.Key, "%s phải là số port từ 1 đến 65535", field.Label)
			}
		}
	}
	return nil
}

// SaveNotifier adds the notifier or replaces its settings.
func SaveNotifier(path string, notifier Notifier) error {
	return editFile(path, func(root *yaml.Node, _ *config.Config) error {
		if err := ValidateNotifier(notifier); err != nil {
			return err
		}
		fields := NotifierFields(notifier.Kind)
		notifiersNode := ensureMapping(root, "notifiers", 0)
		notifierNode := mapGet(notifiersNode, notifier.Kind)
		if notifierNode == nil || notifierNode.Kind != yaml.MappingNode {
			notifierNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			mapSet(notifiersNode, notifier.Kind, notifierNode)
		}
		for _, field := range fields {
			value := strings.TrimSpace(notifier.Values[field.Key])
			switch {
			case field.Secret:
				setEnvRef(notifierNode, field.Key, value, notifier.Literal[field.Key])
			case field.List:
				list := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
				for _, part := range strings.Split(value, ",") {
					if part = strings.TrimSpace(part); part != "" {
						list.Content = append(list.Content, scalar(part, 0))
					}
				}
				if len(list.Content) == 0 {
					mapDelete(notifierNode, field.Key)
				} else {
					mapSet(notifierNode, field.Key, list)
				}
			case field.Number:
				setNumber(notifierNode, field.Key, value)
			case field.Key == "chat_id":
				if value == "" {
					mapDelete(notifierNode, field.Key)
				} else {
					mapSet(notifierNode, field.Key, scalar(value, yaml.DoubleQuotedStyle)) // a negative id must stay text
				}
			default:
				setText(notifierNode, field.Key, value)
			}
		}
		return nil
	})
}

// DeleteNotifier removes a notifier.
func DeleteNotifier(path, kind string) error {
	return editFile(path, func(root *yaml.Node, _ *config.Config) error {
		notifiersNode := mapGet(root, "notifiers")
		if notifiersNode == nil || mapGet(notifiersNode, kind) == nil {
			return fmt.Errorf("notifier %q: %w", kind, ErrNotFound)
		}
		mapDelete(notifiersNode, kind)
		if len(notifiersNode.Content) == 0 {
			mapDelete(root, "notifiers")
		}
		return nil
	})
}

// ---- settings

// ValidateSettings checks the general options; the error names the field at fault.
func ValidateSettings(settings Settings) error {
	if settings.Cooldown != "" {
		if duration, err := time.ParseDuration(settings.Cooldown); err != nil || duration <= 0 {
			return fieldError("cooldown", "cooldown: %q không phải khoảng thời gian như 1h hoặc 30m", settings.Cooldown)
		}
	}
	if settings.Priority != "" && settings.Priority != config.PriorityLow && settings.Priority != config.PriorityNormal {
		return fieldError("priority", "priority: chọn low hoặc normal")
	}
	if settings.MaxParallel != "" {
		if count, err := strconv.Atoi(settings.MaxParallel); err != nil || count < 1 {
			return fieldError("max_parallel", "max_parallel: %q không phải số nguyên từ 1 trở lên", settings.MaxParallel)
		}
	}
	if settings.UploadLimitKiB != "" {
		if limit, err := strconv.Atoi(settings.UploadLimitKiB); err != nil || limit < 0 {
			return fieldError("upload_limit_kib", "upload_limit_kib: %q không phải số KiB/s như 20480", settings.UploadLimitKiB)
		}
	}
	if settings.ShutdownWait != "" {
		if duration, err := time.ParseDuration(settings.ShutdownWait); err != nil || duration <= 0 {
			return fieldError("shutdown_wait", "shutdown_wait: %q không phải khoảng thời gian như 2h hoặc 30m", settings.ShutdownWait)
		}
	}
	if !schedule.IsManual(settings.VerifySchedule) {
		if _, err := schedule.Parse(settings.VerifySchedule); err != nil {
			return fieldError("verify_schedule", "%v", err)
		}
	}
	if !schedule.IsManual(settings.DefaultSchedule) {
		if _, err := schedule.Parse(settings.DefaultSchedule); err != nil {
			return fieldError("default_schedule", "%v", err)
		}
	}
	for _, count := range []int{settings.DefaultRetention.Last, settings.DefaultRetention.Daily, settings.DefaultRetention.Weekly, settings.DefaultRetention.Monthly, settings.DefaultRetention.Yearly} {
		if count < 0 {
			return fieldError("retention", "giá trị retention không được âm")
		}
	}
	return nil
}

// SaveSettings writes the general options; an empty value removes the key so the default applies.
func SaveSettings(path string, settings Settings) error {
	return editFile(path, func(root *yaml.Node, _ *config.Config) error {
		if err := ValidateSettings(settings); err != nil {
			return err
		}
		setText(root, "server_name", settings.ServerName)
		setText(root, "store", settings.Store)
		setText(root, "verify_schedule", settings.VerifySchedule)
		setText(root, "shutdown_wait", settings.ShutdownWait)

		resources := ensureMapping(root, "resources", 0)
		setText(resources, "priority", settings.Priority)
		setNumber(resources, "max_parallel", settings.MaxParallel)
		setNumber(resources, "upload_limit_kib", settings.UploadLimitKiB)
		if len(resources.Content) == 0 {
			mapDelete(root, "resources")
		}

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
