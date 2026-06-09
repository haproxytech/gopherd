# Oneshot

A oneshot service runs to completion during startup. Dependents wait until it exits successfully — ideal for migrations, schema setup, or fixture loading.

## Config

```yaml
# Run a oneshot migration to completion before the app starts.
processes:
  - name: migrate
    command: /usr/local/bin/migrate
    args: ["-c", "echo migrated; exit 0"]
    startup: oneshot
  - name: app
    command: /usr/local/bin/app
    args: ["300"]
    after: [migrate]
    on-failure: shutdown
```

- `startup: oneshot` — `migrate` runs once and must exit before dependents start.
- `app` declares `after: [migrate]`, so it only starts once `migrate` exits 0.
- A failing oneshot is fatal: gopherd exits rather than starting dependents.

## Expected behavior

- `migrate` runs, prints `migrated`, and exits 0.
- `app` then starts and reports `running`.
- `gopherd status migrate` reports `stopped` (it has already completed).

## Test

The placeholder `migrate` binary is replaced with `/bin/sh` so its args run as a script; `app` is replaced with `/usr/bin/sleep`. The test asserts `status app` is `running` (the gate opened) and that `migrate` is no longer `running`.

```bash
go test ./documentation/oneshot/ -v
```
