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
- **Template args** — `{{.VAR}}` and `{{.VAR:-default}}` placeholders in args, environment values, and `startup`, resolved from env vars and dotenv files
- **Memory-aware templates** — `{{mem EXPR}}` expands to available memory in MiB (auto-detects system RAM and cgroup limits)
- **CPU-aware templates** — `{{cpu EXPR}}` expands to available CPUs (auto-detects cgroup CFS quota and cpuset pinning)
- **File-inclusion templates** — `{{file "/path"}}` reads a file's contents at expansion time; supports `trim` and `follow` modifiers and `:-default` fallback. Primary use: Docker/K8s/systemd secrets
- **Restart policies** — configurable `on-success` / `on-failure` actions: `restart`, `shutdown`, `ignore`
- **Exponential backoff** — configurable delay, factor, and limit for restart attempts
- **Service dependencies** — `after`, `before`, `requires` with topological sort
- **Oneshot tasks** — run-once init tasks (e.g. config generation, permission setup) that complete before dependents start, with optional `startup-timeout`
- **Health checks** — HTTP (including over Unix socket), TCP, and exec-based checks with configurable period, timeout, and threshold
- **Readiness gates** — block dependent services until a health check passes or the service writes `READY=1` to `$NOTIFY_SOCKET` (systemd-compatible sd_notify)
- **Log prefixing** — service name and timestamp on every output line (configurable format)
- **Log targets** — forward logs to syslog (UDP/TCP) or files
- **Status reporting** — service uptime, restarts, exits, and health check results via `gopherd status`
- **Control socket** — start/stop/restart/status/signal/reload/logs services at runtime via Unix socket
- **Log streaming** — `gopherd logs <service> -f` for live log tailing via control socket
- **Hot reload** — `gopherd reload` or SIGHUP to re-read config and reconcile services without restart
- **Exit code propagation** — gopherd exits with the actual exit code of the service that triggered shutdown
- **Entrypoint args** — pass Docker/Kubernetes entrypoint arguments to a designated service via `use-entrypoint-args: true`
- **Entrypoint passthrough** — `docker run <image> /bin/sh` execs the command directly, bypassing the init system
- **Graceful shutdown** — configurable shutdown ordering: `reverse-dep` (dependents first, default), `dep` (dependencies first), or `simultaneous` (all at once)
- **No root required** — works in rootless containers

### Usage

#### Install

Via `go install` (builds from source, requires Go toolchain):

```bash
go install -trimpath -ldflags="-s" github.com/haproxytech/gopherd@latest     # latest tagged release
go install -trimpath -ldflags="-s" github.com/haproxytech/gopherd@v1.2.3     # specific version
```

`-trimpath -ldflags="-s"` strips symbol tables and local paths for a ~30% smaller binary. Omit them if you need DWARF for profiling/debugging.

The binary lands in `$(go env GOBIN)` (or `$(go env GOPATH)/bin`).

