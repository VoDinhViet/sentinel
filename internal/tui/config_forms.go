package tui

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"sentinel/internal/config"
	"sentinel/internal/configedit"
	"sentinel/internal/notify"
)

// The forms of the Config tab: repository, uptime check, notifier and settings. They use
// the same form engine as the job form; what differs is how the fields turn into an entry.

var repositoryKinds = []string{"local", "s3", "rclone", "sftp", "custom"}

var urlScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

// splitRepositoryURL breaks a restic repository location into the parts the form edits; a
// location it does not recognise is a "custom" one kept as it is.
func splitRepositoryURL(url string) (kind string, parts map[string]string) {
	switch {
	case strings.HasPrefix(url, "s3:"):
		rest := strings.TrimPrefix(url, "s3:")
		endpoint := ""
		if scheme, after, found := strings.Cut(rest, "://"); found {
			host, path, _ := strings.Cut(after, "/")
			endpoint, rest = scheme+"://"+host, path
		} else if host, path, _ := strings.Cut(rest, "/"); host != "s3.amazonaws.com" {
			endpoint, rest = host, path
		} else {
			rest = path
		}
		bucket, prefix, _ := strings.Cut(rest, "/")
		if bucket == "" {
			return "custom", map[string]string{"url": url}
		}
		return "s3", map[string]string{"endpoint": endpoint, "bucket": bucket, "prefix": prefix}
	case strings.HasPrefix(url, "rclone:"):
		if remote, path, found := strings.Cut(strings.TrimPrefix(url, "rclone:"), ":"); found && remote != "" {
			return "rclone", map[string]string{"remote": remote, "path": path}
		}
	case strings.HasPrefix(url, "sftp:") && !strings.HasPrefix(url, "sftp://"):
		if target, path, found := strings.Cut(strings.TrimPrefix(url, "sftp:"), ":"); found && target != "" {
			return "sftp", map[string]string{"target": target, "path": path}
		}
	case url != "" && !urlScheme.MatchString(url):
		return "local", map[string]string{"path": url}
	}
	return "custom", map[string]string{"url": url}
}

// buildRepositoryURL is the inverse of splitRepositoryURL.
func buildRepositoryURL(kind string, parts map[string]string) string {
	switch kind {
	case "local":
		return parts["path"]
	case "s3":
		endpoint := strings.TrimSuffix(parts["endpoint"], "/")
		if endpoint == "" {
			endpoint = "s3.amazonaws.com"
		}
		url := "s3:" + endpoint + "/" + parts["bucket"]
		if parts["prefix"] != "" {
			url += "/" + strings.Trim(parts["prefix"], "/")
		}
		return url
	case "rclone":
		return "rclone:" + parts["remote"] + ":" + parts["path"]
	case "sftp":
		return "sftp:" + parts["target"] + ":" + parts["path"]
	}
	return parts["url"]
}

// repositoryParts are the text fields of a repository form that make up its location, by kind.
var repositoryParts = map[string][]string{
	"local":  {"path"},
	"s3":     {"endpoint", "bucket", "prefix"},
	"rclone": {"remote", "path"},
	"sftp":   {"target", "path"},
	"custom": {"url"},
}

const (
	awsKeyID     = "AWS_ACCESS_KEY_ID"
	awsSecretKey = "AWS_SECRET_ACCESS_KEY"
)

