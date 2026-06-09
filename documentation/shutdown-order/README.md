# Shutdown order

`shutdown-order` (top-level) controls the sequence in which services are stopped during graceful shutdown.

## Config

```yaml
# Graceful shutdown ordering. With reverse-dep (the default), dependents
# stop before the dependencies they rely on.
shutdown-order: reverse-dep
processes:
  - name: db
    command: /bin/sh
    args: ["-c", "trap 'echo db >> STOPLOG; exit 0' TERM; while true; do sleep 0.2; done"]
    on-failure: shutdown
  - name: app
    command: /bin/sh
    args: ["-c", "trap 'echo app >> STOPLOG; exit 0' TERM; while true; do sleep 0.2; done"]
    after: [db]
    on-failure: shutdown
```

The three strategies:

- `reverse-dep` (default) — stop dependents first, then their dependencies. Here `app` stops before `db`.
- `dep` — stop dependencies first, then dependents. `db` would stop before `app`.
- `simultaneous` — signal all services at once.

## Expected behavior

- `db` starts, then `app` (which depends on it).
- On SIGTERM with `reverse-dep`, `app` receives SIGTERM and exits before `db` is signaled.
- gopherd exits 0 once all services have stopped.

## Test

Each service traps SIGTERM and appends its name to a shared log file before exiting. The test replaces the `STOPLOG` token with a temp-dir path, starts the daemon, waits for both services to be running, then sends SIGTERM.

Because `reverse-dep` stops sequentially — signaling a service and waiting for it to exit before moving to the next — the log order is deterministic. The test asserts that the log contains exactly `["app", "db"]`, proving that the dependent stopped before its dependency.

```bash
go test ./documentation/shutdown-order/ -v
```
