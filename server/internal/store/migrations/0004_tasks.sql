-- The tasks the control plane gives its agents, and the log each wrote while it ran. A task is one row
-- that moves queued -> sent -> one of the end states; nothing about it is deleted before it is old.
CREATE TABLE tasks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    agent       TEXT NOT NULL REFERENCES agents (name),
    action      TEXT NOT NULL,
    params      TEXT NOT NULL, -- a JSON object of strings
    state       TEXT NOT NULL CHECK (state IN ('queued', 'sent', 'succeeded', 'failed', 'refused', 'canceled', 'expired')),
    created_at  INTEGER NOT NULL,
    created_by  TEXT NOT NULL, -- "ctl" and the first characters of the session
    expires_at  INTEGER NOT NULL, -- a task still queued then is expired
    sent_at     INTEGER,
    result_by   INTEGER, -- a task still sent then is expired
    started_at  INTEGER,
    finished_at INTEGER,
    output      TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    log_chars   INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX tasks_agent_state ON tasks (agent, state);
CREATE INDEX tasks_finished ON tasks (finished_at);

CREATE TABLE task_logs (
    task INTEGER NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    seq  INTEGER NOT NULL,
    at   INTEGER NOT NULL,
    text TEXT NOT NULL,
    PRIMARY KEY (task, seq)
) STRICT;
