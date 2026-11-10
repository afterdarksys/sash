# sash language v3: text and data without awk

Status: draft contract. Order confirmed by Ryan 2026-11-10 (functions, row tools, csv, scoped overrides, legacy fixes; overrides: both). Nothing lands until Ryan signs off on the decisions marked **Decision**.

v2 gave sash values, control flow, and json, yaml, and toml. Text processing still needs awk, grep, cut, `perl -lne`, sort, and uniq in other shells. sash runs no host binaries, so the language does it itself. Every rule from v2 holds: verbs only as pipeline stages behind the grant check, values never parsed as source, everything in process bounded by the 8 MiB value cap and depth 64.

## POSIX

**Decision:** sash does not follow IEEE 1003.1 and will not grow a POSIX mode. It already drops word splitting, `eval`, `PATH`, and aliases for security, as fish drops POSIX for ergonomics and nushell for structure. A `.sh` script is not a sash script.

## 1. Expression functions

Calls inside `[ ]` only: `name(args)`. Pure, in process, no verb, no file, no clock, no random. A name not in this table is exit 2 `unknown function`. User `fn`s are commands, not expression functions: a command can call a verb and an expression cannot.

| Group | Functions |
| --- | --- |
| text | `len`, `upper`, `lower`, `trim`, `split(s, sep)`, `join(list, sep)`, `replace(s, old, new)`, `starts(s, p)`, `ends(s, p)`, `pad(s, n)`, `repeat(s, n)` |
| regex | `match(s, re)` (bool), `find(s, re)` (first match or null), `groups(s, re)` (list of capture groups), `sub(s, re, repl)` |
| convert | `int`, `float`, `str`, `bool`, `type` |
| list | `len`, `sum`, `min`, `max`, `avg`, `sort`, `uniq`, `reverse`, `first(l, n)`, `last(l, n)`, `range(a, b)` |
| map | `keys`, `values`, `has(m, k)`, `get(m, k, default)`, `merge(a, b)` |

Regex is Go RE2: linear time, no backreferences, so no catastrophic backtracking. A pattern over 1 KiB is exit 2. Every result is charged against the v2 budget before it is built.

## 2. Row tools

Filters on `|>`, in process, no key. `$it` is the current row inside a per-row expression.

| Form | Replaces |
| --- | --- |
| `sort by col [desc] [, col...]` | `sort -k` |
| `uniq [col]` | `uniq`, `sort -u` |
| `group by col` then `count`, `sum col`, `avg col`, `min col`, `max col` | `sort \| uniq -c`, awk sums |
| `first n`, `last n`, `skip n` | `head`, `tail` |
| `filter [ expr ]` | `grep`, `awk '/x/'`, `where` with logic |
| `each [ expr ]` | `awk '{print ...}'`, `perl -lne` |
| `parse "{user}:{_}:{uid}"` | `cut -d`, `awk -F` |
| `parse re "(?P<user>\w+)"` | `sed -n 's/.../p'`, `grep -o` |
| `lines` | text to rows with a `line` column (as `rows` with no names) |
| `sub col re with repl` | `sed s///` on one column |

`each` returning a map adds or replaces columns; returning a scalar gives one column `value`. `filter` and `each` stop at `loopmax` rows like a loop. `|>` keeps the v2 typing: these take rows.

Example, the classic `ps aux | awk '{print $1}' | sort | uniq -c | sort -rn`:

```
ps |> group by user |> count |> sort by count desc
```

## 3. CSV and TSV

`from csv`, `from tsv`, `to csv`, `to tsv`, using Go `encoding/csv`. The first line is the header. `from csv --noheader` names columns `c1`, `c2`... Input caps at 8 MiB and is charged as it reads. A field starting with `=`, `+`, `-`, or `@` is written by `to csv` with a leading `'` so a spreadsheet does not run it (CSV injection).

## 4. Scoped overrides: `with`

```
with set -e, loopmax 1000 { ... }
with x = 5, prefix /var/log { ... }
```

Options, names, and the prefix take the given values inside the block and are restored on every way out: normal end, a failure, `break`, `return`, `exit`, and a cap. A read-only name cannot be overridden. The prefix must pass the same `file.read` stat as `cd`. Nothing in `with` touches the grant.

### Environment for agent verbs (designed, not built)

**Decision:** sash starts no process, so there is no environment to set on the shell side. A verb the agent runs as a helper could take an environment. That is an agent change, and it needs its own systemapi contract before any code:

- The verb frame gains `env`, a map. The grant names which keys each verb may set (`env: {"journal": ["LANG"]}`), and the default is none.
- `PATH`, `LD_*`, `DYLD_*`, `IFS`, and anything matching the existing child env drops (`SYSTEMAPI*`, `*API_KEY*`, `ADSTELCO*`) are refused on both sides.
- Keys match `^[A-Z][A-Z0-9_]{0,63}$`; a value is at most 256 bytes with no NUL or newline.
- The audit row records the key names, never the values.
- sash syntax would be `with env LANG=C { journal }`. Until the agent contract lands, `with env` is exit 2 `env not in this series`.

## 5. Legacy fixes

- `$a$b` and `$a-$b` become one word, as `"$a$b"` already is. A v1 script that relied on two words breaks; that reliance was a bug.
- In `where` and `filter`, a bare `>` after the column is an operator, not a redirect. Elsewhere `>` stays a redirect.

## Threats

- A function or row tool calling a verb: there is no path from `[ ]` or a row tool to the parent; a test asserts no parent call for every function and tool.
- ReDoS: RE2 is linear; patterns are capped at 1 KiB.
- Memory: every function result, sort, group, and csv read is charged against the 8 MiB budget before it allocates.
- CSV injection into a spreadsheet: `to csv` prefixes formula characters.
- `with` leaving a weakened option behind: restore runs on every exit path; a test covers each one.
- Environment smuggling: designed with a per-verb allowlist in the signed grant, a denylist on both sides, and no values in the audit; not built here.

## Order

1. Expression functions.
2. Row tools: `sort`, `uniq`, `group` and the aggregates, `first`, `last`, `skip`, `filter`, `each`, `lines`.
3. `parse` and `sub`.
4. `from` and `to` csv and tsv.
5. `with` (no `env`).
6. Legacy fixes.

Each step lands with negative tests and a security review before the next.
