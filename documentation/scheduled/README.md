# Scheduled

A scheduled service runs oneshot-style at every matching minute of a cron expression. It never runs at boot, takes no part in startup ordering, and a failed run simply waits for the next tick — cron semantics inside the supervisor.

## When to use it

This container is already a small appliance, not a single process. Schedule a task here when it needs the container's internal state: its filesystem, sockets, processes, or the gopherd control socket. An independent job, a nightly dump shipped to object storage, belongs in the orchestrator's scheduler (for example a Kubernetes CronJob), not here.

## Config

```yaml
# Run a backup at 03:00 every night; the app runs continuously.
processes:
  - name: app
    command: /usr/local/bin/app
    args: ["300"]
    on-success: ignore
    on-failure: shutdown
  - name: backup
    command: /usr/local/bin/backup
    args: ["-c", "echo backup done; exit 0"]
    startup: scheduled
    schedule: "0 3 * * *"
    startup-timeout: 30m
```

- `startup: scheduled` — `backup` is registered but not started at boot; it runs at each matching cron minute.
- `schedule: "0 3 * * *"` — standard 5-field cron (`min hour day-of-month month day-of-week`, local time). Supports `*`, lists (`1,15`), ranges (`1-5`), steps (`*/5`), and month/day names (`jan`, `mon`).
- `startup-timeout: 30m` — an optional bound per run: a run still going after 30 minutes is stopped; the schedule continues.
- If a tick fires while the previous run is still going, the tick is skipped (logged), like cron with flock.
- `on-success`/`on-failure`, backoff, and `after`/`before`/`requires` are rejected for scheduled services: each run's exit is only logged, and nothing can order against a service that does not run at startup.

## Off by default, flipped on via env

`schedule` may also be combined with `startup: disabled`, which keeps the job defined but inert. With an env-var template on `startup`, the same image ships the job off by default and an environment variable turns it on (see [service gating](../service-gating/README.md)):

```yaml
  - name: backup
    command: /usr/local/bin/backup
    startup: "{{.ENABLE_BACKUP:-disabled}}"
    schedule: "0 3 * * *"
```

Unset, the job reports `disabled` (a manual `gopherd start backup` still works); `ENABLE_BACKUP=scheduled` arms the schedule. The full scheduled contract — a parseable cron expression, no exit actions, backoff, or ordering — is validated even while disabled, so flipping the gate on can never surface a new config error.

## Expected behavior

- `gopherd status backup` reports `scheduled (next run 2026-...-... 03:00:00)` while idle, and `running (pid N)` during a run.
- `gopherd start backup` triggers an immediate manual run through the same oneshot-style path a tick uses; `gopherd stop backup` stops a run in progress. Neither affects the schedule.
- A run's exit — clean or failed — never shuts the daemon down or triggers restarts; the next tick is the retry.

## Test

The placeholder `backup` binary is replaced with `/bin/sh` so its args run as a script; `app` is replaced with `/usr/bin/sleep`. Waiting out a real cron minute would be slow, so the test asserts the boot behavior (`scheduled (next run ...)`, not started), triggers a manual run via `start backup`, and verifies the service returns to waiting and the daemon survives the run's clean exit. Tick timing itself is covered by the deterministic `testing/synctest` scheduler tests in the main package.

```bash
go test ./documentation/scheduled/ -v
```
