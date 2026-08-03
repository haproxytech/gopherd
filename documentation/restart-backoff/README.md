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
```

- `backoff-delay` — delay before the first restart (default `500ms`).
- `backoff-factor` — multiplier per consecutive failure (default `2.0`):
  500ms, 1s, 2s, 4s, ...
- `backoff-limit` — cap on the delay (default `30s`); once reached, retries
  continue at this fixed pace forever. There is no retry count limit.
- A run that survives longer than `backoff-limit` resets the counter, so a
  service that crashes once a day always restarts after `backoff-delay`.
- `on-success: restart` also exists for workers that should rerun after a
  clean exit; the default `on-success` is `shutdown`.

## Expected behavior

- `flaky` crashes, gopherd logs the exit and restarts it after the backoff
  delay; `gopherd status` shows the `restarts=` counter climbing.
- `app` and gopherd itself are unaffected by the crash loop.

## Test

Run level. The flaky placeholder is substituted with `/bin/false` (always
exits 1) and the backoff shortened; the test polls `status` until the
restart counter reaches 3, proving the restart loop, then checks the daemon
is still healthy. SIGTERM yields a clean exit 0.

```bash
go test ./documentation/restart-backoff/ -v
```
