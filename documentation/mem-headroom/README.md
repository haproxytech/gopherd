# Memory template with headroom (percentage minus an amount)

Size a memory argument from the detected RAM while reserving a fixed amount, so
the app stays below the cgroup limit and gives the kernel room before an
OOM-kill.

## Config

```yaml
# Reserve memory headroom: percentage minus a fixed amount.
processes:
  - name: app
    command: /usr/local/bin/app
    args: ["--cache", "{{mem 100% - 64MiB}}"]
    on-failure: shutdown
```

- `{{mem 100% - 64MiB}}` — take 100% of the detected memory, then subtract
  64 MiB. The minuend must be a percentage; the subtrahend needs a unit
  (`MB`, `MiB`, `GB`, `GiB`).
- The result is a bare integer count of MiB (e.g. `1984`).
- If the subtraction would reach 0 or below — for example `100% - 64MiB` on a
  64 MiB budget — gopherd rejects the config at load with a clear error, rather
  than starting the app with a nonsensical size.
- Like all memory templates, detection prefers cgroup limits (v2 `memory.max`,
  v1 `memory.limit_in_bytes`) over system RAM, and expansion happens at service
  start (in `args` and `environment`).

## Expected behavior

- Under a 2 GiB cgroup limit, `app` starts with `--cache 1984` (2048 − 64).
- On an 8 GiB host, `--cache 8128`.
- On a budget too small for the subtraction, the config fails to load.

## Test

ValidateParse level — the expanded value depends on the host's RAM and cgroup
limits, so the test asserts the config loads and validates rather than checking
a specific byte count.

```bash
go test ./documentation/mem-headroom/ -v
```
