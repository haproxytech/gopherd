# Restart with exponential backoff

`on-failure: restart` keeps a crashing service alive without taking the
container down: each non-zero exit schedules a relaunch after a backoff
delay that grows exponentially (with jitter) up to a cap.

## Config

```yaml
processes:
  - name: app
    command: /usr/local/bin/myapp
    args: ["300"]
    on-failure: shutdown

  - name: flaky
    command: /usr/local/bin/flaky-worker
    on-failure: restart
    backoff-delay: 500ms
    backoff-factor: 2.0
    backoff-limit: 5s

  - name: batch
    command: /usr/local/bin/batch-job
    on-success: restart
    on-failure: shutdown
    backoff-delay: 500ms
    backoff-limit: 5s
```

- `backoff-delay` — delay before the first restart (default `500ms`).
- `backoff-factor` — multiplier per consecutive failure (default `2.0`):
  500ms, 1s, 2s, 4s, ...
- `backoff-limit` — cap on the delay (default `30s`); once reached, retries
  continue at this fixed pace forever. There is no retry count limit.
- A run that survives longer than `backoff-limit` resets the counter, so a
  service that crashes once a day always restarts after `backoff-delay`.
- `on-success: restart` (the `batch` service) reruns a worker after every
  clean exit with the same backoff pacing — a poor man's cron loop. The
  default `on-success` is `shutdown`, and a genuine failure still takes the
  container down via `on-failure: shutdown`.

## Expected behavior

- `flaky` crashes, gopherd logs the exit and restarts it after the backoff
  delay; `gopherd status` shows the `restarts=` counter climbing.
- `batch` exits 0, is rerun after the backoff delay, and its counter climbs
  the same way.
- `app` and gopherd itself are unaffected by either loop.

## Test

Run level. The placeholders are substituted with `/bin/false` (flaky, always
exits 1) and `/bin/true` (batch, always exits 0) and the backoff shortened;
the test polls `status` until both restart counters reach 3, proving the
crash-restart and clean-rerun loops, then checks the daemon is still
healthy. SIGTERM yields a clean exit 0.

```bash
go test ./documentation/restart-backoff/ -v
```
