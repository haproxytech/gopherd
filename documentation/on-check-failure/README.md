# on-check-failure: check-driven actions

Health checks on their own only report status. `on-check-failure` maps a
check name to an action, turning it into a supervisor: an app that goes
unhealthy gets restarted (self-healing) — or stopped, or takes the whole
container down, depending on the action.

## Config

```yaml
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "touch FLAGFILE; exec sleep 300"]
    on-check-failure:
      app-alive: restart
    on-failure: shutdown

checks:
  app-alive:
    exec:
      command: /bin/sh
      args: ["-c", "test -f FLAGFILE"]
    period: 300ms
    threshold: 2
```

- The check fails `threshold` consecutive times before the action fires
  (here: 2 × 300ms ≈ 600ms of unhealthiness).
- `restart` bypasses the exit-action policy: the service is stopped and
  relaunched even though `on-failure` says `shutdown` — a check-triggered
  restart is not a crash.
- Other actions: `shutdown` / `success-shutdown` / `failure-shutdown`
  (take gopherd down) and `ignore`.
- Several services may react to the same check, each with its own action.

## Expected behavior

The app is healthy while its flag file exists. Deleting the file makes the
check fail twice, gopherd restarts the app, the app recreates the flag on
startup, and the check goes healthy again — a full self-healing cycle with
one `restarts=1` tick in `gopherd status`.

## Test

Run level. The test waits for the check to report healthy, deletes the flag
file, and asserts the service is restarted (restart counter increments) and
the check recovers. SIGTERM then yields a clean exit 0.

```bash
go test ./documentation/on-check-failure/ -v
```
