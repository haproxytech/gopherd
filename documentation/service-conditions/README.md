# Service conditions

`condition-file-exists` and `condition-file-missing` gate a service on
the state of a file: the first runs the process only if the path exists,
the second only if it doesn't. Setting both means both must hold.

`condition-env-equals` is different in kind: it decides whether the
service exists in this run of the config at all. One image serving several
roles (`ROLE=api`, `ROLE=worker`, ...) lists every service once and
tags each with the role it belongs to; in a container of another role the
service is dropped at load, as if the entry were not written. To switch a
service on or off by a variable's mere presence, and keep manual `start`
working, use the `startup` template in
[service-gating](../service-gating/) instead.

The classic file-condition use is a preparation oneshot that must run only
when its work is not done yet — no `sh -c 'if [ ! -e ... ]'` wrapper
needed.

```yaml
condition-file-exists: /etc/app/enable    # run only if present
condition-file-missing: /etc/app/done     # run only if absent
condition-env-equals:                     # every key must match (AND)
  ROLE: api                               # exists only when ROLE is "api"
  TIER: "1"                               # ... and TIER is "1" (values are strings)
```

## Rules and semantics

### File conditions

- Paths must be absolute; a relative path is rejected at load.
- The condition is re-evaluated at **every start attempt**: initial
  startup, `on-failure`/`on-success` restarts, each scheduled cron
  tick, and manual `start` via the control socket. A restart loop
  therefore stops on its own once the watched file state changes.
- Symlinks are followed (`os.Stat`), so a k8s configmap/secret key
  resolved through `..data/` gives the right answer; a dangling symlink
  counts as missing. A `stat` error other than not-exist (e.g.
  permission denied) leaves the condition unmet with the error in the
  skip reason, so it never silently masquerades as a missing file.
- The check is advisory, not a lock: the file state can change between
  the probe and the exec.

### Environment condition

- Resolved once per config load against the same environment snapshot
  the `{{.VAR}}` templates use, so the two can never disagree. A hot
  reload re-resolves it: a service can join or leave the config that way,
  and leaving stops it like a deleted service.
- Read from gopherd's own environment, independent of `pass-env`,
  `environment:` and `dotenv:`.
- Exact string comparison, no trimming, no case folding. All keys must
  match (AND). An unset variable compares as `""`.
- An unmet condition **excludes** the service: it is not listed by
  `status`, `status <name>` and `start <name>` report an unknown service,
  and every `after:`/`before:`/`requires:` edge pointing at it is dropped
  so the survivors order as if the entry were absent. The daemon logs
  `<name> excluded (condition-env-equals: KEY does not match "value")`
  once at load.
- The excluded entry is still validated, so a typo in a rarely deployed
  role fails the load everywhere.
- Checks are global: a health check only meaningful for an excluded
  service still runs. Point checks at services that exist in every role.
- Keys must be valid POSIX variable names and the value must be a
  mapping; anything else is rejected at load rather than opening the gate.
- The log line names the key and the expected value only, never the
  observed value, which may be a secret.

### Skip semantics

- An unmet condition **skips** the start: it is logged with the reason,
  shown by `status <name>` as `skipped (...)`, and counts as success —
  services with `after:`/`requires:` on it start normally, and no
  `on-success`/`on-failure` action fires (nothing ran).

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

- First boot: the file is missing, `aux-cfg` runs and creates it,
  `app` starts after it completes.
- Later boots: the file exists, `aux-cfg` is skipped
  (`aux-cfg skipped (condition-file-missing: ... exists)`), and `app`
  still starts — skip satisfies `requires:`.
- `status aux-cfg` reports `skipped (condition-file-missing: ... exists)`;
  a manual `start aux-cfg` reports the same instead of running it.

## Test

Run level. Boots the daemon twice against the same directory and asserts the
run/skip split above.

`TestServiceConditionsEnv` boots the same config twice. With
`ROLE=worker` the gated `app` is excluded: `status` does not list it,
`status app` and `start app` report an unknown service, the daemon log
carries the exclusion line, and a second service whose `after: [app]` edge
was dropped runs anyway. With `ROLE=api` the app starts and reaches
`running`.

SIGTERM yields a clean exit 0.

```bash
go test ./documentation/service-conditions/ -v
```
