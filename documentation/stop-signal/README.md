# stop-signal & kill-delay

Every stop — control-socket `stop`, dependency shutdown, or gopherd's own
graceful exit — follows the same per-service contract: send `stop-signal`
(default `SIGTERM`) to the service's process group, wait `kill-delay`
(default `5s`), then SIGKILL whatever is still alive.

## Config

```yaml
processes:
  - name: graceful
    command: /bin/sh
    args: ["-c", "trap 'kill $!; exit 0' INT; sleep 300 & wait"]
    stop-signal: SIGINT
    on-failure: shutdown

  - name: stubborn
    command: /bin/sh
    args: ["-c", "trap '' TERM; while true; do sleep 1 || true; done"]
    kill-delay: 2s
    on-failure: shutdown
```

- `stop-signal` accepts names with or without the `SIG` prefix (`SIGINT`,
  `INT`). Use e.g. `SIGQUIT` for nginx's graceful shutdown.
- The signal goes to the whole process group (each service runs in its own
  session), so shell wrappers and their children all receive it. Caveat:
  non-interactive shells start background jobs (`cmd &`) with SIGINT/SIGQUIT
  ignored — a wrapper's trap must `kill` them itself, as above.
- `kill-delay: 0` disables the SIGKILL escalation entirely — the service
  gets unlimited time to exit.
- A stop via the control socket is intentional: the exit does not trigger
  `on-success` / `on-failure` actions, and gopherd stays up.

## Expected behavior

- `gopherd graceful stop` — the service traps SIGINT and exits immediately.
- `gopherd stubborn stop` — the shell ignores SIGTERM (the group signal
  kills its `sleep`, but the loop respawns it); after `kill-delay` gopherd
  SIGKILLs the group and the service reports stopped.

## Test

Run level. The test stops `graceful` and asserts it exits promptly on
SIGINT; then stops `stubborn` (kill-delay shortened), asserts it is still
alive right after the stop signal, and that it reports stopped only after
the SIGKILL escalation. SIGTERM then yields a clean exit 0.

```bash
go test ./documentation/stop-signal/ -v
```
