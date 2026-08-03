# Log streaming: `logs` and `logs -f`

With `log-capture: true`, gopherd keeps a ring buffer of each service's
recent output and can stream new lines live over the control socket:

```bash
gopherd logs ticker        # recent history, then exit
gopherd logs ticker -f     # follow live output (Ctrl-C to stop)
```

## Config

```yaml
log-capture: true

processes:
  - name: ticker
    command: /bin/sh
    args: ["-c", "i=0; while true; do i=$((i+1)); echo tick $i; sleep 0.2; done"]
    on-failure: shutdown
```

- `log-capture: true` is required — with the default direct FD passthrough
  gopherd never sees the output, and `logs` returns an explicit error for
  that service.
- Without `-f` the command prints the ring-buffer history and exits; with
  `-f` it first prints the history, then streams every new line as the
  service writes it.
- Streaming connections have their own pool (separate from one-shot
  commands), so followers cannot starve `status`/`start`/`stop`.
- Lines carry the configured prefix (service name + timestamp by default).

## Expected behavior

- `gopherd logs ticker` returns lines like `[ticker] ... tick 42`.
- `gopherd logs ticker -f` keeps printing a new `tick N` every 200ms until
  interrupted.

## Test

Run level. The test fetches history over the control socket and asserts it
contains tick lines, then opens a raw follow stream and asserts multiple new
lines arrive live. SIGTERM then yields a clean exit 0.

```bash
go test ./documentation/log-streaming/ -v
```
