# Troubleshooting

When pvmt exits with an error, it prints two things:

1. The error itself (e.g., `config file not found`).
2. A one-line **hint** suggesting a fix.

This page expands the hints into longer guidance. Each section's anchor matches the short label pvmt prints when it has one (e.g., `pvmt config show` references `#config-not-found`).

## `#config-not-found`

**Hint:** *create a pvmt.toml in your project root, or cd into a directory that contains one.*

pvmt looks for `pvmt.toml` by walking from the current working directory up toward the filesystem root (like `git` finds `.git`). If nothing matches before `/`, you get this error.

Fixes:

- Run pvmt from inside a project that has a `pvmt.toml`.
- Create one in the directory you're working from. The minimum config:

  ```toml
  [[cities]]
  name = "Oakland"
  ```

  See [configuration.md](configuration.md) for the full schema.

## `#no-cities`

**Hint:** *add a [[cities]] section to pvmt.toml.*

The config parsed cleanly but contains no `[[cities]]` entries. Every pvmt command needs at least one city.

Fix — add one to `pvmt.toml`:

```toml
[[cities]]
name = "Oakland"
```

Optional fields (bbox, overpass URL, ArcGIS source) are documented in [configuration.md](configuration.md). When you omit them, pvmt resolves a bounding box from OSM Nominatim on first ingest.

## `#invalid-config`

**Hint:** *(no hint — the validation error names the field.)*

The config file parsed as TOML but failed shape validation (negative `hex_edge_m`, unknown `display.units`, etc.). pvmt returns exit code 2 (usage error) so scripts can distinguish bad input from operational failures.

Read the error message: it names the offending field and what's wrong. Cross-check against [configuration.md](configuration.md) for valid ranges.

## `#permission-denied`

**Hint:** *check filesystem permissions on `<path>`.*

pvmt couldn't read, create, or write inside one of its runtime directories. The path in the hint tells you which one:

| Directory | Default location | Used for |
|-----------|------------------|----------|
| Config | The directory holding `pvmt.toml` | reading the config file |
| Cache | `$XDG_CACHE_HOME/pvmt` (or `~/.cache/pvmt`) | HTTP cache |
| Data | `$XDG_DATA_HOME/pvmt` (or `~/.local/share/pvmt`) | the SQLite database |

Common causes:

- The directory is owned by root because an earlier `sudo pvmt` invocation created it. Fix: `chown -R "$USER" <path>`.
- The filesystem is mounted read-only (overlayfs, container snapshot). Fix: pick a writable XDG override via `XDG_CACHE_HOME` / `XDG_DATA_HOME`.
- SELinux / AppArmor denies writes to your home directory's hidden dirs. Check audit logs and adjust the policy.

If reseating permissions doesn't help, run `pvmt status` to print every resolved path — that often surfaces a typo or a stray override env var.
