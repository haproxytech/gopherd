# remove-env: strip keys from a child's environment

`remove-env` deletes keys from the final child environment, regardless of
where they came from — OS env (`pass-env: true`), `dotenv`, or `environment:`.
Typical use: several services share one dotenv, but only some of them should
see the secret in it.

## Config

```yaml
processes:
  - name: worker
    command: /bin/sh
    args: ["-c", "test \"$API_TOKEN\" = s3cr3t && test \"$PORT\" = 9090 && sleep 300 || exit 9"]
    dotenv: .env
    on-failure: shutdown

  - name: metrics
    command: /bin/sh
    args: ["-c", "test -z \"$API_TOKEN\" && test \"$PORT\" = 9090 && sleep 300 || exit 9"]
    dotenv: .env
    remove-env: [API_TOKEN]
    on-failure: shutdown
```

With the shared [`.env`](.env):

```ini
PORT=9090
API_TOKEN=s3cr3t
```

- Removal happens after the merge, so it wins over every source — including
  an explicit `environment:` entry and the `export-socket` injection
  (`remove-env: [GOPHERD_SOCKET]` strips that too).
- The remaining dotenv keys are untouched: `metrics` still gets `PORT`.

## Expected behavior

Both services reach `running`: `worker` sees the token, `metrics` proves it
does **not** (and still sees `PORT`) — otherwise it would exit 9 and shut
the container down.

## Test

Run level. The test runs the example against the checked-in `.env`; both
services reaching `running` proves the strip and the selective sharing.
SIGTERM then yields a clean exit 0.

```bash
go test ./documentation/remove-env/ -v
```
