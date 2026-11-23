// Package configedit adds, changes and removes backup jobs in a Sentinel config
// file without disturbing the rest of it: comments, key order and keys it does
// not manage are kept. Passwords are never entered here; a job refers to an
// environment variable, written into the file as ${NAME}.
package configedit

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"sentinel/internal/config"
	"sentinel/internal/schedule"
)

var (
	// ErrNotFound means the named job is not in the config.
	ErrNotFound = errors.New("job not found")
	// ErrExists means a job with that name is already in the config.
	ErrExists = errors.New("a backup job with that name already exists")
)

// InvalidError is a problem with what the user entered; its message is meant to be shown.
// Field names the input the problem is about ("" when it is about the whole job).
type InvalidError struct {
	Message string
	Field   string
}

func (e *InvalidError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &InvalidError{Message: fmt.Sprintf(format, args...)}
}

// DatabaseTypes are the database job types; JobTypes are all the types this package edits.
var (
	DatabaseTypes = []string{config.BackupPostgres, config.BackupMySQL, config.BackupMongoDB}
	JobTypes      = append(slices.Clone(DatabaseTypes), config.BackupFiles)
)

// Job is one backup job as the editor sees it. A files job fills Paths and Exclude and none
// of the database fields.
type Job struct {
	Name     string
	Type     string
	Database string
	Host     string
	Port     int
	User     string
	// PasswordEnv names the environment variable that holds the password ("" = none).
	PasswordEnv string
	// PasswordLiteral is set when the file holds a literal password instead of a
	// ${NAME} reference. It is kept unless PasswordEnv is set on save.
	PasswordLiteral bool
	Schedule        string
	Repositories    []string
	Retention       config.Retention
	TLS             string // mysql: "", "disabled" or "required"
	AuthDatabase    string // mongodb
	Paths           []string
	Exclude         []string

	// Inherited says which of Schedule, Repositories and Retention are not in the job's own
	// entry: they are left empty here and the config's defaults apply.
	Inherited config.Inherited
}

// Editable reports whether the job is of a type this package can change.
func (j Job) Editable() bool { return slices.Contains(JobTypes, j.Type) }

// Listing is what the editor shows: every backup job and the repository names jobs may use.
type Listing struct {
	Jobs         []Job
	Repositories []string
	// Defaults is the config's defaults block; InheritedRepositories are the repositories a
	// job without its own list uses (defaults.repositories, or the only repository).
	Defaults              config.Defaults
	InheritedRepositories []string
}

// Known is what Validate checks a job against.
type Known struct {
	Repositories []string
	// InheritsRepositories is true when a job without repositories gets some from the config.
	InheritsRepositories bool
}

