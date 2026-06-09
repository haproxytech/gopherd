# Exit-code map

A per-process `exit-code-map` rewrites a child's observed exit code **before**
gopherd evaluates `on-success` / `on-failure` and before the code is propagated
as gopherd's own exit status. This lets a known-benign non-zero exit be treated
as success.

## Config

```yaml
processes:
  - name: task
    command: /bin/sh
    args: ["-c", "sleep 1; exit 17"]
    on-success: ignore
    on-failure: shutdown
    exit-code-map:
      17: 0
```

- `exit-code-map: {17: 0}` — an exit code of 17 is remapped to 0. Keys may be
  integers or signal names (e.g. `SIGTERM: 0` maps the shell's 143).
- Because the remap happens first, the remapped 0 makes the exit a **success**,
  so `on-failure: shutdown` never fires.
- `on-success: ignore` — the success exit does not shut gopherd down either;
  it stays alive as a supervisor.

## Expected behavior

- `task` exits 17.
- gopherd remaps it to 0 (`task exited (status 17, remapped to 0)`).
- The success path with `on-success: ignore` leaves the daemon running.

## Test

The test asserts `status task` is `running`, waits past the sleep so `task`
exits 17, then asserts the daemon is still `Alive()` (proving the remap-to-0
avoided `on-failure: shutdown`) and that `status task` is no longer `running`.
SIGTERM then yields a clean exit 0.

## Note on oneshots

`exit-code-map` is applied by the reap loop, which covers long-running services
and oneshots started via the control socket after startup. It is **not** applied
to oneshots that run during the initial startup sequence — a startup oneshot
exiting non-zero fails the start before the remap. Use a regular (non-oneshot)
service, as above, when you need the remap to take effect.

```bash
go test ./documentation/exit-code-map/ -v
```
