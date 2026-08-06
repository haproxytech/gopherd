# Dependencies

`after:` and `before:` control startup ordering. A service listed in another's `after` is started first; `before` is the same edge written from the other side. gopherd computes the order with a topological sort.

## Config

```yaml
# Order startup with `after` and `before`: zeroth -> first -> second.
processes:
  - name: second
    command: /bin/sh
    args: ["-c", "echo second >> ORDERLOG && sleep 300"]
    after: [first]
    on-failure: shutdown
  - name: first
    command: /bin/sh
    args: ["-c", "echo first >> ORDERLOG && sleep 300"]
    on-failure: shutdown
  - name: zeroth
    command: /bin/sh
    args: ["-c", "echo zeroth >> ORDERLOG && sleep 300"]
    before: [first]
    on-failure: shutdown
```

- `second` declares `after: [first]`, so gopherd starts `first` first.
- `zeroth` declares `before: [first]` — the mirror form, useful when the
  inserted service is the one being added and the existing configs should
  stay untouched.
- Declaration order in the file does not matter — the dependency graph does.
- `after`/`before` order startup only; use `requires` if a dependency failure should also fail the dependent.

## Expected behavior

- `zeroth` starts first, then `first`, then `second`; the log reads
  `zeroth`, `first`, `second`.

## Test

`ORDERLOG` is replaced with a temp path via `RunConfig`. The test asserts gopherd's `started` lines appear in `zeroth`, `first`, `second` order. Note `after`/`before` order gopherd's start calls; the echoes themselves run in separate shells, so use a `ready-check` when the dependent needs the dependency's work completed.

```bash
go test ./documentation/dependencies/ -v
```
