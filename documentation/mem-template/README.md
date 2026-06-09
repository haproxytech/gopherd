# Memory template

Size process arguments from the amount of RAM gopherd detects, so heap and cache limits track the container's real memory budget.

## Config

```yaml
# Memory-aware templates: size args from detected RAM + cgroup limits.
processes:
  - name: app
    command: /usr/local/bin/app
    args: ["--heap", "{{mem 75%}}"]
    on-failure: shutdown
```

- `{{mem 75%}}` — percentage form expands to that fraction of the detected memory.
- Fixed amounts are also supported (e.g. `{{mem 512MiB}}`).
- Memory templates have no bare `{{mem}}` form — an expression is always required.
- Detection prefers cgroup limits over system RAM: cgroup v2 `memory.max` and v1 `memory.limit_in_bytes` are honored, falling back to total system memory.
- Memory templates expand at service start (in `args` and `environment`), not at config load, so the value reflects the runtime environment.

## Expected behavior

- On a host with 8 GiB and no cgroup limit, `app` starts with `--heap 6GiB`.
- Under a 2 GiB cgroup memory limit, it starts with `--heap 1536MiB`.
- The config is portable: no hardcoded byte counts.

## Test

ValidateParse level — behavior is environment-dependent (cgroup limits / remote syslog / file rotation thresholds), so the test asserts the config loads and validates.

```bash
go test ./documentation/mem-template/ -v
```
