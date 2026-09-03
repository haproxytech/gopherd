# umask

`umask` sets the file-creation mask of a service before its command runs.
Children otherwise inherit gopherd's own mask, which is whatever the
container runtime handed PID 1 (usually `022`). Use it to tighten a service
that writes secrets (`077`), or to loosen one whose files a second uid must
edit (`002`, the classic shared-volume case), without a `sh -c 'umask ...;
exec ...'` wrapper.

```yaml
umask: "027"    # octal, quoted so YAML does not read it as a number
```

Rules and semantics:

- The value is octal, up to `0777`; `022`, `0022` and `22` mean the same.
  Anything else is rejected at load.
- Applied in the child between fork and exec, after the user and group
  switch, so it is exactly the mask the command starts with. Go has no
  per-child umask, so gopherd re-executes itself as a tiny shim that sets
  the mask and execs the real command; the pid does not change and nothing
  is visible from the outside.
- Health checks and other services are unaffected; gopherd's own mask never
  changes.
- Changing `umask` on reload restarts the service, like any spawn-time field.

## Config

```yaml
processes:
  - name: app
    command: /usr/local/bin/app
    umask: "027"
    startup: oneshot
    on-failure: shutdown

  - name: keeper
    command: /usr/local/bin/keeper
    args: ["300"]
    after: [app]
    on-failure: shutdown
```

## Expected behavior

- `app` starts with mask `0027`: files it creates are `0640` at most.
- Without the field it would inherit gopherd's mask.

## Test

Run level. The test substitutes a shell script that prints `umask` into a
file for `/usr/local/bin/app`, waits for `keeper` (ordered after the
oneshot), and asserts the file reads `0027`. SIGTERM yields a clean exit 0.

```bash
go test ./documentation/umask/ -v
```