func newRepositoryForm(repository configedit.Repository, original string, listing configedit.Listing) form {
	kind, parts := splitRepositoryURL(repository.URL)
	if original == "" {
		kind = "local"
	}
	passwordHint := "tên env var chứa mật khẩu restic (không bao giờ nhập chính mật khẩu)"
	if repository.PasswordLiteral {
		passwordHint = "file đang ghi thẳng mật khẩu; nhập tên env var để thay, hoặc để trống để giữ nguyên"
	}
	kept := map[string]string{}
	for key, name := range repository.Env {
		if key != awsKeyID && key != awsSecretKey {
			kept[key] = name
		}
	}
	title := "Thêm repository"
	if original != "" {
		title = "Sửa repository " + original
	}
	fields := []formField{
		{key: "name", label: "Tên", hint: "chữ, số, . _ - ; job gọi repository bằng tên này", value: repository.Name, fixed: original != ""},
		{key: "kind", label: "Loại", hint: "← → chọn nơi lưu backup", kind: fieldChoice, value: kind, choices: repositoryKinds},
	}
	add := func(onKind, key, label, hint, placeholder string) {
		fields = append(fields, formField{key: key, label: label, hint: hint, value: parts[key], placeholder: placeholder, onField: "kind", only: []string{onKind}})
	}
	add("local", "path", "Thư mục", "thư mục trên máy này hoặc ổ đĩa đã mount; dùng được ${HOME}", "")
	add("s3", "endpoint", "Endpoint", "để trống = Amazon S3; với MinIO, R2 hoặc Wasabi: https://host", "Amazon S3")
	add("s3", "bucket", "Bucket", "tên bucket", "")
	add("s3", "prefix", "Thư mục trong bucket", "tuỳ chọn: đường dẫn trong bucket, vd. web-01", "")
	add("rclone", "remote", "rclone remote", "tên remote trong `rclone config`, vd. gdrive", "")
	add("rclone", "path", "Thư mục", "đường dẫn trong remote, vd. backups/web-01", "")
	add("sftp", "target", "User và host", "user@host", "")
	add("sftp", "path", "Thư mục", "đường dẫn tuyệt đối trên server, vd. /srv/backups", "")
	add("custom", "url", "Địa chỉ", "địa chỉ repository restic bất kỳ, vd. b2:bucket:path", "")
	for i := range fields {
		fields[i].complete = fields[i].key == "path" && slices.Equal(fields[i].only, []string{"local"})
	}
	fields = append(fields,
		formField{key: "access_key_env", label: "Access key var", hint: "tên env var chứa access key id", value: repository.Env[awsKeyID], onField: "kind", only: []string{"s3"}},
		formField{key: "secret_key_env", label: "Secret key var", hint: "tên env var chứa secret access key", value: repository.Env[awsSecretKey], onField: "kind", only: []string{"s3"}},
		formField{key: "password_env", label: "Env var mật khẩu", hint: passwordHint, value: repository.PasswordEnv},
	)
	return form{kind: formRepository, title: title, original: original, passwordKept: repository.PasswordLiteral, known: configedit.Known{Repositories: listing.Repositories}, fields: fields, touched: map[string]bool{}, kept: kept}
}

// repository turns the form into a repository entry.
func (f form) repository() configedit.Repository {
	kind := f.field("kind").value
	parts := map[string]string{}
	for _, key := range repositoryParts[kind] {
		parts[key] = strings.TrimSpace(f.field(key).value)
	}
	repository := configedit.Repository{
		Name:            strings.TrimSpace(f.field("name").value),
		URL:             buildRepositoryURL(kind, parts),
		PasswordEnv:     strings.TrimSpace(f.field("password_env").value),
		PasswordLiteral: f.passwordKept,
		Env:             map[string]string{},
	}
	for key, name := range f.kept {
		repository.Env[key] = name
	}
	if kind == "s3" {
		for key, field := range map[string]string{awsKeyID: "access_key_env", awsSecretKey: "secret_key_env"} {
			if name := strings.TrimSpace(f.field(field).value); name != "" {
				repository.Env[key] = name
			}
		}
	}
	return repository
}

func newUptimeForm(check configedit.Uptime, original string) form {
	title := "Thêm uptime check"
	if original != "" {
		title = "Sửa check " + original
	}
	if check.Type == "" {
		check.Type = config.CheckHTTP
	}
	only := func(types ...string) (string, []string) { return "type", types }
	field := func(key, label, hint, value, placeholder string, kind fieldKind, types ...string) formField {
		field := formField{key: key, label: label, hint: hint, value: value, placeholder: placeholder, kind: kind}
		if len(types) > 0 {
			field.onField, field.only = only(types...)
		}
		return field
	}
	return form{kind: formUptime, title: title, original: original, touched: map[string]bool{}, fields: []formField{
		{key: "name", label: "Tên", hint: "chữ, số, . _ -", value: check.Name},
		{key: "type", label: "Loại", hint: "← → http: địa chỉ web · tcp: host:port nhận kết nối · ping: host có trả lời", kind: fieldChoice, value: check.Type, choices: []string{config.CheckHTTP, config.CheckTCP, config.CheckPing}},
		field("url", "Địa chỉ", "https://example.com/health", check.URL, "", fieldText, config.CheckHTTP),
		field("address", "Host:port", "vd. 10.0.0.5:5432", check.Address, "", fieldText, config.CheckTCP),
		field("host", "Host", "tên hoặc địa chỉ để ping", check.Host, "", fieldText, config.CheckPing),
		field("interval", "Mỗi", "bao lâu kiểm tra một lần, vd. 30s, 1m, 5m", check.Interval, "1m", fieldText),
		field("timeout", "Timeout", "quá thời gian này thì coi lần kiểm tra là lỗi, vd. 10s", check.Timeout, "10s", fieldText),
		field("expect_status", "Status mong đợi", "HTTP status được coi là up", check.ExpectStatus, "200", fieldNumber, config.CheckHTTP),
		field("keyword", "Phải chứa", "tuỳ chọn: chữ phải có trong trang", check.Keyword, "", fieldText, config.CheckHTTP),
		field("cert_expiry_days", "Cảnh báo cert", "cảnh báo khi chứng chỉ TLS hết hạn trong chừng này ngày", check.CertExpiryDays, "tắt", fieldNumber, config.CheckHTTP),
		field("fail_after", "Báo sau", "báo sau chừng này lần kiểm tra lỗi liên tiếp", check.FailAfter, "3", fieldNumber),
	}}
}

