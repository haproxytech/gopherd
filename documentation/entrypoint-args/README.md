# Entrypoint args & passthrough

With gopherd as the image `ENTRYPOINT`, what happens to `docker run` arguments
depends on their shape:

| Invocation | Result |
|:-----------|:-------|
| `docker run image` | normal init, no extra args |
| `docker run image --debug` | daemon mode; `--debug` appended to the `use-entrypoint-args` service |
| `docker run image -- --debug` | same — `--` explicitly separates entrypoint args |
| `docker run image /bin/sh` | **passthrough**: execs `/bin/sh` directly, no init |
| `docker run image status` | client mode (known command) |

Arguments starting with `-` go to the service; a bare command that isn't a
known client command replaces gopherd via `exec` — handy for debugging
containers (`docker run image ls -la /etc`).

## Config

```yaml
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "echo $* > ARGSFILE; exec sleep 300", "app"]
    use-entrypoint-args: true
    on-failure: shutdown
```

- At most one service may set `use-entrypoint-args: true` (config error
  otherwise); entrypoint args are appended after its configured `args`.
- With no consuming service, passed entrypoint args are discarded with a
  warning.

## Expected behavior

- `gopherd --port 9090` starts the daemon and `app` sees `--port 9090` as
  extra arguments.
- `gopherd /bin/echo hello` prints `hello` and exits — gopherd is replaced
  by the command, nothing is supervised.

## Test

Run level. One test launches the daemon with entrypoint args and asserts the
service received them appended. The other invokes the binary with a bare
command and asserts passthrough exec output. SIGTERM yields a clean exit 0.

```bash
go test ./documentation/entrypoint-args/ -v
```
