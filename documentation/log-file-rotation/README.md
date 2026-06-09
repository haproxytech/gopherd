# Log file rotation

Write a service's output to a file and rotate it by size, keeping a bounded number of compressed history files.

## Config

```yaml
# File log target with size-based rotation and retention.
processes:
  - name: app
    command: /usr/local/bin/app
    args: ["300"]
    on-failure: shutdown
log-targets:
  app-file:
    type: file
    location: /var/log/app.log
    services: [app]
    max-size: 10MiB
    max-files: 5
    compress: true
```

- `type: file` — write to a file rather than syslog.
- `location` — the active log file path.
- `services` — only forward output from these services (omit for all).
- `max-size` — rotate when the file would exceed this size (e.g. `10MiB`).
- `max-files` — retain this many rotated files (`app.log.1` ... `app.log.5`).
- `compress` — gzip rotated files (`app.log.1.gz`, `app.log.2.gz`, ...).

## Expected behavior

- `app` output is written to `/var/log/app.log`.
- When the file would exceed 10 MiB, it rotates: existing files shift up by one and a fresh `app.log` is opened.
- At most 5 rotated files are kept; older ones are deleted.
- Rotated files are gzip-compressed.

## Test

ValidateParse level — behavior is environment-dependent (cgroup limits / remote syslog / file rotation thresholds), so the test asserts the config loads and validates.

```bash
go test ./documentation/log-file-rotation/ -v
```
