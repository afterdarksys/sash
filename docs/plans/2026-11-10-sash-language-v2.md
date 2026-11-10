# sash language v2

Status: decisions confirmed by Ryan 2026-11-10: expressions inside `[ ]`, `for x in` only, pinned YAML and TOML modules, loop and verb caps kept.

Ryan chose on 2026-11-10 to grow the sash language rather than embed Starlark. Goal: scripting past fish and bash, with the rules that already hold. Every verb still goes through the grant check, the agent re-check, and the audit row. Values stay values. Nothing here runs a host binary, opens a path, or changes the grant.

## Values

v1 has strings, bytes, rows, and one row. v2 adds:

| Kind | Literal | Notes |
| --- | --- | --- |
| number | `42`, `-3`, `2.5` inside `[ ]` | int64 or float64. A bare word `42` stays a string outside `[ ]`. |
| bool | `true`, `false` inside `[ ]` | |
| list | `[1, "a", $x]` inside `[ ]` | |
| map | `{"port": 8080}` inside `[ ]` | Keys are strings. Order is insertion order, so output is stable. |
| null | `null` inside `[ ]` | From JSON, YAML, and TOML. |

Rows become a list of maps that remembers its column order. `|>`, `where`, `select`, `for` keep working on them unchanged.

Access: `$cfg.server.port`, `$list[0]`, `$m["a key"]`. A missing key is `null`. With `set -u` it is exit 2 `unset <name>`.

## Expressions

Expressions live inside `[ ]`, and only there. Outside `[ ]`, a line is still words and pipelines, so `<` and `>` stay redirects.

```
let n = [ $n + 1 ]
let ok = [ $port >= 1024 && $host != "" ]
let hosts = [ ["a", "b"] + $extra ]
if [ $n < 10 ] { ... }
```

Operators: `+ - * / %`, `== != < <= > >=`, `&& || !`, `in`, string `+` and `*`. No assignment inside an expression. Calling a verb inside `[ ]` is a type error. Verbs run only as pipeline stages, where the grant check and the audit are.

`if <pipeline>` keeps its v1 meaning: a row pipeline is true when it yields a row, a byte pipeline when its exit is 0. `if [ expr ]` is the expression form.

## Control flow

| Form | Meaning |
| --- | --- |
| `if c { } else if c { } else { }` | `else if` chains. |
| `for x in <src> { }` | Walks a list, a map's keys, rows, a pipeline, or a range `[ 1..10 ]`. This is the foreach. |
| `for k, v in $map { }` | Key and value. `for i, x in $list` is index and value. |
| `while c { }` | `c` is a pipeline or `[ expr ]`. |
| `do { } while c` | Runs the body once first. |
| `break`, `continue` | Innermost loop. |
| `return [value]` | From an `fn`. The value is the call's result. Outside an `fn` it is exit 2 `usage return`. |
| `exit [n]` | Ends the script, or the session at the prompt like `exit` today. `n` is 0–255. |
| `try { } catch e { }` | `e` is a map: `status`, `message`, `key`. A denial can be caught: it already ran nothing. Catching does not retry the verb. |

Decision: there is no separate `foreach` keyword. `for x in` is the foreach, and a second spelling would be a second way to say one thing. A C-style `for (;;)` is out: `while` and ranges cover it.

Loops are capped. One loop runs at most `loopmax` iterations, default 100000, settable up to 10000000 with `set loopmax`. Past the cap it is exit 2 `loop max`. A runaway loop of agent verbs is the case that matters. The cap is per loop, and nested loops multiply, so verbs also count against `verbmax` per line, default 10000, also settable within the same ceiling.

Functions: `fn name a b { }` as today, plus `return` values and default parameters `fn f a b=1 { }`. The depth cap stays eight.

## Data formats

Filters, in process, no key:

| Command | In | Out |
| --- | --- | --- |
| `from json` | bytes | value |
| `from yaml` | bytes | value. One document. A multi-document stream is a list. |
| `from toml` | bytes | map |
| `to json`, `to yaml`, `to toml` | value or rows | bytes |
| `json` | as v1 | as v1, kept as `to json` of a pipeline value |

