// Package audit is the log of what was done on the control plane: who logged in, which agent joined or
// was revoked, which task was queued. It is append-only (the database refuses to change or delete a
// row) and chained: every row carries a hash of the row before it, so one that is edited, removed or
// slipped in shows in Verify. Each entry is also written to the log, so the journal keeps a second
// copy that a change of the database cannot reach.
package audit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"sentinel/server/internal/store"
)

// Actor is who does something: whoever is signed in (with the address and the first characters of the
// hash of the session), an agent, or the control plane itself.
type Actor struct {
	Name    string // "ctl", "agent:<name>" or "system"
	Session string // the first 8 characters of the hash of the session; empty if none
	IP      string
}

// System is the control plane acting on its own (a timer, a start-up check).
var System = Actor{Name: "system"}

type actorKey struct{}

// WithActor returns a context that says who is acting, for Append to record.
func WithActor(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// ActorFrom is who the context says is acting; System if it says nothing.
func ActorFrom(ctx context.Context) Actor {
	if actor, ok := ctx.Value(actorKey{}).(Actor); ok {
		return actor
	}
	return System
}

// Entry is one thing that was done. Never put a secret in it: it is kept for ever.
type Entry struct {
	Action string // e.g. "login.ok", "agent.enroll"
	Target string // what it was done to, e.g. an agent name; may be empty
	Detail string // a short note; may be empty
}

// Record is a row of the log.
type Record struct {
	ID       int64
	At       time.Time
	Actor    Actor
	Entry    Entry
	PrevHash string
	Hash     string
}

// genesis is the hash "before" the first row.
const genesis = ""

// Append adds an entry, recording the actor of ctx. Call it with the transaction that does the thing
// the entry says, so the two happen together or not at all. The transaction must be one that took the
// write lock when it began (store.InTx), so no other entry is added between reading the last hash and
// writing the next row.
func Append(ctx context.Context, q store.Querier, now time.Time, entry Entry) error {
	actor := ActorFrom(ctx)
	var prev string
	err := q.QueryRowContext(ctx, `SELECT hash FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&prev)
	if errors.Is(err, sql.ErrNoRows) {
		prev = genesis
	} else if err != nil {
		return fmt.Errorf("audit: read last hash: %w", err)
	}
	record := Record{At: now, Actor: actor, Entry: entry, PrevHash: prev}
	record.Hash = hashOf(record)
	if _, err := q.ExecContext(ctx, `INSERT INTO audit_log (at, actor, session, ip, action, target, detail, prev_hash, hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		now.UnixMilli(), actor.Name, actor.Session, actor.IP, entry.Action, entry.Target, entry.Detail, record.PrevHash, record.Hash); err != nil {
		return fmt.Errorf("audit: append: %w", err)
	}
	slog.Info("audit", "actor", actor.Name, "session", actor.Session, "ip", actor.IP, "action", entry.Action, "target", entry.Target, "detail", entry.Detail)
	return nil
}

// hashOf is the hash of a record: SHA-256 of the previous hash and of every field. Each field is
// written with its length in front, so "ab"+"c" and "a"+"bc" cannot give the same bytes.
func hashOf(record Record) string {
	digest := sha256.New()
	for _, field := range []string{
		record.PrevHash,
		strconv.FormatInt(record.At.UnixMilli(), 10),
		record.Actor.Name, record.Actor.Session, record.Actor.IP,
		record.Entry.Action, record.Entry.Target, record.Entry.Detail,
	} {
		var length [binary.MaxVarintLen64]byte
		digest.Write(length[:binary.PutUvarint(length[:], uint64(len(field)))])
		digest.Write([]byte(field))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// List returns the newest records first, at most limit of them.
func List(ctx context.Context, q store.Querier, limit int) ([]Record, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, at, actor, session, ip, action, target, detail, prev_hash, hash
		FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("audit: list: %w", err)
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		record, err := scan(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func scan(rows *sql.Rows) (Record, error) {
	var record Record
	var atMillis int64
	if err := rows.Scan(&record.ID, &atMillis, &record.Actor.Name, &record.Actor.Session, &record.Actor.IP,
		&record.Entry.Action, &record.Entry.Target, &record.Entry.Detail, &record.PrevHash, &record.Hash); err != nil {
		return Record{}, fmt.Errorf("audit: scan: %w", err)
	}
	record.At = time.UnixMilli(atMillis)
	return record, nil
}

// Verify walks the whole log in order and checks the chain. It returns how many rows it checked, and
// an error that names the first row that does not fit: one whose hash is not what its content gives,
// or whose previous hash is not the hash of the row before it (a row was removed or put in between).
func Verify(ctx context.Context, q store.Querier) (checked int, err error) {
	rows, err := q.QueryContext(ctx, `SELECT id, at, actor, session, ip, action, target, detail, prev_hash, hash
		FROM audit_log ORDER BY id`)
	if err != nil {
		return 0, fmt.Errorf("audit: verify: %w", err)
	}
	defer rows.Close()
	prev := genesis
	for rows.Next() {
		record, err := scan(rows)
		if err != nil {
			return checked, err
		}
		if record.PrevHash != prev {
			return checked, fmt.Errorf("audit row %d does not follow the row before it: a row was removed or added", record.ID)
		}
		if hashOf(record) != record.Hash {
			return checked, fmt.Errorf("audit row %d was changed: its hash does not match its content", record.ID)
		}
		prev = record.Hash
		checked++
	}
	return checked, rows.Err()
}
