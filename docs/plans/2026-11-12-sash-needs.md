# sash needs: a script's grant manifest

Status: drafted 2026-11-12, build approved by Ryan ("lets do it"). CLI only; no session, no parent.

`why` explains one line against the grant in hand. `needs` answers the question before there is a grant: what would this whole script ask for? Its output is the least-privilege policy for the script, so a denial like `cd /tmp` → `denied file.read` shows up at the desk, not on the host.

This is possible because sash has no `eval`, no word splitting, no PATH lookup, and values are never parsed as source. Every verb a script can reach is a literal stage in its parse tree.

## 1. Command

```
sash --needs [--policy] (-c script | file | stdin)
```

- Reads the script the way `sash file` does. Parses it. Runs nothing, calls no parent, opens no file other than the script itself. Works with no `--session` and ignores any grant.
- Every branch counts: both arms of `if`, loop bodies (once), `try` and `catch`, `with`, `retry`, `timeout`, and the bodies of every `fn`, called or not.
- `--needs` cannot be combined with `--session` or `--plan` (exit 2, usage).

## 2. Output

Default: one row per distinct need, sorted by key then target, as a `why`-style table.

| column | meaning |
| --- | --- |
| `key` | the verb key: `file.read`, `file.write`, `unit.status`, `unit.restart`, `package.query`, `package.install`, `sysctl.get`, `sysctl.set`, `config.read`, `ps`, `journal` |
| `target` | the literal path, unit, package, sysctl key, or config dir; `-` for `ps`; `$` when it comes from a value |
| `mutates` | `yes` for `file.write`, `unit.restart`, `package.install`, `sysctl.set` |
| `lines` | the script lines that need it, comma separated, capped at 10 then `…` |
| `note` | `dynamic` (target from a value), `not_grantable` (`journal`), `use: not followed`, or empty |

Paths are cleaned and made absolute against a literal `prefix`/`pushd`/`cd` that dominates them statically. A relative path with no literal base gets `target` as written and note `relative`. Globs report the directory before the first `*`, as `why` does.

`--policy` prints instead the systemapi policy flags that would grant the literal needs, one per line, deduplicated, in the CLI's spelling:

| key | flag |
| --- | --- |
| `file.read` | `--file-read <dir>` |
| `file.write` | `--file-write <dir>` |
| `unit.status` | `--service-status <unit>` |
| `unit.restart` | `--service <unit>` |
| `package.query`, `package.install` | `--package <name>` |
| `sysctl.get`, `sysctl.set` | `--tweak-sysctl <key>` |
| `config.read` | `--config <dir>` |

`<dir>` is the path itself for directory verbs (`cd`, `ls`, `pushd`, `prefix`, globs) and the parent directory for file verbs (`cat`, `<`, `>`, `template_file` paths, `use`). `ps` needs no flag. `journal` and dynamic needs are printed as `# ` comment lines saying what could not be expressed, so the output can be pasted after review but never silently under-grants.

## 3. Exit status

- 0: complete. Every need is literal.
- 1: incomplete. At least one need is `dynamic`, `relative`, `not_grantable`, or behind a `use` that was not followed. The manifest is a lower bound.
- 2: usage or parse error. Nothing is printed on stdout.

## 4. Not in scope (v1)

- Following `use` files. A `use` line is a `file.read` need on that path, noted `use: not followed`.
- Diffing against a live grant (`why` covers a session). A later `--against <policy.json>` can add a `granted` column.
- Inferring values: `$x` is never resolved, even when `x` was bound to a literal. Simple and honest beats clever and wrong.
- A `needs` builtin inside a session.

## Threats

- **Under-reporting is the dangerous failure**: a manifest that omits a need makes the operator grant too little (annoying), but a manifest believed complete when it is not leads them to trust a script they should read. So every need `needs` cannot see makes the result incomplete (exit 1) and says why. A verb key added to `classify` later must also be added to `needs`; a test walks the builtin table and fails on a key `needs` does not map.
- **The manifest is advisory, never an authorization.** Nothing reads it to grant; the agent and the grant check still decide every verb at run time. `--policy` output is for a human to review and paste.
- **Secrets**: `needs` prints literal targets only. A path or unit built from a value prints `$`, so a `declare -s` value never reaches the output. A literal assigned with `declare -s` is never printed.
- **Hostile scripts**: parse caps (size, depth 64) apply as for a run. `needs` does no I/O beyond reading the script, so a script cannot make it touch the filesystem or network. Output is bounded by the number of distinct needs times the line cap.
- **Terminal injection**: targets are printed through the same escaping as `why` (tabs replaced; control characters escaped), so a crafted literal cannot rewrite the operator's terminal.
