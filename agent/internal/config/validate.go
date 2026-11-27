package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"sentinel/agent/internal/schedule"
	"sentinel/pkg/yamlenv"
)

// validate reports every problem in the config at once: the rules of single fields first (see
// fields.go), then the ones that need the whole file.
func (c *Config) validate() error {
	errs := fieldErrors(c)
	errs = append(errs, c.repositoryErrors()...)
	errs = append(errs, c.jobErrors()...)
	errs = append(errs, c.scheduleErrors()...)
	errs = append(errs, c.uptimeErrors()...)
	return errors.Join(errs...)
}

// repositoryErrors checks the env a repository passes to restic: only variable names, and none that
// would change which programs restic runs.
func (c *Config) repositoryErrors() []error {
	var errs []error
	for name, repo := range c.Repositories {
		for _, varName := range slices.Sorted(maps.Keys(repo.Env)) {
			switch {
			case !yamlenv.ValidName(varName):
				errs = append(errs, fmt.Errorf("repository %q: env %q is not a variable name", name, varName))
			case yamlenv.IsDangerousName(varName):
				errs = append(errs, fmt.Errorf("repository %q: env %s cannot be set: it changes which programs restic runs", name, varName))
			}
		}
	}
	return errs
}

func (c *Config) jobErrors() []error {
	var errs []error
	seen := map[string]bool{}
	for i, job := range c.Jobs {
		where := c.entryWhere("jobs", i)
		if strings.ContainsAny(job.Name, ",\x00\n\r") {
			// restic reads a comma in --tag as "all of these tags", so a job
			// named "a,b" would never match its own snapshots.
			errs = append(errs, fmt.Errorf("%s: name must not contain commas or line breaks", where))
		}
		if job.Name != "" && seen[job.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate name", where))
		}
		seen[job.Name] = true
		if job.Type == JobFiles && len(job.Paths) == 0 {
			errs = append(errs, fmt.Errorf("%s: paths is required", where))
		}
		if rule := job.Retention.negativeRule(); rule != "" && !job.Inherited.Retention {
			errs = append(errs, fmt.Errorf("%s: retention.%s must not be negative", where, rule))
		}
		errs = append(errs, c.jobRepositoryErrors(where, job)...)
	}
	return errs
}

func (c *Config) jobRepositoryErrors(where string, job Job) []error {
	var errs []error
	if len(job.Repositories) == 0 {
		errs = append(errs, fmt.Errorf("%s: at least one repository is required: list repositories: on the job or set defaults.repositories (known: %s)",
			where, strings.Join(slices.Sorted(maps.Keys(c.Repositories)), ", ")))
	}
	for _, repoName := range job.Repositories {
		if _, ok := c.Repositories[repoName]; !ok {
			errs = append(errs, fmt.Errorf("%s: unknown repository %q", where, repoName))
		}
	}
	return errs
}

// scheduleErrors checks every schedule (a job's, the default and the repository check's), the default
// retention and the default repositories.
func (c *Config) scheduleErrors() []error {
	var errs []error
	for i, job := range c.Jobs {
		if err := checkSchedule(job.Schedule); err != nil {
			errs = append(errs, fmt.Errorf("%s: invalid schedule: %w", c.entryWhere("jobs", i), err))
		}
	}
	if err := checkSchedule(c.Defaults.Schedule); err != nil {
		errs = append(errs, fmt.Errorf("defaults.schedule: %w", err))
	}
	if err := checkSchedule(c.VerifySchedule); err != nil {
		errs = append(errs, fmt.Errorf("verify_schedule: %w", err))
	}
	if err := checkSchedule(c.PruneSchedule); err != nil {
		errs = append(errs, fmt.Errorf("prune_schedule: %w", err))
	}
	if rule := c.Defaults.Retention.negativeRule(); rule != "" {
		errs = append(errs, fmt.Errorf("defaults.retention.%s must not be negative", rule))
	}
	for _, name := range c.Defaults.Repositories {
		if _, ok := c.Repositories[name]; !ok {
			errs = append(errs, fmt.Errorf("defaults.repositories: unknown repository %q", name))
		}
	}
	return errs
}

// checkSchedule is nil for a schedule that parses and for "manual" (and an empty one).
func checkSchedule(spec string) error {
	if schedule.IsManual(spec) {
		return nil
	}
	_, err := schedule.Parse(spec)
	return err
}

func (c *Config) uptimeErrors() []error {
	var errs []error
	seen := map[string]bool{}
	for i, check := range c.UptimeChecks {
		if check.Name != "" && seen[check.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate name", c.entryWhere("uptime", i)))
		}
		seen[check.Name] = true
	}
	return errs
}
