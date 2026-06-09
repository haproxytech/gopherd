# Environment passthrough

By default gopherd does **not** forward its own OS environment to children — this keeps operator secrets out of service processes. Set `pass-env: true` to opt in.

## Config

```yaml
# Forward gopherd's own environment to child services.
pass-env: true
processes:
  - name: printer
    command: /bin/sh
    args: ["-c", "test -n \"$DEMO_TOKEN\" && sleep 300 || exit 7"]
    on-failure: shutdown
```

- `pass-env: true` — top-level default applied to every service; gopherd's OS env is forwarded to children.
- It can also be set per-process; a per-process value overrides the global default.
- `printer` checks `DEMO_TOKEN`: present means it runs, absent means it exits 7.

## Expected behavior

- gopherd is launched with `DEMO_TOKEN` in its environment.
- With `pass-env: true`, the child sees `DEMO_TOKEN` and stays running.
- With the default `pass-env: false`, the child would not see it and exit 7.

## Test

The test sets `DEMO_TOKEN` before launch, then asserts `status printer` reports `running` — proving the child inherited the variable. SIGTERM then exits 0.

```bash
go test ./documentation/environment-passthrough/ -v
```