func (f form) uptime() configedit.Uptime {
	value := func(key string) string { return strings.TrimSpace(f.field(key).value) }
	return configedit.Uptime{
		Name: value("name"), Type: value("type"), URL: value("url"), Address: value("address"), Host: value("host"),
		Interval: value("interval"), Timeout: value("timeout"), ExpectStatus: value("expect_status"), Keyword: value("keyword"),
		CertExpiryDays: value("cert_expiry_days"), FailAfter: value("fail_after"),
	}
}

func newNotifierForm(notifier configedit.Notifier, original string) form {
	title := "Thêm notifier"
	if original != "" {
		title = "Sửa " + original
	}
	if notifier.Kind == "" {
		notifier.Kind = notify.Kinds()[0]
	}
	fields := []formField{{key: "kind", label: "Notifier", hint: "← → để chọn", kind: fieldChoice, value: notifier.Kind, choices: notify.Kinds(), fixed: original != ""}}
	for _, kind := range notify.Kinds() {
		for _, spec := range notify.FieldsOf(kind) {
			field := formField{key: kind + "." + spec.Key, label: spec.Label, hint: notifierHint(spec), value: notifier.Values[spec.Key], onField: "kind", only: []string{kind}}
			if kind != notifier.Kind {
				field.value = ""
			}
			switch {
			case spec.Secret:
				field.label += " var"
				if notifier.Kind == kind && notifier.Literal[spec.Key] {
					field.hint = "file đang ghi thẳng giá trị này; nhập tên env var để thay, hoặc để trống để giữ nguyên"
				}
			case spec.Number:
				field.kind = fieldNumber
			}
			fields = append(fields, field)
		}
	}
	return form{kind: formNotifier, title: title, original: original, touched: map[string]bool{}, fields: fields, kept: literalKeys(notifier)}
}

func notifierHint(spec notify.Field) string {
	switch {
	case spec.Secret:
		return "tên env var chứa giá trị này, vd. TG_TOKEN (không bao giờ nhập chính secret)"
	case spec.List:
		return "một hoặc nhiều địa chỉ, cách nhau bằng dấu phẩy"
	case spec.Number:
		return "port; để trống = 587"
	case spec.Key == "chat_id":
		return "id của chat hoặc group; id group bắt đầu bằng -"
	}
	return "bắt buộc"
}

// literalKeys remembers which secrets are literals in the file, so they are kept unless replaced.
func literalKeys(notifier configedit.Notifier) map[string]string {
	kept := map[string]string{}
	for key, literal := range notifier.Literal {
		if literal {
			kept[key] = "literal"
		}
	}
	return kept
}

func (f form) notifier() configedit.Notifier {
	kind := f.field("kind").value
	notifier := configedit.Notifier{Kind: kind, Values: map[string]string{}, Literal: map[string]bool{}}
	for _, spec := range notify.FieldsOf(kind) {
		value := strings.TrimSpace(f.field(kind + "." + spec.Key).value)
		notifier.Values[spec.Key] = value
		if spec.Secret && value == "" && f.original == kind && f.kept[spec.Key] == "literal" {
			notifier.Literal[spec.Key] = true
		}
	}
	return notifier
}

