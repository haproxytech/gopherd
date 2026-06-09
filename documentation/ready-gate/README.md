# Readiness gate

Hold a dependent service until a check passes. `ready-check` blocks the start of dependents until the named check reports healthy.

## Config

```yaml
# Readiness gate: hold a dependent until a check passes.
processes:
  - name: db
    command: /usr/local/bin/db
    args: ["300"]
    on-failure: shutdown

  - name: app
    command: /usr/local/bin/app
    args: ["300"]
    after: [db]
    ready-check: db-ready
    ready-timeout: 10s
    on-failure: shutdown

checks:
  db-ready:
    exec:
      command: /bin/true
      args: []
    period: 300ms
    threshold: 1
    level: ready
```

- `after: [db]` orders `app` to start after `db`.
- `ready-check: db-ready` gates `app`: it does not start until the `db-ready` check passes.
- `ready-timeout` bounds the wait; if the check never passes, gopherd exits non-zero.
- `level: ready` marks the check as a readiness gate (vs. an ongoing liveness probe).

## Expected behavior

- gopherd starts `db`, then runs the `db-ready` check.
- `app` stays unstarted until `db-ready` reports healthy, then starts.
- Both report `running` once the gate opens. A failing check that never passes trips `ready-timeout` and gopherd exits non-zero.

## Test

Run level. The test substitutes `/usr/bin/sleep` for both `db` and `app`. Because `app` carries a `ready-check` gate, a `running` `app` proves the gate opened after `db-ready` passed; the test asserts both `db` and `app` are `running`, then a clean SIGTERM exit 0.

The ready-timeout path (a check that never passes) is asserted in the root e2e suite (`TestE2EReadyCheckTimeout`).

```bash
go test ./documentation/ready-gate/ -v
```
