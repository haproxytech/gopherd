# init-stop-signal

The top-level `init-stop-signal` list defines which signals trigger gopherd's
own graceful shutdown (stop all services, then exit). Setting it **replaces**
the default `[SIGTERM, SIGINT]` entirely.

## Config

```yaml
init-stop-signal: [SIGUSR2]
processes:
  - name: app
    command: /usr/local/bin/app
    args: ["300"]
    on-failure: shutdown
```

- `init-stop-signal: [SIGUSR2]` — SIGUSR2 is now the only graceful-shutdown
  trigger. SIGTERM and SIGINT are no longer in the set.
- SIGKILL and SIGSTOP are rejected at load (they cannot be caught).

## Expected behavior

- SIGUSR2 (which gopherd normally ignores) now triggers graceful shutdown:
  services are stopped cleanly and gopherd exits 0.

## A caveat on the dropped defaults

Because the override removes SIGTERM/SIGINT from the set, gopherd no longer
installs a handler for them. Inside gopherd's own process they fall back to the
OS default disposition (terminate), so SIGTERM will still kill the process —
but abruptly, **not** via the graceful shutdown path (it does not stop services
cleanly first). In other words, the override changes which signal gives a
*clean* shutdown; it does not make gopherd immune to a default-terminate signal.
If you want SIGTERM to do nothing at all, do not rely on omitting it here.

## Test

The test substitutes `/usr/bin/sleep` for the placeholder binary, asserts
`status app` is `running`, sends SIGUSR2, and asserts gopherd exits 0 within
the timeout — proving SIGUSR2 (normally a no-op) now drives graceful shutdown.
Verified stable across repeated runs.

The SIGTERM caveat above is not asserted as a unit test because the abrupt
default-terminate behavior depends on process disposition rather than gopherd
logic; it is described in prose instead.

```bash
go test ./documentation/init-stop-signal/ -v
```
