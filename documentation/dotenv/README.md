# dotenv: env file loading

`dotenv:` loads a `KEY=value` file into two places at once: the `{{.VAR}}` /
`{{.VAR:-default}}` template context (for `args`, `environment`, `startup`)
and the child's environment. The file is re-read at every service start, so a
restart picks up rotated values without a daemon reload.

## Config

```yaml
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "test {{.PORT:-8080}} = 9090 && test \"$API_TOKEN\" = s3cr3t && sleep 300 || exit 9"]
    dotenv: .env
    on-failure: shutdown
```

With the [`.env`](.env) file next to it containing:

```ini
PORT=9090
API_TOKEN=s3cr3t
```

A relative `dotenv:` path resolves against gopherd's working directory —
handy for local runs; prefer an absolute path in containers.

- `{{.PORT:-8080}}` expands from the dotenv value (`9090`); with `PORT`
  absent it would fall back to `8080`.
- `API_TOKEN` lands in the child environment directly — no template needed.
- Precedence (highest last): OS env (only with `pass-env: true`) < dotenv <
  per-process `environment:`. `remove-env` strips keys after the merge.
- Format: one `KEY=value` per line, `#` comments and blank lines ignored,
  optional single/double quotes (double quotes process `\n`, `\t`, `\"`).

## Safety checks

The file must be a regular file, not world-writable, and owned by root or
gopherd's own uid — otherwise the service refuses to start. Symlinks (the
file itself or a parent directory) are refused by default; set
`dotenv-follow: true` to permit them, confined to the file's directory.
That is required for K8s secret mounts (`key -> ..data/key`) and paths under
system symlinks like `/var/run -> /run` — same rules as the `{{file}}`
template's `follow` modifier (see [file-inclusion](../file-inclusion/)).

## Expected behavior

- Both assertions in the config pass, so `app` reaches `running`: the
  template expanded from the dotenv file and the child env got the token.
- Pointing `dotenv:` at a symlink fails the start with a clear error unless
  `dotenv-follow: true` is set.

## Test

Run level. One test runs the example against the checked-in `.env` and
asserts the service runs (both template and env checks passed). The other points
`dotenv:` at a symlink and asserts the start is refused, then succeeds with
`dotenv-follow: true`. SIGTERM yields a clean exit 0.

```bash
go test ./documentation/dotenv/ -v
```
