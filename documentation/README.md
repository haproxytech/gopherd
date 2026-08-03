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

## Reference

- [all-options](all-options/) — every option in one commented config; not
  runnable, but CI-validated: it must pass the config loader and mention
  every option key the parser reads

## Basics

- [minimal](minimal/) — single process supervised by gopherd
- [multi-service](multi-service/) — several independent services
- [environment-passthrough](environment-passthrough/) — `pass-env` forwarding
- [remove-env](remove-env/) — strip keys from a child's environment
- [entrypoint-args](entrypoint-args/) — `use-entrypoint-args` and passthrough exec

## Ordering & lifecycle

- [dependencies](dependencies/) — `after:` start ordering
- [requires](requires/) — hard dependencies with failure coupling
- [stop-signal](stop-signal/) — per-service `stop-signal` + `kill-delay` escalation
- [oneshot](oneshot/) — run-to-completion startup tasks
- [shutdown-order](shutdown-order/) — `shutdown-order` strategies
- [startup-timeout](startup-timeout/) — kill hung oneshots
- [restart-backoff](restart-backoff/) — `on-failure: restart` with exponential backoff

## Health & readiness

- [health-check-http](health-check-http/) — HTTP health check
- [health-check-tcp](health-check-tcp/) — TCP connect check
- [health-check-exec](health-check-exec/) — command-based check
- [health-check-http-unix](health-check-http-unix/) — HTTP check over a Unix socket
- [on-check-failure](on-check-failure/) — check-driven restart (self-healing)
- [ready-gate](ready-gate/) — block dependents until a check passes
- [sd-notify](sd-notify/) — systemd `READY=1` readiness gate

## Signals & exit

- [signal-rewrite](signal-rewrite/) — map forwarded signals
- [exit-code-map](exit-code-map/) — remap child exit codes
- [init-stop-signal](init-stop-signal/) — which signals stop gopherd

## Templating

- [env-templates](env-templates/) — `{{.VAR}}` / `{{.VAR:-default}}`
- [dotenv](dotenv/) — `KEY=value` file into templates and child env
- [service-gating](service-gating/) — enable a service via env var (`startup: "{{.START_X}}"`)
- [cpu-template](cpu-template/) — `{{cpu EXPR}}`
- [cpu-headroom](cpu-headroom/) — `{{cpu 100% - 1}}` reserve a core
- [mem-template](mem-template/) — `{{mem EXPR}}`
- [mem-headroom](mem-headroom/) — `{{mem 100% - 64MiB}}` reserve memory
- [mem-golang-template](mem-golang-template/) — `GOMEMLIMIT` for a Go app
- [file-inclusion](file-inclusion/) — `{{file "/path"}}`

## Logging

- [log-capture](log-capture/) — opt-in output capture vs direct FD passthrough (default)
- [log-prefix](log-prefix/) — per-line timestamp + service prefix
- [log-file-rotation](log-file-rotation/) — size-based file rotation
- [syslog-target](syslog-target/) — forward logs to syslog
- [log-streaming](log-streaming/) — `logs` history and `logs -f` live follow

## Runtime control

- [control-socket](control-socket/) — start/stop/status over the socket
- [export-socket](export-socket/) — client commands from inside services
- [hot-reload](hot-reload/) — `gopherd reload` / SIGHUP config reconcile
- [subreaper](subreaper/) — `PR_SET_CHILD_SUBREAPER` for non-PID-1 use
