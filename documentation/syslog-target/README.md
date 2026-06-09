# Syslog target

Forward a service's output to a remote syslog server over UDP or TCP, tagged with custom labels.

## Config

```yaml
# Forward service logs to a remote syslog server.
processes:
  - name: app
    command: /usr/local/bin/app
    args: ["300"]
    on-failure: shutdown
log-targets:
  remote:
    type: syslog
    location: udp://logs.example.com:514
    services: [app]
    labels:
      env: production
```

- `type: syslog` — forward to a syslog server.
- `location` — `udp://host:port` or `tcp://host:port`.
- `services` — only forward output from these services (omit for all).
- `labels` — key/value pairs attached to forwarded records for downstream filtering.

## Expected behavior

- `app` output is sent to `logs.example.com:514` over UDP.
- Records carry the `env: production` label.
- Other services' output is not forwarded by this target.

## Test

ValidateParse level — behavior is environment-dependent (cgroup limits / remote syslog / file rotation thresholds), so the test asserts the config loads and validates.

```bash
go test ./documentation/syslog-target/ -v
```
