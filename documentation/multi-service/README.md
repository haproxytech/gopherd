# Multi-service

Supervise two independent long-running services in one container. Each runs concurrently and is restarted or shuts the container down per its own policy.

## Config

```yaml
# Supervise two independent long-running services.
processes:
  - name: web
    command: /usr/local/bin/web
    args: ["300"]
    on-failure: shutdown
  - name: worker
    command: /usr/local/bin/worker
    args: ["300"]
    on-failure: shutdown
```

- `web` and `worker` have no `after`/`requires`, so they start in parallel.
- `on-failure: shutdown` — if either exits non-zero, gopherd shuts everything down.

## Expected behavior

- Both services start and run concurrently.
- `gopherd status` lists both `web` and `worker`.
- On SIGTERM, gopherd stops both and exits 0.

## Test

Both placeholder binaries are replaced with `/usr/bin/sleep`. The test asserts both names appear in `status`, that each reports `running`, then sends SIGTERM and asserts exit code 0.

```bash
go test ./documentation/multi-service/ -v
```
