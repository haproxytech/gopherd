# CPU template

Size process arguments from the number of CPUs gopherd detects, so the same config adapts to whatever host or container it runs in.

## Config

```yaml
# CPU-aware templates: size args from detected CPUs (system + cgroup).
processes:
  - name: app
    command: /usr/local/bin/app
    args: ["--workers", "{{cpu 50%}}", "--max", "{{cpu}}"]
    on-failure: shutdown
```

- `{{cpu}}` — bare form expands to the detected CPU count.
- `{{cpu 50%}}` — percentage form expands to that fraction of the count (rounded).
- Detection prefers cgroup limits over the system count: cgroup v2 `cpu.max` CFS quota, v1 `cpu.cfs_quota_us`/`cpu.cfs_period_us`, and `cpuset` are honored, falling back to the host CPU count.
- CPU templates expand at service start (in `args` and `environment`), not at config load, so the value reflects the runtime environment.

## Expected behavior

- On a 4-CPU host with no cgroup limit, `app` starts with `--workers 2 --max 4`.
- Under a cgroup CFS quota of 2 CPUs, it starts with `--workers 1 --max 2`.
- The config is portable: no hardcoded core counts.

## Test

ValidateParse level — behavior is environment-dependent (cgroup limits / remote syslog / file rotation thresholds), so the test asserts the config loads and validates.

```bash
go test ./documentation/cpu-template/ -v
```
