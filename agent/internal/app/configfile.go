package app

import "sentinel/agent/internal/configedit"

// The config file: what the manager shows and edits. Each call reads or writes the file on
// disk, so a change made by hand a minute ago is seen.

// ListJobs is the jobs in the config file and what they inherit from the defaults.
func (s *Service) ListJobs() (configedit.Listing, error) { return configedit.List(s.configPath) }

// SaveJob adds a job, or replaces the one named original.
func (s *Service) SaveJob(original string, job configedit.Job) error {
	return configedit.Save(s.configPath, original, job)
}

// DeleteJob removes a job.
func (s *Service) DeleteJob(name string) error { return configedit.Delete(s.configPath, name) }

// Overview is the rest of the config file: repositories, uptime checks, notifiers, settings.
func (s *Service) Overview() (configedit.Overview, error) { return configedit.Read(s.configPath) }

// SaveRepository adds a repository, or replaces the one named original.
func (s *Service) SaveRepository(original string, repo configedit.Repository) error {
	return configedit.SaveRepository(s.configPath, original, repo)
}

// DeleteRepository removes a repository.
func (s *Service) DeleteRepository(name string) error {
	return configedit.DeleteRepository(s.configPath, name)
}

// SaveUptime adds an uptime check, or replaces the one named original.
func (s *Service) SaveUptime(original string, check configedit.Uptime) error {
	return configedit.SaveUptime(s.configPath, original, check)
}

// DeleteUptime removes an uptime check.
func (s *Service) DeleteUptime(name string) error { return configedit.DeleteUptime(s.configPath, name) }

// SaveNotifier adds or replaces the notifier of its kind.
func (s *Service) SaveNotifier(notifier configedit.Notifier) error {
	return configedit.SaveNotifier(s.configPath, notifier)
}

// DeleteNotifier removes the notifier of that kind.
func (s *Service) DeleteNotifier(kind string) error {
	return configedit.DeleteNotifier(s.configPath, kind)
}

// SaveSettings writes the general settings.
func (s *Service) SaveSettings(settings configedit.Settings) error {
	return configedit.SaveSettings(s.configPath, settings)
}
