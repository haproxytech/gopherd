# Startup timeout

`startup-timeout` bounds how long a oneshot may run during startup. If it exceeds the limit, gopherd kills it. A failed oneshot is fatal, so a wedged init step cannot hang the container indefinitely.

## Config

```yaml
# Bound a oneshot's runtime so a wedged init step can't hang startup.
processes:
  - name: slow
    command: /usr/local/bin/slow
    args: ["30"]
    startup: oneshot
    startup-timeout: 1s
```

- `startup: oneshot` — runs once during startup; dependents wait for it.
- `startup-timeout: 1s` — if `slow` runs longer than 1s, gopherd kills it.
- A killed/failed oneshot is a fatal startup error: gopherd exits non-zero.

## Expected behavior

- `slow` is started but never finishes within 1s.
- At the timeout, gopherd kills it and logs `oneshot slow: timed out after 1s`.
- gopherd then exits non-zero, instead of blocking on the `sleep 30` horizon.

## Test

The placeholder is replaced with `/usr/bin/sleep` so the oneshot is `sleep 30` — guaranteed to exceed the 1s timeout. Because the timeout failure is fatal, the daemon shuts down rather than staying up; querying `status slow` is therefore not possible. The stable observable signal is the daemon's prompt non-zero exit, which the test asserts with `Wait`.

```bash
go test ./documentation/startup-timeout/ -v
```