Pre-built release binaries (no Go toolchain needed) are published on the [releases page](https://github.com/haproxytech/gopherd/releases) for linux/darwin/freebsd across amd64/arm64/arm/386/ppc64le/riscv64/s390x. Archive names follow `gopherd_<version>_<OS>_<arch>.tar.gz` (e.g. `Linux_x86_64`, `Linux_arm64`, `Darwin_arm64`):

```bash
VERSION=1.2.3
curl -fsSLO "https://github.com/haproxytech/gopherd/releases/download/v${VERSION}/gopherd_${VERSION}_Linux_x86_64.tar.gz"
tar -xzf "gopherd_${VERSION}_Linux_x86_64.tar.gz"
sudo install -m 0755 gopherd /usr/local/bin/gopherd
```

Verify the download against `checksums.txt` from the same release before installing.

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

Default config path: `/etc/gopherd/gopherd.yml` (override via `GOPHERD_CONFIG` env var).

#### Runtime control (client mode)

When invoked with a known command, `gopherd` connects to the running daemon via Unix socket:

```bash
./gopherd app start                  # start a stopped service
./gopherd app stop                   # stop a running service
./gopherd app restart                # restart a service
./gopherd app status                 # show one service's status
./gopherd status                     # overview of all services and checks
./gopherd status -o json             # overview as JSON (pipe to jq, etc.)
./gopherd status app -o json         # one service as JSON
./gopherd signal haproxy SIGUSR2     # send a signal to a running service (e.g. reload)
./gopherd logs app                   # show recent logs for a service
./gopherd logs app -f                # stream logs (follow mode, like tail -f)
./gopherd reload                     # hot-reload config (add/remove/update services)
./gopherd version                    # print version, repo, and commit date
./gopherd tag                        # print just the version tag (handy for CI scripts)
```

The `start`/`stop`/`restart`/`status` actions accept either order: `gopherd app stop` and `gopherd stop app` are equivalent.

Override the control socket path with the `GOPHERD_SOCKET` env var (default: `/run/gopherd.sock`). It applies to both the daemon and the client, and takes precedence over `control: socket:` in the config — handy for rootless deployments where `/run` is not writable (point it at a writable path).

#### Entrypoint passthrough

When invoked with arguments that aren't known client commands, `gopherd` execs the command directly (replacing the process). This is useful for debugging containers:

```bash
docker run myimage /bin/sh           # drops into a shell, bypasses init
docker run myimage ls -la /etc       # runs ls, exits
```

Known client commands (`start`, `stop`, `restart`, `status`, `signal`, `logs`, `reload`) still go to client mode.

#### Entrypoint extra args

Pass Docker `CMD` or Kubernetes `args` through to a specific service. This lets you configure a service at runtime without wrapper scripts.

Mark one service with `use-entrypoint-args: true` in the config:

```yaml
processes:
  - name: controller
    command: /usr/local/sbin/myapp
    args: ["--base-flag"]
    use-entrypoint-args: true           # appends entrypoint args to this service
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

The service receives `["--base-flag", "--log-level=debug", "--feature-x"]`. Only one service may set `use-entrypoint-args: true`.

#### Environment-variable templates

The `{{.VAR}}` syntax expands to the value of environment variable `$VAR`. Lookups check per-process `environment:` entries first, then any loaded `dotenv:` file, then the process environment gopherd inherited. Missing variables expand to an empty string.

Use `{{.VAR:-default}}` to supply a fallback when the variable is unset **or** empty — the same behavior as POSIX `${VAR:-default}`:

```yaml
processes:
  - name: app
    command: /usr/local/bin/app
    args: ["--log-level={{.LOG_LEVEL:-info}}", "--host={{.HOST:-127.0.0.1}}"]
    environment:
      REGION: "{{.REGION:-us-east-1}}"
```

The default text is a literal string — it cannot contain `}}` and is not itself re-expanded.

Env-var templates also work in the `startup` field (see [Service gating](#service-gating)).

#### Service gating

The `startup` field accepts env-var templates, so a service can be enabled or disabled based on an env var set at container launch:

| Config                             | `$START_X` unset or empty | `$START_X=oneshot` | `$START_X=garbage` |
|:-----------------------------------|:--------------------------|:-------------------|:-------------------|
| `startup: "{{.START_X}}"`          | disabled                  | oneshot            | config error       |
| `startup: "{{.START_X:-enabled}}"` | enabled                   | oneshot            | config error       |

An env-var reference that resolves to empty is remapped to `disabled` — this is the only field where empty-after-expansion has special meaning. A literal empty `startup:` still falls through to the default (`enabled`).

Typos in `startup` (e.g. `enable` instead of `enabled`) now fail at config load.

Hot reload (`SIGHUP` / `reload`) re-reads the config and picks up env-var changes; gopherd does not watch the environment for changes between reloads.

Two settings are bound once at startup and are **not** applied by a hot reload — changing them requires a full restart (gopherd logs a warning if a reload changes either):

- `init-stop-signal` — the shutdown signal set is subscribed at startup.
- `control.socket` / `control.socket-mode` — the control socket is bound at startup.

A runnable example lives in [documentation/service-gating/](documentation/service-gating/).

#### sd_notify readiness (systemd-compatible)

Some services know exactly when they are ready to serve traffic — an HTTP health check can only approximate this with polling. For such services, set `sd-notify: true`:

```yaml
processes:
  - name: haproxy
    command: /usr/local/sbin/haproxy
    args: [-W, -db, -f, /etc/haproxy.cfg]
    sd-notify: true
    sd-notify-timeout: 60s

  - name: hug
    command: /usr/local/sbin/hug
    after: [haproxy]   # dependents start only after haproxy writes READY=1
