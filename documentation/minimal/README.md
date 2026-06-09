# Minimal

The simplest possible gopherd config: supervise a single long-running process and shut down if it fails.

## Config

```yaml
# Minimal gopherd config: supervise a single long-running process.
processes:
  - name: app
    command: /usr/local/bin/myapp
    args: ["300"]
    on-failure: shutdown
```

- `command` — absolute path to the application binary.
- `args` — argument list passed directly; no shell involved.
- `on-failure: shutdown` — gopherd exits when `app` exits non-zero.

## Expected behavior

- gopherd starts `app` and supervises it.
- `gopherd status app` reports `running`.
- On SIGTERM, gopherd stops `app` and exits 0.
- Non-zero exit from `app` triggers gopherd shutdown.

## Test

Run level. The test substitutes `/usr/bin/sleep` for the placeholder `/usr/local/bin/myapp`, asserts `status app` shows `running`, then sends SIGTERM and asserts exit code 0.

Real configs point `command` at the actual application binary.

```bash
go test ./documentation/minimal/ -v
```
