# Hot reload

Edit the config file, then trigger a reload — no daemon restart, no dropped
services:

```bash
gopherd reload        # via the control socket
kill -HUP <pid>       # same thing, signal-driven
```

## Config

```yaml
processes:
  - name: app
    command: /usr/local/bin/myapp
    args: ["300"]
    on-failure: shutdown
```

Reload re-reads the same path the daemon was started with (`GOPHERD_CONFIG`
or `/etc/gopherd/gopherd.yml`) and reconciles:

- **Added services** start (in dependency order, honoring `startup`).
- **Removed services** are stopped with their configured `stop-signal` /
  `kill-delay`.
- **Unchanged services** keep running untouched — a reload is not a restart.
- **Policy-only changes** (`on-failure`, `on-success`, `exit-code-map`,
  `signal-rewrite`, backoff settings) mutate the running service in place
  and apply to its next exit.
- Checks and log-targets are reconciled the same way.

A reload is refused while the startup sequence is still running, and a config
that fails to parse leaves the previous config active — a bad edit never
kills the daemon.

## Expected behavior

- Add a `sidecar` entry to the file, run `gopherd reload`: `sidecar` starts,
  `app` keeps its PID.
- Remove it again, reload: `sidecar` is stopped and dropped from `status`.

## Test

Run level. One test rewrites the config and drives `reload` over the control
socket, asserting the added service starts and, after a second rewrite, is
removed again. The other triggers the same via SIGHUP. SIGTERM then yields a
clean exit 0.

```bash
go test ./documentation/hot-reload/ -v
```