```

On spawn, gopherd:

1. Allocates a per-service abstract unix datagram socket.
2. Sets `NOTIFY_SOCKET=@gopherd-sd-notify-<pid>-<service>` in the child env.
3. Blocks the next service in topological order until `READY=1\n` arrives on that socket, or `sd-notify-timeout` expires.

The child speaks the subset of the sd_notify protocol defined by [sd_notify(3)](https://www.freedesktop.org/software/systemd/man/sd_notify.html): `READY=1` marks readiness, `STOPPING=1` signals draining, and `STATUS=<text>` updates a human-readable status line. Other keys are accepted and ignored.

This works alongside `ready-check`: `ready-check` gates this service on a *dependency* being up (pre-spawn), `sd-notify` gates *dependents* on this service becoming ready (post-spawn). They compose cleanly.

#### Memory-aware templates

The `{{mem EXPR}}` syntax expands to an integer value in MiB based on available memory. Available memory is the minimum of system RAM (from `/proc/meminfo`) and any cgroup limit (v1 or v2).

Supported expressions:

| Expression | Description |
|:-----------|:------------|
| `{{mem 66%}}` | 66% of available memory |
| `{{mem 100% - 200MB}}` | All memory minus 200 MB |
| `{{mem 512MiB}}` | Absolute value (passthrough) |

Units: `MB` (decimal, 1000^2), `MiB` (binary, 1024^2), `GB`, `GiB`. The result is always a whole number of MiB.

Templates work in both `args` and `environment` values:

```yaml
processes:
  - name: haproxy
    command: /usr/local/sbin/haproxy
    args: ["-m", "{{mem 66%}}"]       # HAProxy -m flag takes MiB

  - name: app
    command: /usr/local/bin/app
    environment:
      GOMEMLIMIT: "{{mem 33%}}MiB"    # Go runtime expects value + unit
```

On a container with a 3 GiB cgroup limit, this resolves to `-m 2048` and `GOMEMLIMIT=1024MiB`.

> **Always use `MiB` as the literal suffix.** `{{mem ...}}` emits a bare number in MiB, so a different suffix mislabels it: `}}GiB` inflates the value 1024×, and `}}MB` is rejected outright by the Go runtime (`GOMEMLIMIT` only accepts `B`, `KiB`, `MiB`, `GiB`, `TiB`).

#### CPU-aware templates

The `{{cpu EXPR}}` syntax expands to an integer CPU count based on available CPUs. Available CPUs are the minimum of system online CPUs (`runtime.NumCPU()`), CFS bandwidth quota (`--cpus`), and cpuset pinning (`--cpuset-cpus`). Both cgroup v1 and v2 are supported.

Supported expressions:

| Expression | Description |
|:-----------|:------------|
| `{{cpu}}` | Available CPU count |
| `{{cpu 50%}}` | 50% of available CPUs (rounded up, minimum 1) |
| `{{cpu 100% - 1}}` | All CPUs minus 1 (minimum 1) |
| `{{cpu 50% - 1}}` | 50% minus 1 (minimum 1) |

The result is always an integer >= 1.

Templates work in both `args` and `environment` values:

```yaml
processes:
  - name: haproxy
    command: /usr/local/sbin/haproxy
    args: ["-W", "-db", "-m", "{{mem 66%}}"]

  - name: api
    command: /usr/local/bin/gunicorn
    args: ["--workers", "{{cpu}}", "app:wsgi"]

  - name: app
    command: /usr/local/bin/app
    environment:
      GOMAXPROCS: "{{cpu}}"

  - name: worker
    command: /usr/local/bin/worker
    args: ["--threads", "{{cpu 50%}}"]
