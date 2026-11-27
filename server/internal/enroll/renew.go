package enroll

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"sentinel/server/internal/audit"
)

// RenewWindow is how close to its end a certificate must be before its agent may ask for a new one. An
// agent asks at 30 days; the window is wider so a clock that runs a little off still gets its answer.
const RenewWindow = 45 * 24 * time.Hour

// ErrTooEarly means the agent's certificate has more than RenewWindow left.
var ErrTooEarly = errors.New("the certificate has plenty of time left: renew when less than 45 days remain")

// Renew signs a new certificate for the agent called name, for the key in csrPEM, when the certificate
// it shows (valid until current) is within RenewWindow of its end. The old certificate stays valid until
// it ends, so an agent that crashes before it saves the new one is not locked out. Revoking the agent
// ends them all.
func (s *Service) Renew(ctx context.Context, name string, current time.Time, csrPEM []byte) (Joined, error) {
	now := time.Now()
	if current.Sub(now) > RenewWindow {
		return Joined{}, ErrTooEarly
	}
	var renewed Joined
	err := s.store.InTx(ctx, func(tx *sql.Tx) error {
		var state string
		switch err := tx.QueryRowContext(ctx, `SELECT state FROM agents WHERE name = ?`, name).Scan(&state); {
		case errors.Is(err, sql.ErrNoRows):
			return ErrCertRefused
		case err != nil:
			return fmt.Errorf("read agent: %w", err)
		case state != "active":
			return ErrCertRefused
		}
		issued, err := s.ca.SignAgent(csrPEM, name, now)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrBadRequest, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO certs (serial, agent, issued_at, not_after) VALUES (?, ?, ?, ?)`,
			issued.Serial, name, now.Unix(), issued.NotAfter.Unix()); err != nil {
			return fmt.Errorf("store certificate: %w", err)
		}
		renewed = Joined{Name: name, CertPEM: issued.CertPEM, CAPEM: s.ca.CertPEM(), NotAfter: issued.NotAfter}
		actor := audit.WithActor(ctx, audit.Actor{Name: "agent:" + name, IP: audit.ActorFrom(ctx).IP})
		return audit.Append(actor, tx, now, audit.Entry{Action: "agent.renew", Target: name, Detail: "certificate " + issued.Serial})
	})
	return renewed, err
}