func newSettingsForm(settings configedit.Settings, listing configedit.Listing) form {
	ticked := make([]bool, len(listing.Repositories))
	for i, name := range listing.Repositories {
		for _, chosen := range settings.DefaultRepositories {
			ticked[i] = ticked[i] || chosen == name
		}
	}
	notifySuccess := "no"
	if settings.NotifySuccess {
		notifySuccess = "yes"
	}
	var retention [5]string
	for i, count := range []int{settings.DefaultRetention.Last, settings.DefaultRetention.Daily, settings.DefaultRetention.Weekly, settings.DefaultRetention.Monthly, settings.DefaultRetention.Yearly} {
		if count > 0 {
			retention[i] = fmt.Sprint(count)
		}
	}
	fields := []formField{
		{key: "server_name", label: "Tên server", hint: "tên hiện trong cảnh báo và trên màn hình này", value: settings.ServerName, placeholder: "tên máy này"},
		{key: "store", label: "File trạng thái", hint: "nơi lưu lịch sử chạy và khoá job (đường dẫn file)", value: settings.Store, placeholder: "/var/lib/sentinel/state.db"},
		{key: "cooldown", label: "Lặp cảnh báo", hint: "chờ bao lâu rồi nhắc lại một cảnh báo còn mở, vd. 30m, 1h", value: settings.Cooldown, placeholder: "1h"},
		{key: "shutdown_wait", label: "Chờ khi dừng", hint: "khi dừng daemon (restart, systemctl stop), chờ backup đang chạy xong tối đa bao lâu rồi mới huỷ, vd. 2h, 30m. Phải nhỏ hơn TimeoutStopSec của systemd", value: settings.ShutdownWait, placeholder: "2h"},
		{key: "priority", label: "Ưu tiên backup", hint: "low: backup chạy với CPU và đĩa ưu tiên thấp để không làm chậm dịch vụ khác (mặc định); normal: không hạ", kind: fieldChoice, value: orDefault(settings.Priority, "low"), choices: []string{"low", "normal"}},
		{key: "max_parallel", label: "Backup song song", hint: "tối đa bao nhiêu backup chạy cùng lúc trên máy này, job còn lại chờ đến lượt", value: settings.MaxParallel, placeholder: "1"},
		{key: "upload_limit_kib", label: "Giới hạn upload", hint: "KiB/s cho mỗi backup, vd. 20480 = 20 MiB/s. Để trống = không giới hạn", value: settings.UploadLimitKiB, placeholder: "không giới hạn"},
		{key: "notify_success", label: "Báo thành công", hint: "cũng gửi tin sau mỗi backup thành công", kind: fieldChoice, value: notifySuccess, choices: []string{"no", "yes"}},
	}
	fields = append(fields, scheduleFields("verify_schedule", "Kiểm tra repo", "khi nào chạy `restic check` trên mọi repository. Để trống = mặc định, gõ manual để tắt. "+scheduleHint, settings.VerifySchedule, "hàng tuần, chủ nhật 04:00 (mặc định)", "")...)
	fields = append(fields, scheduleFields("prune_schedule", "Dọn repo", "khi nào chạy `restic prune` trên mọi repository để xoá dữ liệu của các snapshot đã hết hạn giữ. Để trống = mặc định, gõ manual để tắt. "+scheduleHint, settings.PruneSchedule, "hàng tuần, thứ bảy 03:00 (mặc định)", "")...)
	fields = append(fields, scheduleFields("default_schedule", "Schedule mặc định", "cho job không đặt schedule. "+scheduleHint, settings.DefaultSchedule, "chưa có: job chạy bằng tay", "")...)
	fields = append(fields, []formField{
		{key: "default_repositories", label: "Repo mặc định", hint: "space hoặc click để tick; cho job không liệt kê repository. Không tick gì mà chỉ có một repository = repository đó", kind: fieldChecklist, choices: listing.Repositories, ticked: ticked},
		{key: "retention", label: "Giữ mặc định", hint: "số snapshot job giữ lại khi không đặt: N cái mới nhất, rồi mỗi ngày, tuần, tháng, năm một cái", kind: fieldRetention, counts: retention},
	}...)
	settingsForm := form{kind: formSettings, title: "Settings", original: "settings", touched: map[string]bool{}, known: configedit.Known{Repositories: listing.Repositories}, fields: fields}
	settingsForm.syncSchedules()
	return settingsForm
}