```

On a container with `--cpus=4`, this resolves to `--workers 4`, `GOMAXPROCS=4`, and `--threads 2`.

#### File-inclusion templates

The `{{file "/abs/path"}}` syntax expands to the contents of a file at the given absolute path. Three situations make this the right tool over `{{.VAR}}`:

**Avoiding env-var leakage.** A secret passed via env (`DB_PASSWORD=...`) is visible to anyone who can read `/proc/<pid>/environ`, shows up in `ps eww`, gets captured by any crash dumper or APM agent that grabs the process environment, and is inherited unchanged by every child the process forks. Pulling the secret from a file at exec time means the value lives in the child's address space only — it is not part of the env block the kernel stores on the process, so the usual env-snooping vectors return nothing. Docker `--env-file`, K8s `valueFrom: secretKeyRef`, and HashiCorp Vault all push the secret into env; `{{file ...}}` lets gopherd consume the same secret in the file form those systems mount alongside (under `/run/secrets/`, `/var/run/secrets/`, etc.) without ever putting it on `os.Environ()`.

**Containers with a read-only root filesystem.** A hardened image runs with `--read-only` (Docker) or `securityContext.readOnlyRootFilesystem: true` (K8s); the only writable surfaces are tmpfs mounts the orchestrator chooses — typically `/run/secrets/`, `/var/run/`, or an explicit `emptyDir`. Configuration files can ship baked into the image, but the secret material has to come from a tmpfs the orchestrator populates at start. `{{file ...}}` is what you point the YAML at to consume those tmpfs files without templating them into a writable config first.

**Multi-line content.** TLS certificates, private keys, signed JWTs, PEM bundles, JSON service-account files, and CA chains all carry embedded newlines. Env vars technically allow newlines but most shells, `--env-file` parsers, and orchestrators either reject or silently corrupt them, and inline multi-line YAML (`|`, `>-`) forces the secret into the config file. A file reference keeps the bytes byte-for-byte intact: `{{file "/run/secrets/tls.key"}}` produces the same content the application would read if it opened the file itself, with no quoting, escaping, or line-ending mangling along the way.

Supported forms:

| Expression | Description |
|:-----------|:------------|
| `{{file "/run/secrets/db_password"}}` | Raw file contents (includes trailing newline if present) |
| `{{file "/run/secrets/db_password" trim}}` | Right-trims trailing whitespace and newlines |
| `{{file "/var/run/secrets/app/token" follow}}` | Permits symlinks — required for K8s secret volume mounts, whose keys are symlinks into `..data/` |
| `{{file "/etc/license.key":-no-license}}` | Falls back to literal `no-license` when the file does not exist |
| `{{file "/run/secrets/api_token" trim:-anon}}` | Both: trim if found, fallback if missing |

Modifiers combine in any order: `{{file "/path" follow trim:-default}}`.

Where it works: `args`, `environment` values, and `startup`. Paths must be absolute.

> **Security note — do not put secrets in `args`.** A value expanded into `args`
> becomes part of the child's argv, which is world-readable via
> `/proc/<pid>/cmdline` and `ps`. That is *strictly weaker* than the
> `/proc/<pid>/environ` exposure (owner-and-root only) this feature is meant to
> avoid, so `{{file}}` in `args` defeats its own purpose for secret material.
> Keep secrets in `environment:` values; reserve `{{file}}` in `args` for
> non-sensitive content (version strings, feature flags, public config). gopherd
> logs a warning at config load when it sees `{{file}}` inside `args`.

When the file is read:

- `startup`: at config-load time (once per `gopherd reload` / SIGHUP).
- `args` and `environment`: at each service start, so a `gopherd <svc> restart` picks up a rotated secret without a config reload.

> **K8s secrets rotate in place — gopherd does not watch them.** When a
> Secret object changes, the kubelet swaps the `..data` symlink atomically
> and the mounted value changes under the same path. A `{{file ... follow}}`
> value is a snapshot taken at service start; a long-running service keeps
> the old value until its next restart. This pattern therefore fits
> short-lived processes (oneshots, batch jobs) best — for long-lived
> services, restart on rotation or have the application read the file
> itself.

Error behavior:

- Missing file with no `:-default` is a hard error.
- Missing file with `:-default` expands to the literal default text. Same `${VAR:-default}` semantics as env-var templates.
- A present-but-unreadable file (permission denied, path-is-a-directory, etc.) is a hard error even with `:-default` — that is an operator misconfiguration, not a fallback case.
- Symlinks are refused (the file itself and every ancestor directory) unless the reference carries the `follow` modifier: gopherd may run as root, and a symlink could redirect the read to a root-only file. Kubernetes secret/configmap/projected volumes deliver each key as a symlink into `..data/`, so use `follow` for those mounts; Docker secrets are regular files and need no modifier. A dangling symlink with `follow` counts as missing (`:-default` applies).
- Only regular files are read — FIFOs, devices, and files containing NUL bytes are hard errors (NUL cannot survive argv/env).
- 1 MiB size cap per file to make `{{file "/var/log/huge.log"}}` fail loudly. Secrets and license keys are well under this.

Example consuming a Docker/K8s secret mount:

```yaml
processes:
  - name: api
    command: /usr/local/bin/api
    environment:
      DB_PASSWORD: '{{file "/run/secrets/db_password" trim}}'                    # Docker secret: regular file
      API_TOKEN:   '{{file "/var/run/secrets/app/api_token" follow trim}}'       # K8s secret volume: keys are symlinks
      LICENSE:     '{{file "/etc/license.key":-no-license}}'
      TLS_CERT:    '{{file "/run/secrets/tls.crt"}}'  # secrets go in env, never args
    # args: only non-sensitive content — argv is world-readable via /proc/<pid>/cmdline
    args: ["--build={{file \"/etc/build-id\" trim}}"]
