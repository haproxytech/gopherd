# Signal rewrite

Signal forwarding is strict opt-in. A service receives a signal sent to gopherd
only if its per-process `signal-rewrite` map lists that signal; the value names
the signal actually delivered to the child. Unmapped signals (e.g. SIGUSR2 with
no entry) are dropped.

## Config

```yaml
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "trap 'exit 0' TERM; while true; do sleep 0.2; done"]
    on-success: ignore
    on-failure: ignore
    signal-rewrite:
      USR1: TERM
```

- `signal-rewrite: {USR1: TERM}` — a SIGUSR1 received by gopherd is delivered
  to `app` as SIGTERM. Keys and values are signal names; both `USR1` and
  `SIGUSR1` forms are accepted.
- Keys may **not** be SIGHUP (reserved for reload) or any signal in
  `init-stop-signal` (consumed by gopherd for its own shutdown). Such configs
  are rejected at load.
- `on-success: ignore` / `on-failure: ignore` — when `app` exits, gopherd does
  not shut down; it stays alive as a supervisor.

## Expected behavior

- `app` traps SIGTERM and exits 0 when it arrives.
- Sending SIGUSR1 to gopherd is rewritten to SIGTERM, so `app` exits.
- The `ignore` exit actions keep gopherd running afterward.

## Test

The test asserts `status app` is `running`, sends SIGUSR1 to the daemon, then
polls until `status app` is no longer `running` (proving the rewritten SIGTERM
reached the service). It then asserts the daemon is still `Alive()` and SIGTERM
cleanly shuts it down with exit 0. Verified stable across repeated runs.

```bash
go test ./documentation/signal-rewrite/ -v
```
