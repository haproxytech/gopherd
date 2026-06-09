# File-inclusion templates

gopherd expands `{{file "/path"}}` by reading the named file's contents at
expansion time. This is the idiomatic way to consume Docker/Kubernetes secret
mounts without an external secret-fetch step.

## Config

```yaml
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "test \"$TOKEN\" = s3cr3t && sleep 300 || exit 9"]
    environment:
      TOKEN: '{{file "SECRETFILE" trim}}'
    on-failure: shutdown
```

- `{{file "/path"}}` — replaced with the file's contents. The path must be
  absolute.
- ` trim` (space, no pipe) — right-trims trailing whitespace and newlines, the
  trailing `\n` that most secret mounts include.
- `:-default` — optional fallback used only when the file does not exist
  (e.g. `{{file "/etc/license.key":-no-license}}`). A present-but-unreadable
  file is always a hard error.

The full grammar is `{{file "/path"}}`, `{{file "/path" trim}}`,
`{{file "/path":-default}}`, or `{{file "/path" trim:-default}}`. File contents
are capped at 1 MiB; NUL bytes and non-regular files (FIFO, device) are
rejected.

`startup` is expanded once at config load; `args` and `environment` are
re-expanded on every service start, so a rotated secret is picked up on
restart.

## Expected behavior

- The secret file contains `s3cr3t\n`.
- `trim` removes the newline, so `TOKEN` becomes exactly `s3cr3t`.
- The shell test passes and `app` keeps running.

## Test

Run level. The test writes `s3cr3t\n` to a temp file, substitutes the
`SECRETFILE` token with that path via `RunConfig`, and asserts `status app`
reports `running` — which proves the file was read and trimmed correctly (an
untrimmed value would be `s3cr3t\n`, fail the test, and exit 9). SIGTERM then
yields a clean exit 0.

```bash
go test ./documentation/file-inclusion/ -v
```