```

#### Docker

```dockerfile
FROM your-base-image
COPY gopherd /sbin/gopherd
COPY gopherd.yml /etc/gopherd/gopherd.yml
ENTRYPOINT ["/sbin/gopherd"]
# Normal: runs as PID 1 init system
# Debug:  docker run <image> /bin/sh  → passthrough to shell
```

If your Dockerfile uses a custom `STOPSIGNAL`, add it to `init-stop-signal` so gopherd shuts down gracefully when the runtime sends it:

```dockerfile
STOPSIGNAL SIGQUIT
```

```yaml
# gopherd.yml
init-stop-signal: [SIGTERM, SIGINT, SIGQUIT]
```

### Configuration

Configuration is a single YAML file (no external YAML library — built-in parser). See the [example/](example/) directory for ready-to-use configs including a minimal setup, HAProxy ingress pattern, and a comprehensive all-options reference. The [documentation/](documentation/) directory contains examples — one folder per feature, each with a runnable config, a README, and a Go test proving the documented behavior.

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

# Global pass-env default. When false (the default), services start with
# an empty environment — only variables from dotenv and per-process
# environment are passed to children. This prevents operator secrets in
# gopherd's own environment from leaking into every service. Set to true
# to forward gopherd's OS environment to all services; individual services
# can override with per-service pass-env: true/false.
# pass-env: false

# Control socket
control:
  socket: /run/gopherd.sock          # Unix socket path for runtime control
  socket-mode: "0600"                # socket file permissions, octal (default: 0600)

# Processes
processes:
  # Oneshot: runs once to completion before dependents start
  - name: init-config
    command: /usr/local/bin/setup-config
    startup: oneshot                 # run once, block until done
    startup-timeout: 30s             # kill if not done in 30s (default: no limit)
    on-failure: ignore               # optional: continue even if it fails

  - name: app
    command: /usr/local/bin/myapp
    args: ["--config", "/etc/app.conf", "-m", "{{.MEMLIMIT}}", "--threads", "{{cpu}}"]
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
    use-entrypoint-args: true           # append Docker CMD / K8s args to this service
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
  app-log:
    type: file
    location: /var/log/app.log
    services: [app]
    max-size: 10MiB                  # rotate when the file grows past this
    max-files: 5                     # keep N rotated files (default: 5)
    compress: true                   # gzip rotated files (app.log.1.gz, ...)
```

File-target rotation keys (all optional; omit `max-size` to disable rotation):

