package agent

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/scheduler"
)

// Supervisor runs the daemon and swaps it for a new one when the managed
// config changes, waiting for running backups to finish first.
type Supervisor struct {
	Build func(cfg *config.Config) (*scheduler.Daemon, error)
	// BusyPoll is how often a pending reload re-checks for running backups.
	BusyPoll time.Duration

	current atomic.Pointer[running]
	reload  chan update
}

type running struct {
	Daemon  *scheduler.Daemon
	Version int64
}

type update struct {
	cfg     *config.Config
	version int64
	applied func()
}

func NewSupervisor(build func(*config.Config) (*scheduler.Daemon, error)) *Supervisor {
	return &Supervisor{Build: build, BusyPoll: 5 * time.Second, reload: make(chan update, 1)}
}

// Current returns the running daemon and its managed config version
// (0 = local file). It is nil before Run has built the first daemon.
func (s *Supervisor) Current() (*scheduler.Daemon, int64) {
	r := s.current.Load()
	if r == nil {
		return nil, 0
	}
	return r.Daemon, r.Version
}

// Reload asks Run to switch to cfg. A newer request replaces a pending one.
// applied is called once the new daemon is running.
func (s *Supervisor) Reload(cfg *config.Config, version int64, applied func()) {
	u := update{cfg, version, applied}
	for {
		select {
		case s.reload <- u:
			return
		default:
			select {
			case <-s.reload: // drop the stale pending update
			default:
			}
		}
	}
}

// Run runs daemons until ctx is cancelled.
func (s *Supervisor) Run(ctx context.Context, cfg *config.Config, version int64) error {
	var applied func()
	for {
		d, err := s.Build(cfg)
		if err != nil {
			return err
		}
		s.current.Store(&running{d, version})
		if applied != nil {
			go applied()
			applied = nil
		}
		dctx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- d.Run(dctx) }()

		var u update
		select {
		case err := <-done:
			cancel()
			return err
		case u = <-s.reload:
		}

		// Let running backups finish; a newer config may arrive meanwhile.
		if d.Busy() {
			slog.Info("config changed; waiting for running backup to finish before reloading")
		}
		for d.Busy() {
			select {
			case <-ctx.Done():
				cancel()
				return <-done
			case newer := <-s.reload:
				u = newer
			case <-time.After(s.BusyPoll):
			}
		}
		cancel()
		if err := <-done; err != nil {
			slog.Warn("daemon stopped with error during reload", "error", err)
		}
		cfg, version, applied = u.cfg, u.version, u.applied
		slog.Info("reloading with new configuration", "version", version)
	}
}
