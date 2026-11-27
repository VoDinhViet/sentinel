-- The latest report of each agent: how the machine is and how its backups stand, as the agent sent it
-- (JSON, cleaned of control characters). Only the newest is kept; history comes with the reports.
CREATE TABLE agent_reports (
    agent       TEXT PRIMARY KEY REFERENCES agents (name),
    received_at INTEGER NOT NULL,
    report      TEXT NOT NULL
) STRICT;