| Field | Default | Description |
|:------|:--------|:------------|
| `max-size` | unset | Rotate when the current file would exceed this size. Units: `B`, `KB`, `KiB`, `MB`, `MiB`, `GB`, `GiB`. |
| `max-files` | 5 (when `max-size` set) | Number of rotated files to retain. Older files are deleted on rotation. |
| `compress` | false | gzip rotated files. Rotated names become `<path>.1.gz`, `<path>.2.gz`, … |

`labels` (optional, any target type) prepends metadata to every forwarded line as sorted logfmt-style `key=value` pairs (values containing spaces or `"` are quoted). For example `labels: {env: production}` turns `[app] 2026-… started` into `env=production [app] 2026-… started`, so downstream syslog/file consumers can filter or attribute lines by target.

### Configuration Reference

#### Global fields

| Field | Type | Default | Description |
|:------|:-----|:--------|:------------|
| `prefix` | string | `"service timestamp"` | Log prefix format for all services |
| `pass-env` | bool | `false` | Global default: forward gopherd's OS environment to children (false = empty env, children see only dotenv + per-process `environment`) |
| `no-logo` | bool | `false` | Suppress ASCII art banner at startup |
| `shutdown-order` | string | `"reverse-dep"` | Shutdown strategy: `reverse-dep`, `dep`, or `simultaneous` |
| `init-stop-signal` | string[] | `[SIGTERM, SIGINT]` | Signals that trigger gopherd's own graceful shutdown. `SIGKILL` and `SIGSTOP` are rejected at load (cannot be caught). |
| `subreaper` | bool | `false` | Set `PR_SET_CHILD_SUBREAPER` so orphaned descendants re-parent to gopherd (and get reaped by its `Wait4` loop) instead of the real PID 1. Useful when gopherd is not PID 1 (k8s sidecar, `docker exec`, nested init). Linux only; warns and no-ops elsewhere. |

#### Process fields

