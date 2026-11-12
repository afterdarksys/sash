# sash needs: a script's grant manifest

Status: drafted 2026-11-12, build approved by Ryan ("lets do it"). CLI only; no session, no parent.

`why` explains one line against the grant in hand. `needs` answers the question before there is a grant: what would this whole script ask for? Its output is the least-privilege policy for the script, so a denial like `cd /tmp` → `denied file.read` shows up at the desk, not on the host.

This is possible because sash has no `eval`, no word splitting, no PATH lookup, and values are never parsed as source.

**Rule: a command name is a literal.** A run refuses any stage whose command word (argv[0]) is not one plain literal on the line: `let c = unit` then `$c restart nginx`, a fn parameter, a loop name, or a `[ ]` used as the command. The refusal is `command_from_value`, exit 2, checked in `prepare` before expansion, `classify`, or any parent call, so it covers every path that runs a stage (pipelines, `let`/`declare`/`with` pipes, conditions, loop sources, fn calls, plan mode, `retry`, `timeout`), and `why` and the prompt preview say the same. With that rule, every verb a script can reach is a literal stage in its parse tree. A subcommand or argument may still come from a value: `unit $op nginx` runs `unit status` or `unit restart` as `$op` says, so `needs` reports every key it could be, each with note `dynamic` and no policy flag. The result is incomplete (exit 1): the manifest lists every key but cannot say which one the run will use.

## 1. Command

```
sash --needs [--policy] (-c script | file | stdin)
```

- Reads the script the way `sash file` does. Parses it. Runs nothing, calls no parent, opens no file other than the script itself. Works with no `--session` and ignores any grant.
- Every branch counts: both arms of `if`, loop bodies (once), `try` and `catch`, `with`, `retry`, `timeout`, and the bodies of every `fn`, called or not.
- `--needs` cannot be combined with `--session` or `--plan` (exit 2, usage).
- Flags come first and the source is last: exactly `-c script`, one file, or stdin. Any argument after the source, a source that starts with `-`, or `--needs`/`--policy` anywhere but the leading flags is a usage error (exit 2) and nothing runs, so `sash -c 'echo RAN' --needs` does not run the script. `--policy` without `--needs` is a usage error.
- History expansion (`!!`, `!$`; sash has no `!n`, `!*`, or word designators) refuses a reference to a line that declares a secret (`declare` with a flag containing `s`, in any spelling or quoting): exit 2, `history: a declare -s line is not history`, without the secret. This holds in a run, in `needs`, and for the session history the line editor keeps; the editor does not keep or load such a line, so Alt-. and search cannot recall it either.

## 2. Output

Default: one row per distinct need, sorted by key then target, as a `why`-style table.

| column | meaning |
| --- | --- |
| `key` | the verb key: `file.read`, `file.write`, `unit.status`, `unit.restart`, `package.query`, `package.install`, `sysctl.get`, `sysctl.set`, `config.read`, `ps`, `journal` |
| `target` | the literal path, unit, package, sysctl key, or config dir; `-` for `ps`; `$` when it comes from a value |
| `mutates` | `yes` for `file.write`, `unit.restart`, `package.install`, `sysctl.set` |
| `lines` | the script lines that need it, comma separated, capped at 10 then `…` |
| `note` | `dynamic` (target or subcommand from a value), `relative`, `not_grantable` (`journal`, or `unit status`/`package query` without exactly one name, which the agent refuses), `use: not followed`, `unpasteable` (a literal no flag can carry), `command_from_value` (key `-`, target `$`: a stage a run refuses), or empty |

Paths are cleaned and made absolute against a literal `prefix`/`pushd`/`cd` that dominates them statically: `needs` tracks every prefix a statement may run under and resolves a relative path only when exactly one directory is possible. A relative path with no single literal base gets `target` as written and note `relative`; so does any path after `popd`, after a loop, `retry`, or `try` whose body may move the prefix, inside a `fn` body, and after a call when a function or a `use` may move it. A relative glob is never resolved: a run lists globs against the starting prefix, which a script does not have. Globs report the directory before the first `*`, as `why` does.

