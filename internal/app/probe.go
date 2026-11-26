package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/configedit"
	"sentinel/internal/doctor"
	"sentinel/internal/engine"
	"sentinel/internal/notify"
	"sentinel/internal/store"
)

// connectTimeout is how long a connection test waits for the database to answer.
const connectTimeout = 10 * time.Second

// Doctor runs the same checks as `sentinel doctor`.
func (s *Service) Doctor(ctx context.Context) []doctor.Finding {
	return doctor.Run(ctx, s.cfg, s.configPath, func(cfg *config.Config) (*store.Store, error) { return store.Open(cfg.StorePath) })
}

// TestConnection tries to reach the database of a database job as the form holds it. original is
// the name the job has in the config file ("" for a new job): a password written in the file is
// taken from there. It returns how long the database took to answer.
func (s *Service) TestConnection(ctx context.Context, job configedit.Job, original string) (time.Duration, error) {
	config.RefreshEnv(s.configPath)
	saved := ""
	if job.PasswordLiteral && original != "" {
		saved = configedit.SavedPassword(s.configPath, original)
	}
	probeJob, err := configedit.ConnectionJob(job, saved, os.LookupEnv)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	start := time.Now()
	err = engine.Probe(ctx, probeJob, s.cfg.EnvRefs)
	return time.Since(start), err
}

// TestNotifier sends one test message through the notifier of that kind, as the config file has
// it now (a notifier added a minute ago works without restarting). Only that notifier's own
// ${VAR}s need to be set.
func (s *Service) TestNotifier(ctx context.Context, kind string) error {
	config.RefreshEnv(s.configPath)
	only, err := config.LoadNotifier(s.configPath, kind)
	if err != nil {
		return err
	}
	multi := notify.NewMulti(only)
	if len(multi.Notifiers) == 0 {
		return fmt.Errorf("notifier %s is not in the config file", kind)
	}
	multi.Attempts = 1
	errs := multi.SendEach(ctx, notify.Message{
		Level: notify.Info, Server: s.cfg.ServerName, Title: "Test notification",
		Body: "Sent from `sentinel manage` at " + time.Now().Format(time.RFC3339),
	})
	return errors.Join(errs...)
}
