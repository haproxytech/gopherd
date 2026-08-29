# Service conditions

`condition-file-exists` and `condition-file-missing` gate a service on the
state of a file: the first runs the process only if the path exists, the
second only if it doesn't. Setting both means both must hold. The classic use
is a preparation oneshot that must run only when its work is not done yet —
no `sh -c 'if [ ! -e ... ]'` wrapper needed.

```yaml
condition-file-exists: /etc/app/enable    # run only if present
condition-file-missing: /etc/app/done     # run only if absent
```

Rules and semantics:

- Paths must be absolute; a relative path is rejected at load.
- The condition is re-evaluated at **every start attempt**: initial startup,
  `on-failure`/`on-success` restarts, each scheduled cron tick, and manual
  `start` via the control socket. A restart loop therefore stops on its own
  once the watched file state changes.
- An unmet condition **skips** the start: it is logged with the reason, shown
  by `status <name>` as `skipped (...)`, and counts as success — services
  with `after:`/`requires:` on it start normally, and no `on-success`/
  `on-failure` action fires (nothing ran).
- Symlinks are followed (`os.Stat`), so a k8s configmap/secret key resolved
  through `..data/` gives the right answer; a dangling symlink counts as
  missing. A `stat` error other than not-exist (e.g. permission denied)
  leaves the condition unmet with the error in the skip reason, so it never
  silently masquerades as a missing file.
- The check is advisory, not a lock: the file state can change between the
  probe and the exec.

## Config

```yaml
processes:
  - name: aux-cfg
    condition-file-missing: /etc/haproxy/haproxy-aux.cfg
    command: /bin/sh
    args:
      - -c
      - |
        touch /etc/haproxy/haproxy-aux.cfg
        chmod g+w /etc/haproxy/haproxy-aux.cfg
    startup: oneshot
  - name: app
    command: /usr/sbin/haproxy
    requires: [aux-cfg]
```

## Expected behavior

- First boot: the file is missing, `aux-cfg` runs and creates it, `app`
  starts after it completes.
- Later boots: the file exists, `aux-cfg` is skipped
  (`aux-cfg skipped (condition-file-missing: ... exists)`), and `app` still
  starts — skip satisfies `requires:`.
- `status aux-cfg` reports `skipped (condition-file-missing: ... exists)`;
  a manual `start aux-cfg` reports the same instead of running it.

## Test

Run level. Boots the daemon twice against the same directory and asserts the
run/skip split above. SIGTERM yields a clean exit 0.

```bash
go test ./documentation/service-conditions/ -v
```