`config read <path>` is two needs: `config.read` on the path and `file.read` on the path, because a run also checks it against the file_read roots. A relative `config read` path is never resolved (the agent requires the clean absolute path), and an unclean absolute one is `not_grantable`.

`--policy` prints instead the systemapi policy flags that would grant the literal needs, one per line, deduplicated, in the CLI's spelling:

| key | flag |
| --- | --- |
| `file.read` | `--file-read <dir>` |
| `file.write` | `--file-write <dir>` |
| `unit.status` | `--service-status <unit>` |
| `unit.restart` | `--service <unit>` |
| `package.query`, `package.install` | `--package <name>` |
| `sysctl.get`, `sysctl.set` | `--tweak-sysctl <key>` |
| `config.read` | `--config <path>` (the exact path read) |

`<dir>` is the path itself for directory verbs (`cd`, `ls`, `pushd`, `prefix`, globs) and the parent directory for file verbs (`cat`, `<`, `>`, `template`, `template_file` paths, `use`, and the `file.read` of `config read`). `ps` needs no flag. A value a shell would split is single-quoted. Every need a flag cannot express (dynamic, relative, `not_grantable`, `use`, `command_from_value`, `unpasteable`) is printed as a `# ` comment line saying what could not be expressed, so the output can be pasted after review but never silently under-grants.

A literal is `unpasteable`, never a flag, when it starts with `-` (a CLI would read `unit status "--file-write=/"` as `--service-status --file-write=/`) or has a character the output escapes. Its comment names the kind: path, unit, package, sysctl key, or config dir.

## 3. Exit status

- 0: complete. Every need is literal.
- 1: incomplete. At least one need is `dynamic`, `relative`, `not_grantable`, `unpasteable`, or behind a `use` that was not followed, or a stage is `command_from_value`. The manifest is a lower bound. A `command_from_value` stage is exit 1, not 2: a run refuses it only when it is reached, so the statements before it, and every branch or fn that never reaches it, still run and still need what they need.
- 2: usage or parse error. Nothing is printed on stdout.

## 4. Not in scope (v1)

- Following `use` files. A `use` line is a `file.read` need on that path, noted `use: not followed`.
- Diffing against a live grant (`why` covers a session). A later `--against <policy.json>` can add a `granted` column.
- Inferring values: `$x` is never resolved, even when `x` was bound to a literal. Simple and honest beats clever and wrong.
- A `needs` builtin inside a session.

## Threats

- **Under-reporting is the dangerous failure**: a manifest that omits a need makes the operator grant too little (annoying), but a manifest believed complete when it is not leads them to trust a script they should read. So every need `needs` cannot see makes the result incomplete (exit 1) and says why. A verb key added to `classify` later must also be added to `needs`; a test walks the builtin table and fails on a key `needs` does not map.
- **The manifest is advisory, never an authorization.** Nothing reads it to grant; the agent and the grant check still decide every verb at run time. `--policy` output is for a human to review and paste.
- **A command name from a value** (`command_from_value`): before this rule a run expanded argv and then classified it, so `let c = "unit"` then `$c restart nginx`, a fn parameter, or a loop name picked the verb at run time, out of sight of the reader, `why`, and `needs`. A run now refuses such a stage before classify and before any parent call (exit 2), and `needs` reports it as key `-`, note `command_from_value`.
- **Secrets**: `needs` prints literal targets only. A path or unit built from a value prints `$`, so a `declare -s` value never reaches the output. A literal assigned with `declare -s` is never printed, and history cannot copy it onto a later line (section 1).
- **Hostile scripts**: parse caps (size, depth 64) apply as for a run. `needs` does no I/O beyond reading the script, so a script cannot make it touch the filesystem or network. Output is bounded by the number of distinct needs times the line cap.
- **Terminal injection**: every printed cell, in the table and in `--policy`, replaces tabs and escapes control characters (below 0x20, 0x7f, C1 0x80-0x9f), format characters (`unicode.Cf`: bidi overrides such as U+202E, zero-width characters), U+2028 and U+2029, anything else `unicode.IsPrint` rejects, backslashes, and bytes that are not UTF-8, so a crafted literal cannot rewrite or disguise the operator's terminal. `why` prints its table through the same escaping.
- **Flag injection**: a target that starts with `-` is never printed as a flag value (section 2).
