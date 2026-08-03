# requires: hard dependencies

`after:` and `before:` are pure start ordering. `requires:` adds failure
coupling on top of the ordering: when a required service **fails**, its
running dependents are stopped too — systemd `Requires=` semantics.

## Config

```yaml
processes:
  - name: db
    command: /usr/local/bin/db
    args: ["300"]
    on-failure: ignore

  - name: web
    command: /usr/local/bin/web
    args: ["300"]
    requires: [db]
    on-failure: ignore
```

- `requires: [db]` implies `after: [db]` ordering — `web` starts once `db`
  is up (and past its readiness gate, if any).
- When `db` exits non-zero, gopherd stops `web` with its configured
  `stop-signal` / `kill-delay`.
- Dependents are **not** restarted when the dependency recovers: like
  systemd, gopherd does not track a "was stopped because of X" edge. `web`
  stays stopped until started manually (control socket) or by its own
  `on-failure` policy on a later crash of its own.
- A *clean* stop of `db` (control-socket `stop`, or exit 0) does not touch
  dependents — only failure does.

## Expected behavior

- `db` and `web` start in order; killing `db` (a failure) takes `web` down.
- Restarting `db` alone leaves `web` stopped; `web` must be started
  explicitly.

## Test

Run level. The test kills `db` with SIGKILL via the control socket and
asserts `web` is stopped as a consequence, then restarts `db` and verifies
`web` stays down until started manually. SIGTERM then yields a clean exit 0.

```bash
go test ./documentation/requires/ -v
```
