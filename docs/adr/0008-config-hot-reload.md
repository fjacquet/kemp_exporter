# 0008. Config hot reload

- **Status:** accepted
- **Date:** 2026-07-31
- **Deciders:** Frederic Jacquet

## Context and problem statement

Adding, removing, or re-credentialing a LoadMaster target (`systems` in
`config.yaml`) is an operation an operator will want to perform without a process
restart, especially where the exporter runs under systemd on a host with several
dependent scrape targets. The mechanism for detecting and applying that change needs
to be reliable against common editor behavior and must never leave the process in a
half-updated or crashed state because of a config typo.

## Considered options

- SIGHUP only: an explicit, operator-triggered reload signal, no automatic file
  watching.
- File-watch only: an `fsnotify` watch on the config file itself, triggering reload
  automatically on any write.
- SIGHUP **plus** file-watch, watching the config file's containing directory rather
  than the file's inode, with a reload path that never takes the process down on a
  bad file.

## Decision outcome

Chosen option: **"SIGHUP plus directory watch, fail-safe reload"**.

Both trigger paths are kept because they serve different operator workflows:
`systemctl reload kemp_exporter` (wired to SIGHUP via `ExecReload` in the systemd
unit) gives an explicit, auditable reload action for change-managed environments,
while the `fsnotify` watch means a `docker cp`'d or directly-edited config file picks
up automatically in workflows without a reload command in the loop.

**Watch the directory, not the inode.** Common editors and `mv`-based deployment
(`vim`, `kubectl cp` via a temp file plus rename, `envsubst > config.yaml.tmp &&
mv config.yaml.tmp config.yaml`) do not modify the target file in place — they write
a new inode and rename it over the old path. An `fsnotify` watch registered on the
file's inode detaches the moment that rename happens: the watch silently stops
firing for all subsequent writes, and the exporter would appear to have working hot
reload right up until the first rename-based edit quietly breaks it. Watching the
file's parent **directory** and filtering events by filename avoids this: a rename
that replaces the watched file is just another directory event, which is still
delivered. See `internal/config/watcher.go`'s doc comment for the concrete
mechanism.

**A bad file never takes the process down.** A reload triggered by either path
re-parses the target config file; if parsing or validation fails, the watcher logs
the error and keeps running the last-known-good configuration rather than
propagating the failure into a panic or process exit. An operator who ships a typo'd
config learns about it from the log, not from a monitoring alert about the exporter
disappearing entirely.

Concurrency: `onReload` (config parse plus client rebuild) is never invoked
concurrently with itself — a SIGHUP arriving mid-reload from a file-watch event, or
vice versa, is serialized rather than racing. `Close` blocks until the watch-loop
goroutine has fully exited, so a caller can safely tear down state `onReload` touches
immediately after `Close` returns, with no risk of a reload callback firing against
already-freed state.

### Consequences

- Good — target set changes (add/remove/re-credential a LoadMaster) apply without a
  process restart, so the collection loop's uptime and cached transport-detection
  state for unaffected systems are preserved across a config change.
- Good — the directory-watch strategy survives the common rename-based config
  deployment pattern that would silently break a naive file-inode watch.
- Good — a malformed config file is a logged warning, not an outage: the exporter
  keeps serving the last-known-good target set.
- Neutral — SIGHUP and file-watch triggers are serialized against each other, so a
  reload storm (rapid successive edits) coalesces via the debounce timer in
  `scheduleReload` rather than each edit triggering its own independent client
  rebuild.
- Bad — there is no explicit "reload succeeded/failed" status exposed as a metric;
  an operator must consult logs to confirm a reload took effect. This is a candidate
  follow-up, not addressed in this ADR's scope.
