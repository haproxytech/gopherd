# ![HAProxy](https://github.com/haproxytech/kubernetes-ingress/raw/master/assets/images/haproxy-weblogo-210x49.png "HAProxy")

## go-init

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/haproxytech/go-init)](https://goreportcard.com/report/github.com/haproxytech/go-init)

### Description

A minimal PID 1 init process and service supervisor for Docker containers, especially rootless. Manages multiple processes with dependency ordering, health checks, restart policies, log prefixing, and runtime control — all configured via a single YAML file. Zero external dependencies.

### Features

- **Process management** — start multiple processes with dependency ordering, signal forwarding, and zombie reaping
- **Per-service stop signal & kill delay** — configurable shutdown signal and grace period before SIGKILL
- **User/group switching** — run each process as a specific user/group (by name or numeric ID)
- **Environment & working directory** — per-process environment variables and working directory
- **Restart policies** — configurable `on-success` / `on-failure` actions: `restart`, `shutdown`, `ignore`
- **Exponential backoff** — configurable delay, factor, and limit for restart attempts
- **Service dependencies** — `after`, `before`, `requires` with topological sort
- **Oneshot tasks** — run-once init tasks (e.g. config generation, permission setup) that complete before dependents start
- **Health checks** — HTTP (including over Unix socket), TCP, and exec-based checks with configurable period, timeout, and threshold
- **Readiness gates** — block dependent services until a health check passes (not just until the process spawns)
- **Log prefixing** — timestamps and service name on every output line
- **Log targets** — forward logs to syslog (UDP/TCP)
- **Stats tracking** — service uptime, restarts, exits, and health check results via `go-init stats`
- **Control socket** — start/stop/restart/status/signal/reload/stats/logs services at runtime via Unix socket
- **Log streaming** — `go-init logs <service> -f` for live log tailing via control socket
- **Hot reload** — `go-init reload` or SIGHUP to re-read config and reconcile services without restart
- **Exit code propagation** — go-init exits with the actual exit code of the service that triggered shutdown
- **Entrypoint passthrough** — `docker run <image> /bin/sh` execs the command directly, bypassing the init system
- **No root required** — works in rootless containers

### Usage

#### Build

```bash
go build -o go-init .         # or: task build
task ci                       # full CI: check-commit, tidy, format, lint, test
task test                     # run tests with gotestsum
task lint                     # revive + staticcheck + betteralign
task format                   # go fix + betteralign + gofumpt
```

#### Run as init (daemon mode)

```bash
GO_INIT_CONFIG=/path/to/config.yml ./go-init
```

Default config path: `/etc/go-init.yml` (override via `GO_INIT_CONFIG` env var).

#### Runtime control (client mode)

When invoked with a known command, `go-init` connects to the running daemon via Unix socket:

```bash
./go-init list                       # list all services and their status
./go-init app start                  # start a stopped service
./go-init app stop                   # stop a running service
./go-init app restart                # restart a service
./go-init app status                 # show service status
./go-init signal haproxy SIGUSR2     # send a signal to a running service (e.g. reload)
./go-init logs app                   # show recent logs for a service
./go-init logs app -f                # stream logs (follow mode, like tail -f)
./go-init stats                      # show service and check statistics
./go-init reload                     # hot-reload config (add/remove/update services)
```

Override socket path with `GO_INIT_SOCKET` env var (default: `/run/go-init.sock`).

#### Entrypoint passthrough

When invoked with arguments that aren't known client commands, `go-init` execs the command directly (replacing the process). This is useful for debugging containers:

```bash
docker run myimage /bin/sh           # drops into a shell, bypasses init
docker run myimage ls -la /etc       # runs ls, exits
```

Known client commands (`list`, `stats`, `start`, `stop`, `restart`, `status`, `signal`, `logs`, `reload`) still go to client mode.

#### Docker

```dockerfile
FROM your-base-image
COPY go-init /sbin/go-init
COPY go-init.yml /etc/go-init.yml
ENTRYPOINT ["/sbin/go-init"]
# Normal: runs as PID 1 init system
# Debug:  docker run <image> /bin/sh  → passthrough to shell
```

### Configuration

Configuration is a single YAML file (no external YAML library — built-in parser). See the [example/](example/) directory for ready-to-use configs including a minimal setup, HAProxy ingress pattern, and a comprehensive all-options reference.

Below is a full example showing all available options:

```yaml
# Global options
no-time: false                       # disable timestamps in log output

# Control socket
control:
  socket: /run/go-init.sock          # Unix socket path for runtime control

# Processes
processes:
  # Oneshot: runs once to completion before dependents start
  - name: init-config
    command: /usr/local/bin/setup-config
    startup: oneshot                 # run once, block until done
    on-failure: ignore               # optional: continue even if it fails

  - name: app
    command: /usr/local/bin/myapp
    args: ["--config", "/etc/app.conf"]
    working-dir: /app
    user: appuser                    # run as user (name or user-id)
    group: appgroup                  # run as group (name or group-id)
    startup: enabled                 # enabled (default), disabled, or oneshot
    stop-signal: SIGTERM             # signal sent on shutdown (default: SIGTERM)
    kill-delay: 10s                  # grace period before SIGKILL (default: 5s)
    on-success: ignore               # action on exit 0: restart|shutdown|ignore
    on-failure: restart              # action on non-zero exit: restart|shutdown|ignore
    backoff-delay: 500ms             # initial restart delay (default: 500ms)
    backoff-factor: 2.0              # delay multiplier per attempt (default: 2.0)
    backoff-limit: 30s               # max restart delay (default: 30s)
    after: [init-config, sidecar]    # start after these services
    requires: [db]                   # hard dependencies (failure cascades)
    ready-check: health              # block dependents until this check passes
    ready-timeout: 30s               # max wait for ready check (default: 60s)
    no-time: false                   # per-process timestamp override
    environment:
      DB_HOST: localhost
      LOG_LEVEL: info
    on-check-failure:
      health: restart                # action when named check breaches threshold

  - name: sidecar
    command: /usr/local/bin/sidecar
    after: [app]                     # app must be ready (check passed) before sidecar starts
    on-success: shutdown
    on-failure: shutdown

# Health checks
checks:
  health:
    http:
      url: http://localhost:8080/health
    period: 10s
    timeout: 3s
    threshold: 3
    initial-delay: 30s               # wait before first check (default: 1x period)
    level: alive

  # Health check over Unix socket
  haproxy-health:
    http:
      url: http://localhost/healthz
      socket: /var/run/haproxy/health.sock
    period: 5s
    timeout: 2s
    threshold: 3

  db-ready:
    tcp:
      host: localhost
      port: 5432
    period: 5s
    timeout: 2s
    threshold: 5

  custom:
    exec:
      command: /usr/local/bin/healthcheck
    period: 30s
    timeout: 10s
    threshold: 2

# Log targets
log-targets:
  remote-syslog:
    type: syslog
    location: udp://logs.example.com:514
    services: [app]                  # only forward these services (empty = all)
    labels:
      env: production
```

### Configuration Reference

#### Process fields

| Field | Type | Default | Description |
|:------|:-----|:--------|:------------|
| `name` | string | command path | Service name for logging and control |
| `command` | string | *required* | Executable path |
| `args` | string[] | `[]` | Command arguments |
| `working-dir` | string | inherited | Working directory |
| `user` | string | inherited | Run as user (name) |
| `group` | string | inherited | Run as group (name) |
| `user-id` | int | inherited | Run as user (numeric, takes precedence) |
| `group-id` | int | inherited | Run as group (numeric, takes precedence) |
| `environment` | map | inherited | Extra environment variables |
| `startup` | string | `"enabled"` | `"enabled"`, `"disabled"`, or `"oneshot"` |
| `stop-signal` | string | `"SIGTERM"` | Signal name (with or without SIG prefix) |
| `kill-delay` | duration | `"5s"` | Grace period before SIGKILL |
| `on-success` | string | `"shutdown"` | Action on exit 0 |
| `on-failure` | string | `"shutdown"` | Action on non-zero exit |
| `backoff-delay` | duration | `"500ms"` | Initial restart backoff delay |
| `backoff-factor` | float | `2.0` | Backoff multiplier |
| `backoff-limit` | duration | `"30s"` | Max backoff delay |
| `after` | string[] | `[]` | Start after these services |
| `before` | string[] | `[]` | Start before these services |
| `requires` | string[] | `[]` | Hard dependencies |
| `on-check-failure` | map | `{}` | Check name -> action mapping |
| `ready-check` | string | | Health check name that gates dependents |
| `ready-timeout` | duration | `"60s"` | Max wait for ready check to pass |
| `no-time` | bool | `false` | Disable timestamp in log prefix |

#### Exit actions

| Action | Description |
|:-------|:------------|
| `restart` | Restart the process with exponential backoff |
| `shutdown` | Exit go-init with the service's actual exit code |
| `success-shutdown` | Exit go-init with code 0 |
| `failure-shutdown` | Exit go-init with the service's actual exit code |
| `ignore` | Leave process stopped, don't trigger shutdown |

#### Health check types

| Type | Fields | Description |
|:-----|:-------|:------------|
| `http` | `url`, `socket` | GET request, expect 2xx status. Optional `socket` for Unix socket transport |
| `tcp` | `host`, `port` | TCP connection, expect success |
| `exec` | `command`, `args` | Run command, expect exit 0 |

### Architecture

| Package | Purpose |
|:--------|:--------|
| `main` | Daemon entry point, reap loop, lifecycle orchestration |
| `yml/` | Built-in YAML parser and config loader (no external deps) |
| `service/` | Service lifecycle, signal parsing, user/group resolution |
| `backoff/` | Exponential backoff with jitter for restarts |
| `check/` | Health checks (HTTP, TCP, exec), unix socket transport, readiness gates |
| `control/` | Unix socket control server + CLI client |
| `logger/` | Line-buffered prefix writer, syslog log target forwarding |
| `metrics/` | In-memory service and check statistics |
| `order/` | Topological sort for service dependencies |
| `version/` | Build version from Go's embedded VCS metadata |

Core design:

- YAML config defines processes, checks, log targets, and control socket
- Zero external dependencies — built-in YAML parser, no protobuf/prometheus
- Single `Wait4(-1)` reap loop handles both managed children and orphaned zombies (no separate reaper goroutine — avoids race with `cmd.Wait()`)
- Forwards SIGTERM, SIGINT to all children using per-service stop signals; other signals forwarded as-is
- Each child gets its own process group (`Setpgid`)
- Services start in topological order based on dependency graph
- Restart requests are handled asynchronously via a channel with backoff delays
- Control socket uses one-command-per-connection protocol over Unix domain socket (streaming for `logs -f`)
- SIGHUP triggers config hot-reload instead of being forwarded to children
- Exit codes from services are propagated as go-init's own exit code

### Contributing

Thanks for your interest in the project and your willingness to contribute:

- Pull requests are welcome!
- For commit messages and general style please follow the haproxy project's [CONTRIBUTING guide](https://github.com/haproxy/haproxy/blob/master/CONTRIBUTING) and use that where applicable.

### Discussion

A Github issue is the right place to discuss feature requests, bug reports or any other subject that needs tracking.

## License

[Apache License 2.0](LICENSE)
