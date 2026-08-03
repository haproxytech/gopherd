# All options

A single reference config showing **every available option**, with defaults
and allowed values in comments.

**This is not a runnable config.** Commands, paths, and hosts are
illustrative placeholders (`/usr/local/bin/myapp`, `logs.example.com`), and
many options only make sense together with real workloads. Use it as a
lookup reference and copy the parts you need; the per-feature folders next
to this one show each option actually running.

## What the test proves

Unlike the other examples, this one is not executed — it is *validated*:

- **Load** — `example.yml` must pass the real config loader (`yml.Load`).
  Option renames, stricter validation, or syntax changes fail CI.
- **Completeness** — every option key the parser reads (extracted from the
  `.Get("...")` calls in `internal/yml/config.go`) must appear in
  `example.yml`, commented-out entries included. Adding a config option
  without documenting it here breaks CI.

```bash
go test ./documentation/all-options/ -v
```
