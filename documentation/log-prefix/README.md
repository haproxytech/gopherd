# Log prefix

The global `prefix` option controls the per-line tag prepended to every child's
log output. Tokens are space-separated and applied in order.

## Config

```yaml
prefix: "timestamp service"
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "echo hello-from-app; sleep 300"]
    on-failure: shutdown
```

- `prefix` — global format. Tokens:
  - `timestamp` — UTC timestamp (e.g. `2026-06-08T21:11:40.666Z`).
  - `service` — `[name]` tag.
  - `none` — no prefix at all (raw output).
- The default when omitted is `service timestamp`. `timestamp service` swaps
  the order, producing `2026-06-08T21:11:40.666Z [app] line`.
- A process may override the global prefix with its own per-process `prefix`.

## Expected behavior

- `app` writes `hello-from-app` to stdout.
- gopherd prefixes the line with the configured tokens:
  `2026-06-08T21:11:40.666Z [app] hello-from-app`.

## Test

The test asserts `status app` is `running`, then queries `logs app`
(non-follow), which returns the recent ring-buffer lines for the service. It
asserts the captured line contains both `hello-from-app` and the `[app]`
service prefix, then SIGTERM yields a clean exit 0.

```bash
go test ./documentation/log-prefix/ -v
```
