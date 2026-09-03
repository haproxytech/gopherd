# restart-with-dependents

`requires:` couples a dependent to its dependency's *failure*. A manual
`gopherd restart db` is not a failure, so by default the requirers keep running
against a briefly absent `db`. `restart-with-dependents: true` on `db` makes
the control-socket restart bounce them too, in order:

1. running services that transitively `requires: [db]` stop, last-started first
2. `db` stops and starts again through its readiness gates (`ready-check`,
   `sd-notify`)
3. the stopped requirers start again in start order, through their own gates

Requirers that were already stopped are left alone. Automatic restarts
(`on-failure: restart`, `on-check-failure`) keep plain `requires` semantics
and do not cascade. A step that fails to start aborts the rest and, like a
failed automatic restart, shuts gopherd down with exit code 1.

Combine with `--wait` to block until the whole sequence is done:

```bash
gopherd restart db --wait --timeout 30s
# db: restarted (pid 123); dependents restarted: web
```

Without `--wait` the sequence runs in the background and the reply names what
will be bounced: `db: restart scheduled (with dependents: web)`.

## Config

```yaml
processes:
  - name: db
    command: /usr/local/bin/db
    args: ["300"]
    restart-with-dependents: true
    on-failure: ignore

  - name: web
    command: /usr/local/bin/web
    args: ["300"]
    requires: [db]
    on-failure: ignore
```

## Expected behavior

- `restart db --wait` returns after both `db` and `web` run with new pids.
- `web` exited before `db` was restarted and came back after it.
- `restart web` (no flag on `web`) restarts only `web`.

## Test

Run level. The test substitutes `sleep` for both commands, records the pids,
runs `restart db --wait`, and asserts the reply names `web` and both pids
changed. It then runs `restart web --wait` and asserts `db` kept its pid.
SIGTERM yields a clean exit 0.

The harness substitutes the `{{SOCKET}}` token in `control.socket` with a
temporary socket path.

```bash
go test ./documentation/restart-with-dependents/ -v
```
