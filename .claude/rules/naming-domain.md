---
paths:
  - "cmd/**"
  - "internal/**"
---

# Domain naming (Sentinel)

Complements the user's global naming rules (name a value after what it holds; plural for query results, singular for
callback params; `xByY` maps; no generic `rows`/`data`/`result`/`out`). The migration to these names is in progress, so code
you touch should already use them.

**Short names, abbreviations welcome.** Use the shortest name that still says what the value is. Prefer the usual
abbreviation of the thing over spelling it out: `repo`, `cfg`, `opts`, `msg`, `snap`/`snaps`, `st` (store), `sum`, `dur`,
`ver`, `errs`. The package gives context (`store.Prune`, not `store.PruneCheckHistory`). Rename only when the old name is
wrong or means two things; never lengthen a name for its own sake. An abbreviation must still name the entity: generic
shapes (`r`, `x`, `v`, `row`, `data`, `result`, `list`, `items`) stay banned.

## Terms: one word per concept

| Concept | Use | Not |
|---|---|---|
| Configured backup unit | `job` (`config.Job`, YAML `jobs:`) | `backup` for the config, `task` |
| One execution of a job into one repo | `BackupRun` | `result`, `entry` |
| restic copy; its id string | `snap` or `snapshot`; `snapID` | `snapshot` for a string |
| restic repository | `repo` in Go identifiers; `repository` in YAML, CLI, docs, DB columns | mixing both in code |
| Repo integrity check | `verify` (`Restic.Verify`, alert key `verify:`) | `check` |
| Uptime | check (config) → `probe` (run it) → `CheckResult` (DB row); address is `endpoint` | `monitor`, `target` |
| Restore destination | `restoreTarget` (flag `--target` stays) | bare `target` |
| Job with no recent success | `stale` (alert key `stale:`) | `backup-stale` |
| Where messages are sent | `notifier` | `channel`, `sender` |
| `sentinel doctor` output | `finding` | `check` |
| Keeping snapshots | `retention` (only this meaning) | for check history |
| The `sentinel run` process | `daemon` (`internal/daemon`) | `scheduler` |

One entity, one name per layer: config `config.Job` / `config.UptimeCheck`; run-time `uptime.Result`, `restic.Snapshot`;
stored `store.BackupRun` / `store.CheckResult` / `store.Alert`; report `status.Report`; TUI form `configedit.Job`.
Never reuse one name for two layers in the same scope.

## Verbs

`Load` reads and parses a file; `Parse` takes bytes or a string; `Open` opens a resource; `New…` is a constructor; `Record…`
appends a history row; `Save…` upserts state or a file; `Find…` returns one or an error; `List…` returns many; `Run…` executes.

## Go and CLI

- Do not shadow package names (`backup`, `log`, `url`) with variables.
- Cobra: `newXxxCmd(app)` per command; the parent command variable is `xxxCmd`, so a child's `RunE(cmd, …)` does not shadow it.
- Bubbletea messages are `<thing><past tense>Msg` (`reportLoadedMsg`); tabs are named constants, never bare `0`/`2`.
- Short names that stay fine: `err`, `ctx`, `i`/`j` in short loops, 1–2 letter receivers, cobra `cmd`/`args`, `t` in tests, `tx`.
- User-facing contract (YAML keys, CLI names and flags, `status -o json`, SQLite schema, restic tag `job=<name>`, dump file
  names) changes only on purpose, with the README and `deploy/` examples updated in the same commit.
