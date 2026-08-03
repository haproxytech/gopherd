# export-socket: client commands from inside services

`export-socket: true` sets `GOPHERD_SOCKET` in the child's environment to the
daemon's resolved control socket path (like `NOTIFY_SOCKET` for sd-notify).
The gopherd client reads that variable, so a managed service can run
`gopherd app restart`, `gopherd status`, etc. against its own supervisor —
no need to know where the socket actually landed (rootless deployments fall
back to a user-specific tmp dir instead of `/run`).

Off by default: the child environment is left untouched unless opted in.

## Config

```yaml
export-socket: true

processes:
  - name: app
    command: /usr/local/bin/myapp
    args: ["300"]
    on-failure: shutdown

  - name: sidecar
    command: /bin/sh
    args: ["-c", "gopherd status app > STATUSFILE 2>&1; echo ${GOPHERD_SOCKET:-unset} > SOCKFILE; exec sleep 300"]
    on-failure: shutdown

  - name: plain
    command: /bin/sh
    args: ["-c", "echo ${GOPHERD_SOCKET:-unset} > PLAINFILE; exec sleep 300"]
    export-socket: false
    on-failure: shutdown
```

- Global `export-socket: true` is the default for every service; `plain`
  opts back out with `export-socket: false`.
- The exported path is the socket bound at daemon startup — stable across
  hot reloads.
- Precedence: an explicit `environment: GOPHERD_SOCKET` value wins over the
  injection, and `remove-env: [GOPHERD_SOCKET]` strips it entirely.
- `pass-env` is unrelated: injection works with the default empty child env.

## Expected behavior

- `sidecar` sees `GOPHERD_SOCKET` set to the daemon's socket path and its
  `gopherd status app` call succeeds from inside the container.
- `plain` sees no `GOPHERD_SOCKET` at all.

## Test

Run level. The sidecar's placeholder command is substituted with the real
gopherd binary; the test asserts the exported path matches the daemon's
socket, that the in-child `status app` output reports `running`, and that
the opted-out service sees no variable. SIGTERM then yields a clean exit 0.

```bash
go test ./documentation/export-socket/ -v
```
