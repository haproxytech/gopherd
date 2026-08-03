# HTTP health check over a Unix socket

Services that expose their health endpoint on a Unix domain socket instead
of a TCP port (no open ports, socket-permission access control) can be
checked by combining `url` with `socket`: the GET request is sent through
the socket, and any 2xx response counts as healthy.

## Config

```yaml
processes:
  - name: app
    command: /usr/local/bin/app
    args: ["300"]
    on-failure: shutdown

checks:
  app-http:
    http:
      url: http://localhost/health
      socket: /run/app/health.sock
    period: 300ms
    threshold: 1
```

- `socket` switches the transport: the connection goes to the unix socket,
  `url` only contributes the request path (and Host header).
- Everything else works as with TCP HTTP checks: `period`, `timeout`,
  `threshold`, `initial-delay`, plus `ready-check` gating and
  `on-check-failure` actions.

## Expected behavior

`gopherd status` lists `app-http` as `healthy` while the socket answers
`200 OK` on `/health`, `unhealthy` once it stops.

## Test

Run level. The test hosts an HTTP server on a unix socket in-process,
substitutes its path, and asserts the check reports healthy; after closing
the server it asserts the check flips to unhealthy. SIGTERM then yields a
clean exit 0.

```bash
go test ./documentation/health-check-http-unix/ -v
```
