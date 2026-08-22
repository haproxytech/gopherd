# Service gating via env var

The `startup` field accepts env-var templates, so an environment variable set
at container launch (`docker run -e ENABLE_SIDECAR=enabled ...`) decides
whether a service auto-starts — no config edit or wrapper script needed.

## Config

```yaml
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "sleep 300"]
    on-failure: shutdown

  - name: sidecar
    command: /bin/sh
    args: ["-c", "sleep 300"]
    startup: "{{.ENABLE_SIDECAR}}"
    on-failure: shutdown
```

- `startup: "{{.ENABLE_SIDECAR}}"` — expands from gopherd's own environment
  at config load (independent of `pass-env`, which only controls what
  children inherit).
- Unset or empty expands to `disabled` — the service is defined but not
  auto-started, and can still be started via the control socket.
- `ENABLE_SIDECAR=enabled` (or `oneshot`) auto-starts it; any other value is
  a config error.
- `startup: "{{.ENABLE_SIDECAR:-enabled}}"` flips the default: on unless the
  var says otherwise.
- [Scheduled jobs](../scheduled/README.md) gate the same way: `schedule` is
  allowed alongside `startup: disabled`, so `"{{.ENABLE_BACKUP:-disabled}}"`
  ships a cron job off by default and `ENABLE_BACKUP=scheduled` arms it.

Hot reload (`SIGHUP` / `gopherd reload`) re-expands the template, picking up
env changes made between reloads.

## Docker

With gopherd as the image entrypoint, the gate is a plain `-e` at launch — the
runtime puts the variable into gopherd's environment, where the `startup`
template reads it at config load:

```dockerfile
FROM your-base-image
COPY gopherd /sbin/gopherd
COPY gopherd.yml /etc/gopherd/gopherd.yml
ENTRYPOINT ["/sbin/gopherd"]
```

```bash
docker run myimage                             # sidecar stays disabled
docker run -e ENABLE_SIDECAR=enabled myimage   # sidecar auto-starts
```

Same image, two roles — no config edit, no second Dockerfile. With Compose:

```yaml
services:
  app:
    image: myimage
    environment:
      ENABLE_SIDECAR: enabled
```

Kubernetes is the same idea via the container's `env:` list. Note that
`pass-env` is not needed for the gate itself; set it only if the *children*
should also see the variable.

## Expected behavior

- `ENABLE_SIDECAR` unset: `app` runs, `sidecar` reports `disabled`;
  `gopherd sidecar start` still starts it.
- `ENABLE_SIDECAR=enabled`: both services auto-start.

## Test

Run level. One test launches with the variable empty and asserts
`status sidecar` reports `disabled`, then proves "disabled ≠ removed" by
starting it over the control socket. The other launches with
`ENABLE_SIDECAR=enabled` and asserts both services reach `running`. SIGTERM
then yields a clean exit 0.

```bash
go test ./documentation/service-gating/ -v
```
