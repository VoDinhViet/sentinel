package app

import "errors"

var (
	// ErrNotFound is wrapped by the error for a job or repository the config does not have.
	ErrNotFound = errors.New("not found")
	// ErrRunning is wrapped by the error for a backup that cannot start because the job is
	// already being backed up.
	ErrRunning = errors.New("already running")
)
