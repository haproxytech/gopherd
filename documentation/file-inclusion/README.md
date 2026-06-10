# File-inclusion templates

gopherd expands `{{file "/path"}}` by reading the named file's contents at
expansion time. This is the idiomatic way to consume Docker/Kubernetes secret
mounts without an external secret-fetch step.

## Config

```yaml
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "test \"$TOKEN\" = s3cr3t && test \"$K8S_TOKEN\" = k8sv4lue && sleep 300 || exit 9"]
    environment:
      TOKEN: '{{file "SECRETFILE" trim}}'
      K8S_TOKEN: '{{file "K8SSECRETFILE" follow trim}}'
    on-failure: shutdown
```

- `{{file "/path"}}` — replaced with the file's contents. The path must be
  absolute.
- `trim` — right-trims trailing whitespace and newlines, the trailing `\n`
  that most secret mounts include.
- `follow` — permits symlinks for this reference. Kubernetes secret-volume
  keys are symlinks into `..data/`, so K8s mounts need it; without it gopherd
  refuses any symlink (the file or an ancestor directory). Note that K8s
  rotates secrets in place and gopherd does not watch for that: the value is
  a snapshot taken at service start, which suits short-lived jobs — a
  long-running service sees a rotated value only on its next restart.
- `:-default` — optional fallback used only when the file does not exist
  (e.g. `{{file "/etc/license.key":-no-license}}`). A present-but-unreadable
  file is always a hard error. A dangling symlink with `follow` counts as
  missing.

The full grammar is `{{file "/path"}}` with optional modifiers (`trim`,
`follow`, in any order) followed by an optional `:-default`. File contents
are capped at 1 MiB; NUL bytes and non-regular files (FIFO, device) are
rejected.

`startup` is expanded once at config load; `args` and `environment` are
re-expanded on every service start, so a rotated secret is picked up on
restart.

## Expected behavior

- The secret file contains `s3cr3t\n`; `trim` removes the newline, so
  `TOKEN` becomes exactly `s3cr3t`.
- The K8s-style secret is a symlink chain (`token -> ..data/token`,
  `..data -> ..<timestamp>/`); `follow` permits it and `K8S_TOKEN` becomes
  `k8sv4lue`.
- Both shell tests pass and `app` keeps running.

## Test

Run level. The test writes `s3cr3t\n` to a temp file and builds a K8s
secret-volume layout (key symlinked through `..data/`) for the second value,
substitutes the `SECRETFILE`/`K8SSECRETFILE` tokens via `RunConfig`, and
asserts `status app` reports `running` — which proves both files were read,
trimmed, and the symlink chain was followed (a failure exits 9; without
`follow` the config would not even load). SIGTERM then yields a clean exit 0.

```bash
go test ./documentation/file-inclusion/ -v
```
