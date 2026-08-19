# Block-style args

`args` (and every other list option: `after`, `before`, `requires`,
`remove-env`, `init-stop-signal`, `services`) accepts both YAML list styles:

```yaml
args: ["-c", "script", "app"]   # flow style
```

```yaml
args:                            # block style
  - -c
  - script
  - app
```

Block style keeps long argument lists readable. Quoting rules match flow
style: a quoted item may contain commas, colons, and `#` — an inline JSON
blob stays a single argument.

## Config

```yaml
processes:
  - name: app
    command: /bin/sh
    args:
      - -c
      - 'echo "$0" > ARGSFILE; exec sleep 300'
      - '{"theme": {"foreground": "#d0d0d0", "background": "#1c1c1c"}}'
    on-failure: shutdown
```

## Expected behavior

- Each block item becomes one argv entry, in order.
- The single-quoted JSON arrives at the process as one argument, unmodified.
- An unquoted item with `key: value` shape is still parsed as a mapping —
  quote it if it is meant as a literal argument.

## Test

Run level. Launches the daemon and asserts the JSON item reached the shell as
a single intact argument. SIGTERM yields a clean exit 0.

```bash
go test ./documentation/block-style-args/ -v
```