| Field | Type | Default | Description |
|:------|:-----|:--------|:------------|
| `name` | string | command path | Service name for logging and control |
| `command` | string | *required* | Executable path |
| `args` | string[] | `[]` | Command arguments (supports `{{.VAR}}` / `{{.VAR:-default}}`, `{{mem EXPR}}`, `{{cpu EXPR}}`, and `{{file "/path"}}` templates) |
| `environment` | map | inherited | Extra environment variables (values support the same template forms as `args`) |
| `dotenv` | string | | Path to env file (`KEY=value` per line), loaded into templates and child env. Symlinks (leaf and ancestors) are refused by default, same as `{{file}}` |
| `dotenv-follow` | bool | `false` | Permit symlinks when opening `dotenv`, confining resolution to the file's directory. Enable for K8s `..data/` secret mounts or paths under system symlinks like `/var/run` → `/run`. Mirrors the `{{file}}` `follow` modifier |
| `working-dir` | string | inherited | Working directory |
| `user` | string | inherited | Run as user (name) |
| `group` | string | inherited | Run as group (name) |
| `user-id` | int | inherited | Run as user (numeric, takes precedence) |
| `group-id` | int | inherited | Run as group (numeric, takes precedence) |
| `strict-groups` | bool | `false` | When an explicit group is set, drop the named user's supplementary groups instead of keeping full membership |
| `pass-env` | bool | global default | Forward gopherd's OS environment to this service (false = empty env + only dotenv/environment vars) |
| `remove-env` | list | `[]` | Env keys to delete from the final child environment, regardless of source (OS env / dotenv / `environment:`) |
| `startup` | string | `"enabled"` | `"enabled"`, `"disabled"`, or `"oneshot"`. Supports `{{.VAR}}` / `{{.VAR:-default}}` and `{{file "/path"}}`; empty after expansion → disabled. Oneshots with no `after`/`requires` edge between them run concurrently; dependents wait for all oneshots in the prior layer to exit cleanly |
| `startup-timeout` | duration | | Max time for oneshot to complete (kills and fails if exceeded) |
| `stop-signal` | string | `"SIGTERM"` | Signal name (with or without SIG prefix) |
| `kill-delay` | duration | `"5s"` | Grace period before SIGKILL |
| `on-success` | string | `"shutdown"` | Action on exit 0 |
| `on-failure` | string | `"shutdown"` | Action on non-zero exit |
| `backoff-delay` | duration | `"500ms"` | Initial restart backoff delay |
| `backoff-factor` | float | `2.0` | Backoff multiplier |
| `backoff-limit` | duration | `"30s"` | Max backoff delay |
| `after` | string[] | `[]` | Start after these services |
| `before` | string[] | `[]` | Start before these services |
| `requires` | string[] | `[]` | Hard dependencies. If a required service fails, its running dependents are stopped (systemd `Requires=` semantics). Dependents are **not** automatically restarted when the dependency recovers — they stay stopped until manually restarted |
| `on-check-failure` | map | `{}` | Check name -> action mapping |
| `use-entrypoint-args` | bool | `false` | append Docker/K8s entrypoint args to this service |
| `ready-check` | string | | Health check name that gates dependents |
| `ready-timeout` | duration | `"60s"` | Max wait for ready check to pass |
| `sd-notify` | bool | `false` | Enable systemd-compatible sd_notify readiness: gopherd sets `$NOTIFY_SOCKET` in the child env; dependents wait until the child writes `READY=1` to that socket |
| `sd-notify-timeout` | duration | `"60s"` | Max wait for `READY=1` after spawn when `sd-notify: true` |
| `parent-death-signal` | string | | Signal name (e.g. `SIGTERM`, `SIGKILL`) delivered by the kernel to this child when its parent thread dies (`prctl(PR_SET_PDEATHSIG)`). Ensures children do not linger if gopherd is killed abruptly. Linux only. |
| `exit-code-map` | map | `{}` | Remap observed child exit codes before `on-success`/`on-failure` dispatch and before propagation as gopherd's own exit code. Keys and values accept either an integer exit code or a signal name (shell convention: `SIGTERM` = 128+15 = 143, `SIGKILL` = 137, etc.), so `{SIGTERM: 0, SIGKILL: 0}` is the idiomatic "treat signal-induced exits as success in CI". |
| `signal-rewrite` | map[string]string | `{}` | Opt this service into signal forwarding. Keys and values are signal names (`SIGUSR1`, `USR1`, …). When unset/empty, gopherd does **not** forward arbitrary received signals to the child; forwarding is strictly opt-in. Keys may not overlap with `SIGTERM`/`SIGINT`/`SIGHUP` or the global `stop-signal` — those are consumed by gopherd's own shutdown/reload handlers before the forward branch runs, and overlapping entries are rejected at config load. |
| `prefix` | string | `"service timestamp"` | Log prefix format: `"service timestamp"`, `"timestamp service"`, `"timestamp"`, `"service"`, `"none"` |

#### Exit actions

| Action | Description |
|:-------|:------------|
| `restart` | Restart the process with exponential backoff |
| `shutdown` | Exit gopherd with the service's actual exit code |
| `success-shutdown` | Exit gopherd with code 0 |
| `failure-shutdown` | Exit gopherd with the service's actual exit code |
| `ignore` | Leave process stopped, don't trigger shutdown |

gopherd does not exit just because no services are running. When the last
service stops (via `stop` on the control socket, or an `ignore` exit), gopherd
stays alive and idle so the service can be started again. It exits only when a
service triggers a shutdown action or it receives an `init-stop-signal`.

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
| `service/` | Service lifecycle, signal parsing, user/group resolution |
| `check/` | Health checks (HTTP, TCP, exec), unix socket transport, readiness gates |
| `control/` | Unix socket control server + CLI client |
| `version/` | Build version from Go's embedded VCS metadata |
| `internal/yml/` | Built-in YAML parser and config loader (no external deps) |
| `internal/backoff/` | Exponential backoff with jitter for restarts |
| `internal/cgroup/` | Shared cgroup v1/v2 helpers (path discovery, safe file reads, walk-up) |
| `internal/cpu/` | System and cgroup CPU detection, CPU expression parser |
| `internal/logger/` | Line-buffered prefix writer, syslog and file log target forwarding |
| `internal/memory/` | System and cgroup memory detection, memory expression parser |
| `internal/metrics/` | In-memory service and check statistics |
| `internal/order/` | Topological sort for service dependencies |
| `internal/sdnotify/` | systemd sd_notify readiness listener (abstract-namespace datagram socket) |

