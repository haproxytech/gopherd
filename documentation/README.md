# gopherd Documentation Examples

Each subfolder is a self-contained, **CI-tested** example: a runnable
`example.yml`, a `README.md` explaining it, and a Go test that proves the
documented behavior. Examples use realistic placeholder commands (e.g.
`/usr/local/bin/myapp`); the tests substitute runnable stand-ins so the same
config is both copy-paste documentation and a live test.

Run all example tests:

```bash
go test ./documentation/...
```

## Basics

- [minimal](minimal/) — single process supervised by gopherd
- [multi-service](multi-service/) — several independent services
- [environment-passthrough](environment-passthrough/) — `pass-env` forwarding

## Ordering & lifecycle

- [dependencies](dependencies/) — `after:` start ordering
- [oneshot](oneshot/) — run-to-completion startup tasks
- [shutdown-order](shutdown-order/) — `shutdown-order` strategies
- [startup-timeout](startup-timeout/) — kill hung oneshots

## Health & readiness

- [health-check-http](health-check-http/) — HTTP health check
- [health-check-tcp](health-check-tcp/) — TCP connect check
- [health-check-exec](health-check-exec/) — command-based check
- [ready-gate](ready-gate/) — block dependents until a check passes
- [sd-notify](sd-notify/) — systemd `READY=1` readiness gate

## Signals & exit

- [signal-rewrite](signal-rewrite/) — map forwarded signals
- [exit-code-map](exit-code-map/) — remap child exit codes
- [init-stop-signal](init-stop-signal/) — which signals stop gopherd

## Templating

- [env-templates](env-templates/) — `{{.VAR}}` / `{{.VAR:-default}}`
- [service-gating](service-gating/) — enable a service via env var (`startup: "{{.START_X}}"`)
- [cpu-template](cpu-template/) — `{{cpu EXPR}}`
- [cpu-headroom](cpu-headroom/) — `{{cpu 100% - 1}}` reserve a core
- [mem-template](mem-template/) — `{{mem EXPR}}`
- [mem-headroom](mem-headroom/) — `{{mem 100% - 64MiB}}` reserve memory
- [mem-golang-template](mem-golang-template/) — `GOMEMLIMIT` for a Go app
- [file-inclusion](file-inclusion/) — `{{file "/path"}}`

## Logging

- [log-prefix](log-prefix/) — per-line timestamp + service prefix
- [log-file-rotation](log-file-rotation/) — size-based file rotation
- [syslog-target](syslog-target/) — forward logs to syslog

## Runtime control

- [control-socket](control-socket/) — start/stop/status over the socket
- [subreaper](subreaper/) — `PR_SET_CHILD_SUBREAPER` for non-PID-1 use
