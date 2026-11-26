// Package app is what Sentinel does for a person or a program that asks: start and stop a
// backup, list snapshots, restore, read and edit the config file, run the doctor. The CLI,
// the terminal manager and the HTTP API all call it, so none of them holds business logic.
package app

import (
	"context"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/status"
	"sentinel/internal/store"
)

// Service works on one loaded config, the store beside it, and the config file it came from.
type Service struct {
	cfg        *config.Config
	st         *store.Store
	configPath string
}

// New returns the service for a loaded config. configPath is the file cfg was read from; the
// methods that edit the config write to it.
func New(cfg *config.Config, st *store.Store, configPath string) *Service {
	return &Service{cfg: cfg, st: st, configPath: configPath}
}

// Status is the report of `sentinel status`: jobs, uptime checks, open alerts, running backups.
func (s *Service) Status(ctx context.Context, now time.Time) (status.Report, error) {
	return status.Build(ctx, s.cfg, s.st, now)
}
