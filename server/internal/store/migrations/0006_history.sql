-- What agents report, kept as history: a sample of the host at most every five minutes, and every backup run an
-- agent has said it finished. The latest report of each agent is in agent_reports; these are what came before.
CREATE TABLE host_samples (
    agent        TEXT NOT NULL REFERENCES agents (name),
    at           INTEGER NOT NULL,
    cpu_percent  REAL NOT NULL,
    load1        REAL NOT NULL,
    mem_percent  REAL NOT NULL, -- used, of the total
    disk_percent REAL NOT NULL, -- used, of the fullest disk
    PRIMARY KEY (agent, at)
) STRICT;

CREATE TABLE backup_runs (
    agent       TEXT NOT NULL REFERENCES agents (name),
    job         TEXT NOT NULL,
    repository  TEXT NOT NULL,
    finished_at INTEGER NOT NULL,
    success     INTEGER NOT NULL CHECK (success IN (0, 1)),
    result      TEXT NOT NULL,
    bytes_added INTEGER NOT NULL,
    PRIMARY KEY (agent, job, repository, finished_at)
) STRICT;

CREATE INDEX backup_runs_finished ON backup_runs (finished_at);
