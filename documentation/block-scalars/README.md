# Literal block scalars

Any string option accepts a YAML literal block scalar (`|`): the indented
lines below the indicator become one multi-line value, verbatim. Newlines
are preserved, indentation past the first content line is kept, and `#` is
content — never a comment. This makes inline shell scripts readable without
`\n` escapes or one-line `;` chains.

Chomping indicators control trailing newlines:

```yaml
script: |     # clip (default): value ends with exactly one newline
script: |-    # strip: no trailing newline
script: |+    # keep: all trailing blank lines are preserved
```

A block scalar works anywhere a string does — as a mapping value at any
nesting depth, or as a list item (e.g. one entry of `args`). Folded block
scalars (`>`) and indentation indicators (`|2`) are not supported and are
rejected at load time with a clear error.

## Config

```yaml
processes:
  - name: app
    command: /bin/sh
    args:
      - -c
      - |
        echo "line one" >> OUTFILE
        if true; then
          echo "line two" >> OUTFILE
        fi
        exec sleep 300
    on-failure: shutdown
```

## Expected behavior

- The block reaches `/bin/sh -c` as a single multi-line argument.
- Inner indentation (the `if` body) and the shell comment-free lines run
  exactly as written; the script's own `#` would be passed through, not
  stripped as a YAML comment.
- `exec sleep 300` keeps the service in the running state after the script
  body completes.

## Test

Run level. Launches the daemon and asserts both script lines executed in
order. SIGTERM yields a clean exit 0.

```bash
go test ./documentation/block-scalars/ -v
```
