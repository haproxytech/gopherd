# sd-notify readiness gate

Hold dependents until a service signals readiness via the systemd sd_notify protocol. With `sd-notify: true`, gopherd waits for the service to write `READY=1` to `$NOTIFY_SOCKET` before starting dependents.

## Config

```yaml
# sd-notify gate: dependents wait until the service writes READY=1.
processes:
  # mydaemon sends READY=1 to $NOTIFY_SOCKET once initialized (sd_notify).
  - name: notifier
    command: /usr/local/bin/mydaemon
    sd-notify: true
    sd-notify-timeout: 10s
    on-failure: shutdown

  - name: app
    command: /usr/local/bin/app
    args: ["300"]
    after: [notifier]
    on-failure: shutdown
```

- `sd-notify: true` makes gopherd inject `$NOTIFY_SOCKET` into the service env and wait for `READY=1` before starting dependents.
- `sd-notify-timeout` bounds the wait; if `READY=1` never arrives, gopherd exits non-zero.
- `after: [notifier]` orders `app` after `notifier`; the sd-notify gate then holds `app` until `READY=1`.

## NOTIFY_SOCKET details

gopherd binds the notify socket in the Linux **abstract namespace**, so `$NOTIFY_SOCKET` is a `@`-prefixed name (e.g. `@gopherd-sd-notify-<pid>-notifier`), not a filesystem path. To send to it, a client must replace the leading `@` with a NUL byte and use a `SOCK_DGRAM` `AF_UNIX` socket — in Go:

```go
addr := os.Getenv("NOTIFY_SOCKET")
if strings.HasPrefix(addr, "@") {
    addr = "\x00" + addr[1:] // abstract namespace
}
conn, _ := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
conn.Write([]byte("READY=1"))
```

or in Python:

```python
import os, socket

addr = os.environ["NOTIFY_SOCKET"]
if addr.startswith("@"):
    addr = "\0" + addr[1:]  # abstract namespace
socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM).sendto(b"READY=1", addr)
```

Any sd_notify library (systemd's own, `go-systemd`, Python's `sdnotify`, ...) does the same. OpenBSD netcat (`nc -uU`) cannot address abstract-namespace datagram sockets, so a plain `nc` one-liner does **not** work here.

## Expected behavior

- gopherd starts `notifier` and waits.
- `notifier` sends `READY=1` to `$NOTIFY_SOCKET`; gopherd logs `READY=1` and starts `app`.
- `app` reaches `running` only after the gate opens. If `READY=1` never arrives, `sd-notify-timeout` fires and gopherd exits non-zero.

## Test

Run level. The notifier placeholder is substituted with a small Go stand-in built by the test harness (`internal/doctest/cmd/sdnotifyready`) that resolves the abstract `$NOTIFY_SOCKET`, sends `READY=1`, and stays alive — no external interpreter needed. The test substitutes `sleep` for `app` and asserts `app` reaches `running` — which can only happen after `READY=1` is received, proving the gate opened.

The timeout path (a service that never sends `READY=1`) is asserted in the root e2e suite (`TestE2ESDNotifyTimeout`).

```bash
go test ./documentation/sd-notify/ -v
```
