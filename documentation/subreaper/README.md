# Subreaper

Run gopherd as a supervisor when it is not PID 1, and have the kernel notify children if gopherd dies abruptly.

## Config

```yaml
# Subreaper mode for non-PID-1 deployments, plus parent-death cleanup.
subreaper: true
processes:
  - name: app
    command: /usr/local/bin/app
    args: ["300"]
    parent-death-signal: SIGTERM
    on-failure: shutdown
```

- `subreaper: true` — sets `PR_SET_CHILD_SUBREAPER` so orphaned descendants re-parent to gopherd instead of host PID 1, letting its reap loop collect them. Use when gopherd is not PID 1 (e.g. `docker exec`, a k8s sidecar, or nested init). Linux-only.
- `parent-death-signal: SIGTERM` — sets `PR_SET_PDEATHSIG`; the kernel delivers this signal to `app` if gopherd dies, so the child shuts down rather than being orphaned. Linux-only.

## Expected behavior

- Orphaned grandchildren of `app` re-parent to gopherd and get reaped, avoiding zombies.
- If gopherd is killed, `app` receives SIGTERM from the kernel and exits.

## Test

ValidateParse level — behavior is environment-dependent (cgroup limits / remote syslog / file rotation thresholds), so the test asserts the config loads and validates. Subreaper and parent-death-signal are Linux runtime behaviors; parse-validate is the honest CI-portable level.

```bash
go test ./documentation/subreaper/ -v
```
