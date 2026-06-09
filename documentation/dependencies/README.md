# Dependencies

`after:` controls startup ordering. A service listed in another's `after` is started first; gopherd computes the order with a topological sort.

## Config

```yaml
# Order startup with `after`: `first` runs before `second`.
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
```

- `second` declares `after: [first]`, so gopherd starts `first` first.
- Declaration order in the file does not matter — the dependency graph does.
- `after` orders startup only; use `requires` if a dependency failure should also fail the dependent.

## Expected behavior

- `first` starts and appends `first` to the log.
- `second` starts afterward and appends `second`.
- The log reads `first` then `second`.

## Test

`ORDERLOG` is replaced with a temp path via `RunConfig`. After startup the test reads the log and asserts the lines are `first` then `second`.

```bash
go test ./documentation/dependencies/ -v
```