var (
	envRef     = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)
	envName    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	jobName    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)
	hostName   = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,255}$`)
	identifier = regexp.MustCompile(`^[^\x00-\x1f\x7f]{1,128}$`)
)

func defaultPort(jobType string) int {
	switch jobType {
	case config.BackupMySQL:
		return 3306
	case config.BackupMongoDB:
		return 27017
	}
	return 5432
}

// List reads the config file at path.
func List(path string) (Listing, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Listing{}, err
	}
	cfg, err := config.ParseWith(raw, config.ParseOptions{ExpandEnv: false})
	if err != nil {
		return Listing{}, err
	}
	listing := Listing{Jobs: []Job{}, Repositories: []string{}, Defaults: cfg.Defaults}
	for name := range cfg.Repositories {
		listing.Repositories = append(listing.Repositories, name)
	}
	sort.Strings(listing.Repositories)
	listing.InheritedRepositories = inheritedRepositories(cfg)
	for _, backup := range cfg.Backups {
		listing.Jobs = append(listing.Jobs, jobFromConfig(backup))
	}
	return listing, nil
}

// inheritedRepositories are the repositories a job without its own list gets.
func inheritedRepositories(cfg *config.Config) []string {
	switch {
	case len(cfg.Defaults.Repositories) > 0:
		return slices.Clone(cfg.Defaults.Repositories)
	case len(cfg.Repositories) == 1:
		return slices.Collect(maps.Keys(cfg.Repositories))
	}
	return nil
}

func jobFromConfig(backup config.Backup) Job {
	job := Job{Name: backup.Name, Type: backup.Type, Schedule: backup.Schedule, Repositories: backup.Repositories, Retention: backup.Retention, Inherited: backup.Inherited,
		Paths: backup.Paths, Exclude: backup.Exclude}
	if backup.Inherited.Schedule {
		job.Schedule = ""
	}
	if backup.Inherited.Repositories {
		job.Repositories = nil
	}
	if backup.Inherited.Retention {
		job.Retention = config.Retention{}
	}
	var password string
	switch {
	case backup.Postgres != nil:
		p := backup.Postgres
		job.Database, job.Host, job.Port, job.User, password = p.Database, p.Host, p.Port, p.User, p.Password
	case backup.MySQL != nil:
		m := backup.MySQL
		job.Database, job.Host, job.Port, job.User, password, job.TLS = m.Database, m.Host, m.Port, m.User, m.Password, m.TLS
	case backup.MongoDB != nil:
		m := backup.MongoDB
		job.Database, job.Host, job.Port, job.User, password, job.AuthDatabase = m.Database, m.Host, m.Port, m.User, m.Password, m.AuthDatabase
	}
	if match := envRef.FindStringSubmatch(password); match != nil {
		job.PasswordEnv = match[1]
	} else if password != "" {
		job.PasswordLiteral = true
	}
	return job
}

// Validate checks a job against what the config defines and fills in defaults. It returns
// the first problem; Problems returns all of them.
func Validate(job *Job, known Known) error {
	if problems := Problems(job, known); len(problems) > 0 {
		return problems[0]
	}
	return nil
}

// Problems lists everything wrong with the job, at most one problem per field, in field order.
func Problems(job *Job, known Known) []*InvalidError {
	var problems []*InvalidError
	add := func(field, format string, args ...any) {
		problems = append(problems, &InvalidError{Message: fmt.Sprintf(format, args...), Field: field})
	}
	if !slices.Contains(JobTypes, job.Type) {
		add("type", "type must be one of %v", JobTypes)
	}
	if !jobName.MatchString(job.Name) {
		add("name", "invalid job name %q: use letters, digits, '.', '_' or '-'", job.Name)
	}
	if job.Type == config.BackupFiles {
		return append(problems, append(filesProblems(job), commonProblems(job, known)...)...)
	}
	if !identifier.MatchString(job.Database) {
		add("database", "database name is required")
	}
	if job.User != "" && !identifier.MatchString(job.User) {
		add("user", "invalid user")
	}
	if job.Host == "" {
		job.Host = "localhost"
	}
	if !hostName.MatchString(job.Host) {
		add("host", "invalid host %q", job.Host)
	}
	if job.Port == 0 {
		job.Port = defaultPort(job.Type)
	}
	if job.Port < 1 || job.Port > 65535 {
		add("port", "port must be between 1 and 65535")
	}
	if job.PasswordEnv != "" && !envName.MatchString(job.PasswordEnv) {
		add("password_env", "invalid environment variable name %q (the password itself is never entered here)", job.PasswordEnv)
	}
	if job.Type == config.BackupMySQL && job.TLS != "" && job.TLS != "disabled" && job.TLS != "required" {
		add("tls", "tls must be empty, disabled or required")
	}
	if job.Type != config.BackupMySQL {
		job.TLS = ""
	}
	if job.Type != config.BackupMongoDB {
		job.AuthDatabase = ""
	}
	return append(problems, commonProblems(job, known)...)
}

// filesProblems checks the paths of a files job.
func filesProblems(job *Job) []*InvalidError {
	var problems []*InvalidError
	paths := slices.DeleteFunc(slices.Clone(job.Paths), func(path string) bool { return path == "" })
	if len(paths) == 0 {
		problems = append(problems, &InvalidError{Message: "give at least one path to back up", Field: "paths"})
	}
	for _, path := range append(slices.Clone(paths), job.Exclude...) {
		if strings.ContainsAny(path, "\x00\n\r") {
			problems = append(problems, &InvalidError{Message: "a path cannot contain line breaks", Field: "paths"})
			break
		}
	}
	job.Paths = paths
	return problems
}

// commonProblems checks what every job type shares: schedule, repositories and retention.
func commonProblems(job *Job, known Known) []*InvalidError {
	var problems []*InvalidError
	add := func(field, format string, args ...any) {
		problems = append(problems, &InvalidError{Message: fmt.Sprintf(format, args...), Field: field})
	}
	if !schedule.IsManual(job.Schedule) {
		if _, err := schedule.Parse(job.Schedule); err != nil {
			add("schedule", "invalid schedule: %v (try \"daily 02:00\", \"weekly sun 03:00\" or a cron expression)", err)
		}
	}
	if len(job.Repositories) == 0 && !known.InheritsRepositories {
		add("repositories", "choose at least one repository")
	}
	slices.Sort(job.Repositories)
	job.Repositories = slices.Compact(job.Repositories)
	for _, name := range job.Repositories {
		if !slices.Contains(known.Repositories, name) {
			add("repositories", "unknown repository %q (defined: %v)", name, known.Repositories)
			break
		}
	}
	for _, n := range []int{job.Retention.Last, job.Retention.Daily, job.Retention.Weekly, job.Retention.Monthly, job.Retention.Yearly} {
		if n < 0 {
			add("retention", "retention values cannot be negative")
			break
		}
	}
	return problems
}

// Save adds the job (original == "") or replaces the settings of the job named
// original, then rewrites the file. The previous file is kept as path+".bak".
func Save(path, original string, job Job) error {
	return editFile(path, func(root *yaml.Node, cfg *config.Config) error {
		known := Known{Repositories: slices.Sorted(maps.Keys(cfg.Repositories)), InheritsRepositories: len(inheritedRepositories(cfg)) > 0}
		if err := Validate(&job, known); err != nil {
			return err
		}
		return editBackup(root, original, job)
	})
}

// Delete removes a database job from the file. Its snapshots stay in the repositories.
func Delete(path, name string) error {
	return editFile(path, func(root *yaml.Node, _ *config.Config) error { return removeBackup(root, name) })
}

// editFile applies edit to the file's YAML, checks that the result is still a
// valid config, and replaces the file atomically with the same permissions.
func editFile(path string, edit func(root *yaml.Node, current *config.Config) error) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	current, err := config.ParseWith(raw, config.ParseOptions{ExpandEnv: false})
	if err != nil {
		return fmt.Errorf("config does not parse, not editing it: %w", err)
	}
	doc, root, err := parseDoc(raw)
	if err != nil {
		return err
	}
	if err := edit(root, current); err != nil {
		return err
	}
	edited, err := encodeDoc(doc)
	if err != nil {
		return err
	}
	if _, err := config.ParseWith(edited, config.ParseOptions{ExpandEnv: false}); err != nil {
		return invalid("the change makes the config invalid: %v", err)
	}
	return replaceFile(path, raw, edited)
}

func replaceFile(path string, previous, next []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()
	if err := os.WriteFile(path+".bak", previous, mode); err != nil {
		return fmt.Errorf("write backup of the config: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".sentinel-config-*")
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(next); err != nil {
		temp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return os.Rename(temp.Name(), path)
}

// ---- YAML node editing

func scalar(value string, style yaml.Style) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: style}
}

func intNode(n int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(n)}
}

func mapGet(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// mapSet sets key, keeping the comments attached to the old value.
func mapSet(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			old := mapping.Content[i+1]
			value.HeadComment, value.LineComment, value.FootComment = old.HeadComment, old.LineComment, old.FootComment
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, scalar(key, 0), value)
}

func mapDelete(mapping *yaml.Node, key string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return
		}
	}
}

func parseDoc(raw []byte) (*yaml.Node, *yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, nil, fmt.Errorf("parse config: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, invalid("config must be a YAML mapping")
	}
	return &doc, doc.Content[0], nil
}

func encodeDoc(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), encoder.Close()
}

func findBackup(sequence *yaml.Node, name string) int {
	for i, item := range sequence.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		if nameNode := mapGet(item, "name"); nameNode != nil && nameNode.Value == name {
			return i
		}
	}
	return -1
}

func isEditableJob(item *yaml.Node) bool {
	typeNode := mapGet(item, "type")
	return typeNode != nil && slices.Contains(JobTypes, typeNode.Value)
}

// stringList is a flow-style YAML list of the strings.
func stringList(values []string) *yaml.Node {
	list := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, value := range values {
		list.Content = append(list.Content, scalar(value, 0))
	}
	return list
}

func editBackup(root *yaml.Node, original string, job Job) error {
	sequence := mapGet(root, "backups")
	if sequence == nil {
		sequence = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		root.Content = append(root.Content, scalar("backups", 0), sequence)
	}
	if sequence.Kind != yaml.SequenceNode {
		return invalid("config: backups must be a list")
	}
	var item *yaml.Node
	if original != "" {
		i := findBackup(sequence, original)
		if i < 0 {
			return fmt.Errorf("job %q: %w", original, ErrNotFound)
		}
		item = sequence.Content[i]
		if !isEditableJob(item) {
			return invalid("job %q has a type this editor does not handle", original)
		}
		if typeNode := mapGet(item, "type"); typeNode.Value != job.Type {
			return invalid("the type of an existing job cannot be changed (remove it and add a new one)")
		}
	}
	if i := findBackup(sequence, job.Name); i >= 0 && (item == nil || sequence.Content[i] != item) {
		return fmt.Errorf("%q: %w", job.Name, ErrExists)
	}
	if item == nil {
		item = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		sequence.Content = append(sequence.Content, item)
	}

	mapSet(item, "name", scalar(job.Name, 0))
	mapSet(item, "type", scalar(job.Type, 0))
	if job.Schedule == "" {
		mapDelete(item, "schedule")
	} else {
		mapSet(item, "schedule", scalar(job.Schedule, yaml.DoubleQuotedStyle))
	}

	if job.Type == config.BackupFiles {
		mapSet(item, "paths", stringList(job.Paths))
		if len(job.Exclude) == 0 {
			mapDelete(item, "exclude")
		} else {
			mapSet(item, "exclude", stringList(job.Exclude))
		}
	} else {
		editDatabaseBlock(item, job)
	}

	repositories := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, name := range job.Repositories {
		repositories.Content = append(repositories.Content, scalar(name, 0))
	}
	if len(job.Repositories) == 0 {
		mapDelete(item, "repositories") // the config's defaults apply
	} else {
		mapSet(item, "repositories", repositories)
	}

	retention := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
	for _, field := range []struct {
		key   string
		value int
	}{{"last", job.Retention.Last}, {"daily", job.Retention.Daily}, {"weekly", job.Retention.Weekly}, {"monthly", job.Retention.Monthly}, {"yearly", job.Retention.Yearly}} {
		if field.value > 0 {
			retention.Content = append(retention.Content, scalar(field.key, 0), intNode(field.value))
		}
	}
	if len(retention.Content) == 0 {
		mapDelete(item, "retention")
	} else {
		mapSet(item, "retention", retention)
	}
	return nil
}

// editDatabaseBlock writes the job's <type>: { host, port, ... } mapping.
func editDatabaseBlock(item *yaml.Node, job Job) {
	block := mapGet(item, job.Type)
	if block == nil || block.Kind != yaml.MappingNode {
		block = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		block.Style = yaml.FlowStyle
	}
	mapSet(block, "host", scalar(job.Host, 0))
	mapSet(block, "port", intNode(job.Port))
	if job.User == "" {
		mapDelete(block, "user")
	} else {
		mapSet(block, "user", scalar(job.User, 0))
	}
	if job.PasswordEnv != "" {
		mapSet(block, "password", scalar("${"+job.PasswordEnv+"}", yaml.DoubleQuotedStyle))
	} else if !job.PasswordLiteral {
		mapDelete(block, "password")
	}
	mapSet(block, "database", scalar(job.Database, 0))
	switch job.Type {
	case config.BackupMySQL:
		setOrDelete(block, "tls", job.TLS)
	case config.BackupMongoDB:
		setOrDelete(block, "auth_database", job.AuthDatabase)
	}
	mapSet(item, job.Type, block)
}

func setOrDelete(mapping *yaml.Node, key, value string) {
	if value == "" {
		mapDelete(mapping, key)
		return
	}
	mapSet(mapping, key, scalar(value, 0))
}

func removeBackup(root *yaml.Node, name string) error {
	sequence := mapGet(root, "backups")
	if sequence == nil || sequence.Kind != yaml.SequenceNode {
		return fmt.Errorf("job %q: %w", name, ErrNotFound)
	}
	i := findBackup(sequence, name)
	if i < 0 {
		return fmt.Errorf("job %q: %w", name, ErrNotFound)
	}
	if !isEditableJob(sequence.Content[i]) {
		return invalid("job %q has a type this editor does not handle", name)
	}
	sequence.Content = append(sequence.Content[:i], sequence.Content[i+1:]...)
	if len(sequence.Content) == 0 {
		mapDelete(root, "backups")
	}
	return nil
}