Core design:

- YAML config defines processes, checks, log targets, and control socket
- Zero external dependencies — built-in YAML parser, no protobuf/prometheus
- Single `Wait4(-1)` reap loop handles both managed children and orphaned zombies (no separate reaper goroutine — avoids race with `cmd.Wait()`)
- When no children remain and no shutdown is in progress, the reap loop idles (stays a live supervisor) rather than exiting, so stopping the last service does not take gopherd down
- Graceful shutdown ordering is configurable: `reverse-dep` (dependents first, default), `dep` (dependencies first), or `simultaneous` (all at once)
- Forwards SIGTERM/SIGINT to all children using per-service stop signals; SIGHUP triggers reload. Other received signals (SIGUSR1/SIGUSR2/etc.) are forwarded to a service **only if** that service declares a `signal-rewrite` entry for them (opt-in)
- Each child gets its own process group (`Setpgid`)
- Services start in topological order based on dependency graph; independent oneshots in the same dependency layer run in parallel (and the next layer waits for the layer above to complete)
- Restart requests are handled asynchronously via a channel with backoff delays
- Control socket uses one-command-per-connection protocol over Unix domain socket (streaming for `logs -f`)
- SIGHUP triggers config hot-reload instead of being forwarded to children
- Exit codes from services are propagated as gopherd's own exit code

### Performance Tuning

gopherd's hot paths (log prefixing, health checks, control socket) are designed for zero allocations in steady state. For further GC pressure reduction, Go's built-in runtime environment variables can be used:

| Variable | Default | Recommended | Description |
|:---------|:--------|:------------|:------------|
| `GOGC` | `100` | `200`–`400` | GC target percentage. A PID 1 has a small live heap (~1–5 MB) but steady allocation from child I/O. Higher values trade a few MB of RSS for significantly fewer GC pauses. |
| `GOMEMLIMIT` | unlimited | container limit | Soft memory limit. When set, the runtime will try to keep total Go heap under this value, running GC more aggressively only when approaching it. Useful in memory-constrained containers. |

See `PERFORMANCE.md` for the full hot-path analysis and optimization details.

### Platform Support

gopherd is designed for Linux containers. It also compiles and runs on macOS and FreeBSD for development and testing, with the following limitations:

| Feature | Linux | macOS / FreeBSD |
|:--------|:------|:----------------|
| Process management, signals, zombie reaping | Full | Full |
| User/group switching | Full | Full |
| Control socket | Full | Full |
| Health checks (HTTP, TCP, exec) | Full | Full |
| `{{mem EXPR}}` — system RAM detection | Full | Full |
| `{{mem EXPR}}` — cgroup limit detection | Full | Not available (no cgroups) |
| `{{cpu EXPR}}` — system CPU detection | Full | Full |
| `{{cpu EXPR}}` — cgroup limit detection | Full | Not available (no cgroups) |
| `{{file "/path"}}` — file expansion | Full | Full |
| Control socket audit logging (peer UID) | Full (`SO_PEERCRED`) | Not available (uid=-1) |
| Config file permission/ownership checks | Full | Partial (no root ownership convention) |

#### Build targets

Release binaries cover all architectures used by the official `haproxy` Docker Library image and all `haproxytech/*` Docker images:

| OS | Architectures |
|:---|:-------------|
| Linux | amd64, arm64, arm/v5, arm/v6, arm/v7, 386, ppc64le, riscv64, s390x |
| macOS | amd64, arm64, arm, 386, ppc64le, s390x |
| FreeBSD | amd64, arm64, arm, 386, ppc64le, s390x |

Windows is not supported. See `.goreleaser.yml` for the full build matrix.

### Contributing

Thanks for your interest in the project and your willingness to contribute:

- Pull requests are welcome!
- For commit messages and general style please follow the haproxy project's [CONTRIBUTING guide](https://github.com/haproxy/haproxy/blob/master/CONTRIBUTING) and use that where applicable.

### Discussion

A Github issue is the right place to discuss feature requests, bug reports or any other subject that needs tracking.

## License

[Apache License 2.0](LICENSE)
