# TCP health check

Probe a service by opening a TCP connection to its listening port. The check passes when the connection succeeds.

## Config

```yaml
# TCP health check: probe a service by opening a TCP connection to its port.
processes:
  - name: svc
    command: /bin/sh
    args: ["-c", "exec nc -lk 127.0.0.1 PORT"]
    on-failure: shutdown

checks:
  svc-tcp:
    tcp:
      host: 127.0.0.1
      port: PORT
    period: 300ms
    threshold: 1
```

- `tcp.host` and `tcp.port` are dialed every `period`; a successful connect is healthy.
- `threshold` is the number of consecutive failures before the check is marked unhealthy.
- In a real config, `command` is your service binary and `port` is the port it listens on. Here the service is an `nc` listener so the example is self-contained.

## Expected behavior

- gopherd starts `svc` (an `nc` TCP listener) and the `svc-tcp` check.
- Once the listener binds, the probe connects and the check reports `healthy`:

  ```
  checks:
    svc-tcp              healthy  failures=0
  ```

- If the port is closed (service down or not yet bound), connects fail and the check trends `unhealthy` once `threshold` is reached.

## Test

Run level. `PORT` is substituted with an OS-assigned free port (e.g. 8080). The service is a real `nc -lk` listener; the test waits for the TCP probe to connect and asserts the `svc-tcp` line is `healthy`.

`nc` here is OpenBSD netcat (Debian); `-lk` listens and keeps accepting connections.

```bash
go test ./documentation/health-check-tcp/ -v
```
