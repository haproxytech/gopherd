# HTTP health check

Probe a service by issuing an HTTP GET. The check passes when the server responds with a 2xx status.

## Config

```yaml
# HTTP health check: probe a service by issuing an HTTP GET.
processes:
  - name: web
    command: /usr/bin/python3
    args: ["-m", "http.server", "PORT", "--bind", "127.0.0.1"]
    on-failure: shutdown

checks:
  web-http:
    http:
      url: http://127.0.0.1:PORT/
    period: 300ms
    threshold: 1
```

- `http.url` is fetched every `period`; a 2xx response is healthy.
- `threshold` is the number of consecutive failures before the check is marked unhealthy.
- For HTTP over a Unix socket, add `socket: /path/to.sock` under `http:` and keep a normal `url`.
- In a real config, `command` is your web service and `url` is its health endpoint (e.g. `/healthz`). Here a Python `http.server` stands in so the example is self-contained.

## Expected behavior

- gopherd starts `web` (a Python HTTP server) and the `web-http` check.
- Once the server binds, the GET probe returns 200 and the check reports `healthy`:

  ```
  checks:
    web-http             healthy  failures=0
  ```

- If the endpoint is unreachable or returns non-2xx, the check trends `unhealthy` once `threshold` is reached.

## Test

Run level. `PORT` is substituted with an OS-assigned free port (e.g. 8080). The service is a real `python3 -m http.server` responder; the test waits for the probe to get a 2xx and asserts the `web-http` line is `healthy`. The test skips if `python3` is absent.

```bash
go test ./documentation/health-check-http/ -v
```
