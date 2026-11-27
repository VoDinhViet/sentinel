-- The core of the control plane: who the agents are, how they join, who may log in, and the audit log.
-- Times are unix seconds, except audit_log.at (milliseconds).

CREATE TABLE agents (
    name        TEXT PRIMARY KEY,
    state       TEXT NOT NULL CHECK (state IN ('pending', 'active', 'revoked')),
    created_at  INTEGER NOT NULL,
    enrolled_at INTEGER,
    last_seen   INTEGER,
    version     TEXT NOT NULL DEFAULT ''
) STRICT;

-- A join token is shown once; only its SHA-256 is kept.
CREATE TABLE join_tokens (
    token_hash TEXT PRIMARY KEY,
    agent      TEXT NOT NULL REFERENCES agents (name),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    used_at    INTEGER
) STRICT;

-- Every certificate the CA signed for an agent. A revoked serial is refused on every request.
CREATE TABLE certs (
    serial     TEXT PRIMARY KEY,
    agent      TEXT NOT NULL REFERENCES agents (name),
    issued_at  INTEGER NOT NULL,
    not_after  INTEGER NOT NULL,
    revoked_at INTEGER
) STRICT;
CREATE INDEX certs_agent ON certs (agent);

-- The one administrator: a password and nothing else. Exactly one row.
CREATE TABLE admin (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    password_hash TEXT NOT NULL,
    changed_at    INTEGER NOT NULL
) STRICT;

-- A session token is shown once; only its SHA-256 is kept.
CREATE TABLE sessions (
    token_hash   TEXT PRIMARY KEY,
    ip           TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
) STRICT;

-- Wrong passwords in a row from one address, and until when the address is blocked.
CREATE TABLE login_failures (
    ip            TEXT PRIMARY KEY,
    failures      INTEGER NOT NULL,
    last_at       INTEGER NOT NULL,
    blocked_until INTEGER NOT NULL DEFAULT 0
) STRICT;

-- Append-only, and chained: each row's hash covers the one before it, so a row that is changed,
-- removed or inserted shows in `sentinel-server audit verify`.
CREATE TABLE audit_log (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    at        INTEGER NOT NULL,
    actor     TEXT NOT NULL,
    session   TEXT NOT NULL,
    ip        TEXT NOT NULL,
    action    TEXT NOT NULL,
    target    TEXT NOT NULL,
    detail    TEXT NOT NULL,
    prev_hash TEXT NOT NULL,
    hash      TEXT NOT NULL
) STRICT;
CREATE INDEX audit_log_action_at ON audit_log (action, at);

CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;

CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;

-- The state of the alerts the control plane raises (an agent that went quiet, a certificate that
-- ends), in the shape package alert stores.
CREATE TABLE alerts (
    key           TEXT PRIMARY KEY,
    firing        INTEGER NOT NULL,
    since         INTEGER NOT NULL,
    last_notified INTEGER NOT NULL,
    message       TEXT NOT NULL,
    pending_level INTEGER,
    pending_title TEXT NOT NULL DEFAULT '',
    pending_body  TEXT NOT NULL DEFAULT ''
) STRICT;