func (f form) settings() (configedit.Settings, error) {
	settings := configedit.Settings{
		ServerName: strings.TrimSpace(f.field("server_name").value), Store: strings.TrimSpace(f.field("store").value),
		Cooldown: strings.TrimSpace(f.field("cooldown").value), ShutdownWait: strings.TrimSpace(f.field("shutdown_wait").value),
		// low is the default: the key is left out.
		Priority: strings.TrimPrefix(f.field("priority").value, "low"), MaxParallel: strings.TrimSpace(f.field("max_parallel").value), UploadLimitKiB: strings.TrimSpace(f.field("upload_limit_kib").value), NotifySuccess: f.field("notify_success").value == "yes",
		VerifySchedule: strings.TrimSpace(f.field("verify_schedule").value), PruneSchedule: strings.TrimSpace(f.field("prune_schedule").value), DefaultSchedule: strings.TrimSpace(f.field("default_schedule").value),
	}
	repositories := f.field("default_repositories")
	for i, name := range repositories.choices {
		if repositories.ticked[i] {
			settings.DefaultRepositories = append(settings.DefaultRepositories, name)
		}
	}
	counts := f.field("retention").counts
	for i, target := range []*int{&settings.DefaultRetention.Last, &settings.DefaultRetention.Daily, &settings.DefaultRetention.Weekly, &settings.DefaultRetention.Monthly, &settings.DefaultRetention.Yearly} {
		if counts[i] != "" {
			if _, err := fmt.Sscan(counts[i], target); err != nil {
				return settings, formProblem("retention", "retention: %q không phải là số", counts[i])
			}
		}
	}
	return settings, nil
}

// checkEntries validates a repository, uptime, notifier or settings form without saving it.
func (f form) checkEntries() []error {
	switch f.kind {
	case formRepository:
		return f.repositoryProblems(f.repository())
	case formInit:
		return f.initProblems()
	case formUptime:
		check := f.uptime()
		return []error{configedit.ValidateUptime(&check)}
	case formNotifier:
		return []error{f.prefixNotifierField(configedit.ValidateNotifier(f.notifier()))}
	case formSettings:
		settings, err := f.settings()
		if err != nil {
			return []error{err}
		}
		return []error{configedit.ValidateSettings(settings)}
	}
	return nil
}

// repositoryProblems checks a repository built from the form's fields: the entry itself and the
// parts of its location the chosen kind needs.
func (f form) repositoryProblems(repo configedit.Repository) []error {
	var problems []error
	for _, problem := range configedit.RepositoryProblems(repo) {
		switch problem.Field {
		case "url": // the form has no such field: the missing parts are reported below
		case "env":
			problems = append(problems, formProblem("access_key_env", "%s", problem.Message))
		default:
			problems = append(problems, problem)
		}
	}
	required := map[string][]string{"local": {"path"}, "s3": {"bucket"}, "rclone": {"remote", "path"}, "sftp": {"target", "path"}, "custom": {"url"}}
	for _, key := range required[f.field("kind").value] {
		if strings.TrimSpace(f.field(key).value) == "" {
			problems = append(problems, formProblem(key, "cần nhập %s", strings.ToLower(f.field(key).label)))
		}
	}
	return problems
}

// prefixNotifierField maps a problem of a notifier field ("token") to the form's key ("telegram.token").
func (f form) prefixNotifierField(err error) error {
	var invalid *configedit.InvalidError
	if errors.As(err, &invalid) && invalid.Field != "" && invalid.Field != "kind" {
		return formProblem(f.field("kind").value+"."+invalid.Field, "%s", invalid.Message)
	}
	return err
}

// applyEntry saves a repository, uptime, notifier or settings form.
func (f form) applyEntry(actions manageActions) (string, error) {
	for _, err := range f.checkEntries() {
		if err != nil {
			return "", err
		}
	}
	switch f.kind {
	case formRepository:
		repository := f.repository()
		return "repository " + repository.Name, actions.saveRepository(f.original, repository)
	case formUptime:
		check := f.uptime()
		return "uptime check " + check.Name, actions.saveUptime(f.original, check)
	case formNotifier:
		notifier := f.notifier()
		return "notifier " + notifier.Kind, actions.saveNotifier(notifier)
	case formSettings:
		settings, _ := f.settings()
		return "cài đặt", actions.saveSettings(settings)
	case formInit:
		initial, _ := f.initial()
		return "config", actions.initialize(initial)
	}
	return "", errors.New("form không rõ")
}
