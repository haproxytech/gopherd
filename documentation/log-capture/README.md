# Log capture (opt-in)

The `log-capture` option decides whether gopherd interposes on a child's
stdout/stderr. It is **off by default**: children write directly to the
container's stdout/stderr file descriptors and gopherd never touches their
output — no pipe, no per-line processing, zero CPU cost.

## Config

```yaml
processes:
  - name: raw
    command: /bin/sh
    args: ["-c", "echo raw-passthrough; sleep 300"]
    on-failure: shutdown

  - name: captured
    command: /bin/sh
    args: ["-c", "echo captured-line; sleep 300"]
    on-failure: shutdown
    log-capture: true
```

- `log-capture` — global key sets the default for all processes; a per-process
  key overrides it (same shape as `pass-env`).
- When **false** (default), capture-dependent features are unavailable for
  that service: `prefix` is ignored, log-targets receive nothing, and
  `gopherd logs <svc>` returns `log capture disabled`.
- When **true**, output flows through gopherd: prefixes, `gopherd logs`,
  log-targets, and rotation all work.

## Expected behavior

- `raw` writes `raw-passthrough` straight to gopherd's stdout FD — the line
  appears without any prefix.
- `captured` is prefixed (`[captured] <timestamp> captured-line`) and
  queryable via `logs captured`.
- `logs raw` fails with `log capture disabled for "raw" (set log-capture: true)`.

## Test

The test asserts the raw line appears un-prefixed in daemon output, the
captured line is returned by `logs captured` with its `[captured]` tag, and
`logs raw` returns the disabled error.

```bash
go test ./documentation/log-capture/ -v
```
