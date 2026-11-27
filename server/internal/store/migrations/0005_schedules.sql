-- Schedules give an agent a task on a cron timetable, and maintenance windows say when an agent (or all of
-- them) is expected to be away, so that its silence is not an alert.
CREATE TABLE schedules (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    agent       TEXT NOT NULL REFERENCES agents (name),
    action      TEXT NOT NULL,
    params      TEXT NOT NULL, -- a JSON object of strings
    spec        TEXT NOT NULL, -- cron: five fields, or @daily and the like, with an optional CRON_TZ= first
    created_at  INTEGER NOT NULL,
    created_by  TEXT NOT NULL,
    last_run_at INTEGER, -- the time of the last run that was due, whether or not a task could be queued
    last_task   INTEGER, -- the task that run queued
    last_error  TEXT NOT NULL DEFAULT '' -- why that run queued none
) STRICT;

CREATE INDEX schedules_agent ON schedules (agent);

CREATE TABLE maintenance (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    agent      TEXT, -- NULL: every agent
    starts_at  INTEGER NOT NULL,
    ends_at    INTEGER NOT NULL CHECK (ends_at > starts_at),
    reason     TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    created_by TEXT NOT NULL
) STRICT;

CREATE INDEX maintenance_ends ON maintenance (ends_at);
