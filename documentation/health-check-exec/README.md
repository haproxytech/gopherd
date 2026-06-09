# Exec health check

Probe a service by running a command on a schedule. The check passes when the command exits zero.

## Config

```yaml
# Exec health check: run a command periodically to probe a service.
processes:
  - name: svc
    command: /usr/local/bin/svc
    args: ["300"]
    on-failure: shutdown

checks:
  svc-alive:
    exec:
      command: /bin/true
      args: []
    period: 300ms
    threshold: 1
```

- `checks:` is a map keyed by check name. `svc-alive` is the check.
- `exec.command` runs every `period`; exit 0 is healthy, non-zero is a failure.
- `threshold` is the number of consecutive failures before the check is marked unhealthy.
- A check is purely a probe here. To make a service react, add `on-check-failure: { svc-alive: restart }` (or `shutdown`).

## Expected behavior

- gopherd starts `svc` and the `svc-alive` check.
- The check appears in `gopherd status` immediately, then flips to `healthy` after its first probe.
- The checks section of `status` reports one line per check:

  ```
  checks:
    svc-alive            healthy  failures=0
  ```

- A failing exec check (non-zero exit) reaches `unhealthy` once `threshold` consecutive failures accumulate, and `failures` counts up.

## Test

Run level. The test substitutes `/usr/bin/sleep` for the `svc` placeholder, asserts `svc` is `running`, waits a few check periods, and asserts the `svc-alive` line is `healthy` (not `unhealthy`). `/bin/true` always succeeds, so the probe is deterministic and needs no network.

```bash
go test ./documentation/health-check-exec/ -v
```
