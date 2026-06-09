# Control socket

Manage services at runtime over a unix domain socket: start, stop, restart, query status, or send signals without restarting gopherd.

## Config

```yaml
# Runtime control over a unix socket: start/stop/status on demand.
control:
  socket: /run/gopherd.sock
processes:
  - name: app
    command: /usr/local/bin/app
    args: ["300"]
    startup: disabled
    on-failure: ignore
```

- `control.socket` — path to the unix socket gopherd listens on for commands.
- `startup: disabled` — `app` is defined but not auto-started; it waits to be started over the socket.
- `on-failure: ignore` — gopherd keeps supervising even when `app` is not running.

Commands (`gopherd <service> <action>` or `gopherd <action> <service>`):

- `status` — overview table; `status app` for one service.
- `start app` / `stop app` / `restart app` — lifecycle control.
- `signal app SIGUSR1` — send an arbitrary signal.

## Expected behavior

- gopherd starts with `app` disabled, so `status app` reports `disabled` (not `running`).
- `start app` launches it; `status app` then reports `running`.
- `stop app` stops it; `status app` no longer reports `running`.
- gopherd stays alive throughout and exits 0 on SIGTERM.

## Test

Run level. The test substitutes `/usr/bin/sleep` for the placeholder `/usr/local/bin/app`. It asserts `app` is not running initially, runs `start app` and asserts it becomes `running`, runs `stop app` and asserts it stops, then sends SIGTERM and asserts exit code 0.

The harness substitutes the `{{SOCKET}}` token in `control.socket` with a temporary socket path.

```bash
go test ./documentation/control-socket/ -v
```
