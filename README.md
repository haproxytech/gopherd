# ![HAProxy](https://github.com/haproxytech/kubernetes-ingress/raw/master/assets/images/haproxy-weblogo-210x49.png "HAProxy")

## gopherd

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/haproxytech/gopherd)](https://goreportcard.com/report/github.com/haproxytech/gopherd)

### Description

A minimal PID 1 init process and service supervisor for Docker containers, especially rootless. Manages multiple processes with dependency ordering, health checks, restart policies, log prefixing, and runtime control — all configured via a single YAML file. Zero external dependencies.

### Features

- **Process management** — start multiple processes with dependency ordering, signal forwarding, and zombie reaping
- **Per-service stop signal & kill delay** — configurable shutdown signal and grace period before SIGKILL
- **User/group switching** — run each process as a specific user/group (by name or numeric ID)
- **Environment & working directory** — per-process environment variables, dotenv file loading, and working directory
- **Template args** — Go template syntax in args (e.g. `{{.MEMLIMIT}}`) resolved from env vars and dotenv files
- **Restart policies** — configurable `on-success` / `on-failure` actions: `restart`, `shutdown`, `ignore`
- **Exponential backoff** — configurable delay, factor, and limit for restart attempts
- **Service dependencies** — `after`, `before`, `requires` with topological sort
- **Oneshot tasks** — run-once init tasks (e.g. config generation, permission setup) that complete before dependents start
- **Health checks** — HTTP (including over Unix socket), TCP, and exec-based checks with configurable period, timeout, and threshold
- **Readiness gates** — block dependent services until a health check passes (not just until the process spawns)
- **Log prefixing** — service name and timestamp on every output line (configurable format)
- **Log targets** — forward logs to syslog (UDP/TCP)
- **Stats tracking** — service uptime, restarts, exits, and health check results via `gopherd stats`
- **Control socket** — start/stop/restart/status/signal/reload/stats/logs services at runtime via Unix socket
- **Log streaming** — `gopherd logs <service> -f` for live log tailing via control socket
- **Hot reload** — `gopherd reload` or SIGHUP to re-read config and reconcile services without restart
- **Exit code propagation** — gopherd exits with the actual exit code of the service that triggered shutdown
- **Entrypoint extra args** — pass Docker/Kubernetes entrypoint arguments to a designated service via `extra-args: entrypoint`
- **Entrypoint passthrough** — `docker run <image> /bin/sh` execs the command directly, bypassing the init system
- **No root required** — works in rootless containers

### Usage

#### Build

```bash
go build -o gopherd .         # or: task build
task ci                       # full CI: check-commit, tidy, format, lint, test
task test                     # run tests with gotestsum
task lint                     # revive + staticcheck + betteralign
task format                   # go fix + betteralign + gofumpt
```

#### Run as init (daemon mode)

```bash
GOPHERD_CONFIG=/path/to/config.yml ./gopherd
```

Default config path: `/var/lib/gopherd/gopherd.yml` (override via `GOPHERD_CONFIG` env var).

#### Runtime control (client mode)

When invoked with a known command, `gopherd` connects to the running daemon via Unix socket:

```bash
./gopherd list                       # list all services and their status
./gopherd app start                  # start a stopped service
./gopherd app stop                   # stop a running service
./gopherd app restart                # restart a service
./gopherd app status                 # show service status
./gopherd signal haproxy SIGUSR2     # send a signal to a running service (e.g. reload)
./gopherd logs app                   # show recent logs for a service
./gopherd logs app -f                # stream logs (follow mode, like tail -f)
./gopherd stats                      # show service and check statistics
./gopherd reload                     # hot-reload config (add/remove/update services)
```

Override socket path with `GOPHERD_SOCKET` env var (default: `/run/gopherd.sock`).

#### Entrypoint passthrough

When invoked with arguments that aren't known client commands, `gopherd` execs the command directly (replacing the process). This is useful for debugging containers:

```bash
docker run myimage /bin/sh           # drops into a shell, bypasses init
docker run myimage ls -la /etc       # runs ls, exits
```

Known client commands (`list`, `stats`, `start`, `stop`, `restart`, `status`, `signal`, `logs`, `reload`) still go to client mode.

#### Entrypoint extra args

Pass Docker `CMD` or Kubernetes `args` through to a specific service. This lets you configure a service at runtime without wrapper scripts.

Mark one service with `extra-args: entrypoint` in the config:

```yaml
processes:
  - name: controller
    command: /usr/local/sbin/myapp
    args: ["--base-flag"]
    extra-args: entrypoint           # appends entrypoint args to this service
```

Then pass extra arguments:

```bash
# Docker — args after "--" or flag-style args are forwarded
docker run myimage -- --log-level=debug --feature-x
docker run myimage --log-level=debug --feature-x

# Kubernetes — pod args are forwarded (ENTRYPOINT = gopherd in Dockerfile)
containers:
  - name: app
    image: myimage
    args: ["--log-level=debug", "--feature-x"]
```

The service receives `["--base-flag", "--log-level=debug", "--feature-x"]`. Only one service may use `extra-args: entrypoint`.

#### Docker

```dockerfile
FROM your-base-image
COPY gopherd /sbin/gopherd
COPY gopherd.yml /var/lib/gopherd/gopherd.yml
ENTRYPOINT ["/sbin/gopherd"]
# Normal: runs as PID 1 init system
# Debug:  docker run <image> /bin/sh  → passthrough to shell
```

### Configuration

Configuration is a single YAML file (no external YAML library — built-in parser). See the [example/](example/) directory for ready-to-use configs including a minimal setup, HAProxy ingress pattern, and a comprehensive all-options reference.

Below is a full example showing all available options:

```yaml
# Global log prefix format (space-separated tokens, applied in order).
# Tokens: "timestamp" (UTC timestamp), "service" ([name] tag).
#   "service timestamp"  — [app] 2021-05-13T03:16:51.001Z line  (default)
#   "timestamp service"  — 2021-05-13T03:16:51.001Z [app] line
#   "timestamp"          — timestamp only, no service name
#   "service"            — service name only, no timestamp
#   "none"               — no prefix at all (raw output)
# prefix: "service timestamp"

# Control socket
control:
  socket: /run/gopherd.sock          # Unix socket path for runtime control

# Processes
processes:
  # Oneshot: runs once to completion before dependents start
  - name: init-config
    command: /usr/local/bin/setup-config
    startup: oneshot                 # run once, block until done
    on-failure: ignore               # optional: continue even if it fails

  - name: app
    command: /usr/local/bin/myapp
    args: ["--config", "/etc/app.conf", "-m", "{{.MEMLIMIT}}"]
    dotenv: /etc/app.env             # load KEY=value env vars from file
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
    # prefix: "none"                 # per-process prefix override (see global prefix)
    environment:
      DB_HOST: localhost
      LOG_LEVEL: info
    on-check-failure:
      health: restart                # action when named check breaches threshold

  - name: sidecar
    command: /usr/local/bin/sidecar
    extra-args: entrypoint           # append Docker CMD / K8s args to this service
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
| `args` | string[] | `[]` | Command arguments (supports Go templates, e.g. `{{.MEMLIMIT}}`) |
| `dotenv` | string | | Path to env file (`KEY=value` per line), loaded into templates and child env |
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
| `extra-args` | string | | `"entrypoint"`: append Docker/K8s entrypoint args to this service |
| `ready-check` | string | | Health check name that gates dependents |
| `ready-timeout` | duration | `"60s"` | Max wait for ready check to pass |
| `prefix` | string | `"service timestamp"` | Log prefix format: `"service timestamp"`, `"timestamp service"`, `"timestamp"`, `"service"`, `"none"` |

#### Exit actions

| Action | Description |
|:-------|:------------|
| `restart` | Restart the process with exponential backoff |
| `shutdown` | Exit gopherd with the service's actual exit code |
| `success-shutdown` | Exit gopherd with code 0 |
| `failure-shutdown` | Exit gopherd with the service's actual exit code |
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
- Exit codes from services are propagated as gopherd's own exit code

### Contributing

Thanks for your interest in the project and your willingness to contribute:

- Pull requests are welcome!
- For commit messages and general style please follow the haproxy project's [CONTRIBUTING guide](https://github.com/haproxy/haproxy/blob/master/CONTRIBUTING) and use that where applicable.

### Discussion

A Github issue is the right place to discuss feature requests, bug reports or any other subject that needs tracking.

## License

[Apache License 2.0](LICENSE)
