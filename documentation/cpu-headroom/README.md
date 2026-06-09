# CPU template with headroom (percentage minus a count)

Size a worker count from the detected CPUs while reserving a fixed number of
cores for other work — sidecars, the init process, or the kernel.

## Config

```yaml
# Reserve CPU headroom: percentage minus a fixed count.
processes:
  - name: app
    command: /usr/local/bin/app
    args: ["--workers", "{{cpu 100% - 1}}"]
    on-failure: shutdown
```

- `{{cpu 100% - 1}}` — take 100% of the detected CPUs, then subtract 1. The
  minuend must be a percentage; `{{cpu - 1}}` or `{{cpu 4 - 1}}` are **not**
  valid forms.
- The subtrahend is a whole number of CPUs (`- 1`, `- 2`, …).
- The result is clamped to a minimum of 1, so a single-CPU host still yields
  `1` rather than `0`.
- Like all CPU templates, detection honors cgroup limits (CFS quota, cpuset)
  before falling back to the system CPU count, and expansion happens at service
  start (in `args` and `environment`).

## Expected behavior

- On an 8-CPU host, `app` starts with `--workers 7`.
- On a 4-CPU host, `--workers 3`.
- On a 1-CPU host, `--workers 1` (clamped).

## Test

ValidateParse level — the expanded count depends on the host's CPUs and cgroup
limits, so the test asserts the config loads and validates rather than checking
a specific number.

```bash
go test ./documentation/cpu-headroom/ -v
```
