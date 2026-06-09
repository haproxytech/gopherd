# Memory template for a Go app (GOMEMLIMIT)

Set Go's `GOMEMLIMIT` soft memory limit from the RAM gopherd detects, so the
garbage collector tracks the container's real memory budget without a hardcoded
byte count.

## Config

```yaml
# GOMEMLIMIT for a Go app, sized from detected RAM + cgroup limits.
processes:
  - name: app
    command: /usr/local/bin/app
    environment:
      GOMEMLIMIT: "{{mem 90% - 64MiB}}MiB"
    on-failure: shutdown
```

- `{{mem 90% - 64MiB}}` — 90% of the detected memory, minus a 64 MiB floor of
  headroom. Detection prefers cgroup limits (v2 `memory.max`, v1
  `memory.limit_in_bytes`) over total system RAM.
- **The `MiB` suffix after the template is mandatory for Go.** `{{mem EXPR}}`
  expands to a bare integer count of MiB (e.g. `1472`), and `GOMEMLIMIT` reads a
  bare number as **bytes**. Appending `MiB` yields `1472MiB`, which Go parses
  correctly. Without it, `GOMEMLIMIT=1472` would cap the heap at 1472 bytes.
- `GOMEMLIMIT` is a *soft* limit — Go works to keep the heap under it but does
  not guarantee it. Reserving headroom (`- 64MiB`, or a percentage below 100)
  gives the GC room to run before the cgroup hard limit OOM-kills the process.

## Expected behavior

- Under a 2 GiB cgroup memory limit, `app` starts with
  `GOMEMLIMIT=1779MiB` (90% of 2048 MiB, minus 64).
- On an 8 GiB host with no cgroup limit, it starts with `GOMEMLIMIT=7308MiB`.
- The config is portable across container sizes: no hardcoded byte counts.

## Test

ValidateParse level — the expanded value depends on the host's RAM and cgroup
limits, so the test asserts the config loads and validates rather than checking
a specific byte count.

```bash
go test ./documentation/mem-golang-template/ -v
```
