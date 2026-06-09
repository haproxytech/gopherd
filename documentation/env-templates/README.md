# Environment-variable templates

gopherd expands `{{.VAR}}` and `{{.VAR:-default}}` references inside `args`,
`environment`, and `startup`. Values come from the merged environment map
(OS env when `pass-env: true`, plus `dotenv` and per-process `environment`).

## Config

```yaml
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "test \"$GREETING\" = hello-world && sleep 300 || exit 9"]
    environment:
      GREETING: "{{.WORD:-hello}}-world"
    pass-env: true
    on-failure: shutdown
```

- `{{.WORD:-hello}}` — looks up `WORD`; if unset or empty, expands to the
  literal default `hello`.
- `GREETING` therefore becomes `hello-world` when `WORD` is unset.
- `pass-env: true` — forwards gopherd's OS env to the child, so an externally
  set `WORD` can override the default.
- `on-failure: shutdown` — gopherd exits if the shell test fails (exit 9).

Expansion is single-pass: an `environment` value cannot reference another
`environment` key. A bare `{{.VAR}}` (no `:-default`) expands to `""` and logs
a warning when the variable is unset.

## Expected behavior

- With `WORD` unset: `GREETING=hello-world`, the test passes, `app` keeps
  running.
- With `WORD=custom`: `GREETING=custom-world`, the test fails, `app` exits 9,
  and `on-failure: shutdown` takes gopherd down.

## Test

Run level. `WORD` is intentionally left unset, so the default `hello` applies
and `status app` reports `running` (proving the default expanded). SIGTERM then
yields a clean exit 0.

The override case (`WORD=custom`) is not asserted here: the child exits during
startup before the control socket is ready, which the harness treats as a
startup failure. The behavior is described above.

```bash
go test ./documentation/env-templates/ -v
```
