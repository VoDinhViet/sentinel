// Package configedit adds, changes and removes backup jobs in a Sentinel config
// file without disturbing the rest of it: comments, key order and keys it does
// not manage are kept. Passwords are never entered here; a job refers to an
// environment variable, written into the file as ${NAME}.
package configedit

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"sort"
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
	DatabaseTypes = []string{config.JobPostgres, config.JobMySQL, config.JobMongoDB}
	JobTypes      = append(slices.Clone(DatabaseTypes), config.JobFiles)
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
	envRef    = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)
	envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	nameRe    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)
	hostRe    = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,255}$`)
	identRe   = regexp.MustCompile(`^[^\x00-\x1f\x7f]{1,128}$`)
)

func defaultPort(jobType string) int {
	switch jobType {
	case config.JobMySQL:
		return 3306
	case config.JobMongoDB:
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
	for _, cfgJob := range cfg.Jobs {
		listing.Jobs = append(listing.Jobs, jobFromConfig(cfgJob))
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

func jobFromConfig(cfgJob config.Job) Job {
	job := Job{Name: cfgJob.Name, Type: cfgJob.Type, Schedule: cfgJob.Schedule, Repositories: cfgJob.Repositories, Retention: cfgJob.Retention, Inherited: cfgJob.Inherited,
		Paths: cfgJob.Paths, Exclude: cfgJob.Excludes}
	if cfgJob.Inherited.Schedule {
		job.Schedule = ""
	}
	if cfgJob.Inherited.Repositories {
		job.Repositories = nil
	}
	if cfgJob.Inherited.Retention {
		job.Retention = config.Retention{}
	}
	var password string
	switch {
	case cfgJob.Postgres != nil:
		pg := cfgJob.Postgres
		job.Database, job.Host, job.Port, job.User, password = pg.Database, pg.Host, pg.Port, pg.User, pg.Password
	case cfgJob.MySQL != nil:
		my := cfgJob.MySQL
		job.Database, job.Host, job.Port, job.User, password, job.TLS = my.Database, my.Host, my.Port, my.User, my.Password, my.TLS
	case cfgJob.MongoDB != nil:
		mongo := cfgJob.MongoDB
		job.Database, job.Host, job.Port, job.User, password, job.AuthDatabase = mongo.Database, mongo.Host, mongo.Port, mongo.User, mongo.Password, mongo.AuthDatabase
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
	if !nameRe.MatchString(job.Name) {
		add("name", "invalid job name %q: use letters, digits, '.', '_' or '-'", job.Name)
	}
	if job.Type == config.JobFiles {
		return append(problems, append(filesProblems(job), commonProblems(job, known)...)...)
	}
	if !identRe.MatchString(job.Database) {
		add("database", "database name is required")
	}
	if job.User != "" && !identRe.MatchString(job.User) {
		add("user", "invalid user")
	}
	if job.Host == "" {
		job.Host = "localhost"
	}
	if !hostRe.MatchString(job.Host) {
		add("host", "invalid host %q", job.Host)
	}
	if job.Port == 0 {
		job.Port = defaultPort(job.Type)
	}
	if job.Port < 1 || job.Port > 65535 {
		add("port", "port must be between 1 and 65535")
	}
	if job.PasswordEnv != "" && !envNameRe.MatchString(job.PasswordEnv) {
		add("password_env", "invalid environment variable name %q (the password itself is never entered here)", job.PasswordEnv)
	}
	if job.Type == config.JobMySQL && job.TLS != "" && job.TLS != "disabled" && job.TLS != "required" {
		add("tls", "tls must be empty, disabled or required")
	}
	if job.Type != config.JobMySQL {
		job.TLS = ""
	}
	if job.Type != config.JobMongoDB {
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
	for _, count := range []int{job.Retention.Last, job.Retention.Daily, job.Retention.Weekly, job.Retention.Monthly, job.Retention.Yearly} {
		if count < 0 {
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
		return editJob(root, original, job)
	})
}

// Delete removes a database job from the file. Its snapshots stay in the repositories.
func Delete(path, jobName string) error {
	return editFile(path, func(root *yaml.Node, _ *config.Config) error { return removeJob(root, jobName) })
}

func findJob(seq *yaml.Node, jobName string) int {
	for i, node := range seq.Content {
		if node.Kind != yaml.MappingNode {
			continue
		}
		if nameNode := mapGet(node, "name"); nameNode != nil && nameNode.Value == jobName {
			return i
		}
	}
	return -1
}

func isEditableJob(node *yaml.Node) bool {
	typeNode := mapGet(node, "type")
	return typeNode != nil && slices.Contains(JobTypes, typeNode.Value)
}

func editJob(root *yaml.Node, original string, job Job) error {
	seq := mapGet(root, "jobs")
	if seq == nil {
		seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		root.Content = append(root.Content, scalar("jobs", 0), seq)
	}
	if seq.Kind != yaml.SequenceNode {
		return invalid("config: jobs must be a list")
	}
	var jobNode *yaml.Node
	if original != "" {
		i := findJob(seq, original)
		if i < 0 {
			return fmt.Errorf("job %q: %w", original, ErrNotFound)
		}
		jobNode = seq.Content[i]
		if !isEditableJob(jobNode) {
			return invalid("job %q has a type this editor does not handle", original)
		}
		if typeNode := mapGet(jobNode, "type"); typeNode.Value != job.Type {
			return invalid("the type of an existing job cannot be changed (remove it and add a new one)")
		}
	}
	if i := findJob(seq, job.Name); i >= 0 && (jobNode == nil || seq.Content[i] != jobNode) {
		return fmt.Errorf("%q: %w", job.Name, ErrExists)
	}
	if jobNode == nil {
		jobNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		seq.Content = append(seq.Content, jobNode)
	}

	mapSet(jobNode, "name", scalar(job.Name, 0))
	mapSet(jobNode, "type", scalar(job.Type, 0))
	if job.Schedule == "" {
		mapDelete(jobNode, "schedule")
	} else {
		mapSet(jobNode, "schedule", scalar(job.Schedule, yaml.DoubleQuotedStyle))
	}

	if job.Type == config.JobFiles {
		mapSet(jobNode, "paths", stringList(job.Paths))
		if len(job.Exclude) == 0 {
			mapDelete(jobNode, "exclude")
		} else {
			mapSet(jobNode, "exclude", stringList(job.Exclude))
		}
	} else {
		editDatabaseBlock(jobNode, job)
	}

	repositories := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, name := range job.Repositories {
		repositories.Content = append(repositories.Content, scalar(name, 0))
	}
	if len(job.Repositories) == 0 {
		mapDelete(jobNode, "repositories") // the config's defaults apply
	} else {
		mapSet(jobNode, "repositories", repositories)
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
		mapDelete(jobNode, "retention")
	} else {
		mapSet(jobNode, "retention", retention)
	}
	return nil
}

// editDatabaseBlock writes the job's <type>: { host, port, ... } mapping.
func editDatabaseBlock(jobNode *yaml.Node, job Job) {
	dbNode := mapGet(jobNode, job.Type)
	if dbNode == nil || dbNode.Kind != yaml.MappingNode {
		dbNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		dbNode.Style = yaml.FlowStyle
	}
	mapSet(dbNode, "host", scalar(job.Host, 0))
	mapSet(dbNode, "port", intNode(job.Port))
	if job.User == "" {
		mapDelete(dbNode, "user")
	} else {
		mapSet(dbNode, "user", scalar(job.User, 0))
	}
	if job.PasswordEnv != "" {
		mapSet(dbNode, "password", scalar("${"+job.PasswordEnv+"}", yaml.DoubleQuotedStyle))
	} else if !job.PasswordLiteral {
		mapDelete(dbNode, "password")
	}
	mapSet(dbNode, "database", scalar(job.Database, 0))
	switch job.Type {
	case config.JobMySQL:
		setOrDelete(dbNode, "tls", job.TLS)
	case config.JobMongoDB:
		setOrDelete(dbNode, "auth_database", job.AuthDatabase)
	}
	mapSet(jobNode, job.Type, dbNode)
}

func removeJob(root *yaml.Node, jobName string) error {
	seq := mapGet(root, "jobs")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return fmt.Errorf("job %q: %w", jobName, ErrNotFound)
	}
	i := findJob(seq, jobName)
	if i < 0 {
		return fmt.Errorf("job %q: %w", jobName, ErrNotFound)
	}
	if !isEditableJob(seq.Content[i]) {
		return invalid("job %q has a type this editor does not handle", jobName)
	}
	seq.Content = append(seq.Content[:i], seq.Content[i+1:]...)
	if len(seq.Content) == 0 {
		mapDelete(root, "jobs")
	}
	return nil
}
