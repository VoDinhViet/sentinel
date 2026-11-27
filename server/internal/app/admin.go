package app

import (
	"context"
	"time"

	"sentinel/server/internal/audit"
)

// Added is a new agent and what it is given to join.
type Added struct {
	Name        string
	Token       string // shown once
	Expires     time.Time
	JoinURL     string
	Fingerprint string // of the CA
}

// AddAgent names a new agent and makes its join token.
func (a *App) AddAgent(ctx context.Context, name string) (Added, error) {
	token, expires, err := a.Enroll.Create(ctx, name)
	if err != nil {
		return Added{}, err
	}
	return Added{Name: name, Token: token, Expires: expires, JoinURL: a.Config.JoinURL(), Fingerprint: a.CA.Fingerprint()}, nil
}

// Audit is the newest records of the audit log, newest first.
func (a *App) Audit(ctx context.Context, limit int) ([]audit.Record, error) {
	return audit.List(ctx, a.Store.DB, limit)
}

// VerifyAudit checks the hash chain of the audit log.
func (a *App) VerifyAudit(ctx context.Context) (checked int, err error) {
	return audit.Verify(ctx, a.Store.DB)
}