`cat /etc/app/config.yaml | from yaml` reads through `file.read` like any `cat`. Anchors and aliases in YAML are expanded with a size cap: a document that expands past 8 MiB is exit 2 `yaml too large`, the same bound as one verb result. Tags other than the core schema are a type error. TOML dates become strings in RFC 3339.

Dependency decision: sash has no third-party module today. JSON uses `encoding/json`. YAML takes `go.yaml.in/yaml/v3` (the YAML org's maintained fork). TOML takes `github.com/BurntSushi/toml`. Both are pinned in `go.sum`, and each enters in its own commit with a note. The alternative is a hand-written subset parser, which is worse for a security tool.

## Settings: `set`, `shopt`, `declare`

`set` holds session options. None of them changes the grant, and none can turn off a check.

| Option | Default | Meaning |
| --- | --- | --- |
| `set -e` / `set errexit` | off | A failed stage ends the script. |
| `set -u` / `set nounset` | off | An unset name or missing key is exit 2. |
| `set pipefail` | on | A pipeline's status is its first failed stage. v1 already does this; it stays on. |
| `set -x` / `set xtrace` | off | Same as `trace on`. |
| `set noglob` | off | `*` stays a literal word. |
| `set loopmax N`, `set verbmax N` | 100000, 10000 | Bounded as above. |
| `set` | | Lists every option as rows. |

`shopt` is not a second store. `shopt` prints the same rows as `set`, and `shopt -s x` is `set x`, so bash muscle memory works and there is one table. `bind`, `syntax`, `trace`, and `prompt` stay as they are and also show up in `set`'s rows.

`declare`:

| Form | Meaning |
| --- | --- |
| `declare -r name = value` | Read-only. Assigning it again is exit 2 `readonly name`. |
| `declare -i`, `-a`, `-A` | Typed: number, list, map. A wrong type is exit 2. |
| `declare` | Every name, its type, and flags, as rows. Values are not printed (they may hold file contents). `declare -p name` prints one. |

There is no `-x` and no `export`. sash starts no process, so there is no environment to export into, and the child env drops stay as they are.

## `builtin`

`builtin` lists everything sash understands, as rows: `name`, `kind`, `key`, `desc`.

- `kind` is `keyword` (`if`, `for`, `fn`...), `builtin` (`set`, `declare`, `cd`, `why`...), `filter` (`where`, `from yaml`...), or `verb` (`ps`, `unit status`...).
- `key` is the entitlement, `-` for anything in process.
- `builtin <name>` is one row plus its usage.
- `builtin` is not bash's bypass-the-function: sash functions cannot shadow a closed-table name, so there is nothing to bypass.

This list and the Tab table come from one Go table, so they cannot disagree. A test fails if a name is parsed or dispatched and is missing from it.

## Out of scope

- `eval`, `source` of a value, and `export` stay absent. `source <path>` (load functions from a file) reads through `file.read` and parses the file as sash. It is in scope as `use <path>`, which only defines `fn`s and `declare -r` names and runs no top-level statement.
- Background jobs and `&` stay blocked (single-process reap rule).
- Aliases stay blocked.

## Threats

- A runaway loop flooding the agent with verbs: bounded by `loopmax` and `verbmax`, and the agent's own limits still apply.
- Parser bombs in YAML (alias expansion), deep nesting in JSON or TOML: a size cap and a depth cap of 64 for every `from` format; past either is exit 2.
- A value treated as code: expressions do not parse strings again, there is no `eval`, and `use` takes a path, never a value's text as source.
- `catch` hiding a denial from the audit: the agent audits the verb whether or not a script catches it, and a denial in preflight never ran.
- `set` weakening a check: no option touches the grant, the prefix roots, or the agent re-check. A test lists every option and asserts that.
- New dependencies: two pinned modules, each in its own commit, scanned before landing.

## Order

1. Values and `[ ]` expressions, with access paths. Lexer mode for `[ ]`.
2. Control flow: `else if`, `while`, `do`, `break`, `continue`, `return`, `exit`, `try`. Loop and verb caps.
3. `builtin`, with the single table shared with Tab.
4. `set`, `shopt`, `declare`.
5. `from` / `to` json. Then yaml (dependency commit first), then toml.
6. `use <path>`.
7. `journal` as rows (the agent reader) slots in after 3; it is its own contract.

Each commit builds and tests alone, with negative tests for every exit 2 above.
