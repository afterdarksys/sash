# sash: the closed table

Read this to check that a name exists, which entitlement key it needs, and whether it mutates. Every command and keyword form is in one Go table (`internal/lang/builtin.go`); the `builtin` command prints it, and a test fails if this file and that table disagree. The one statement outside it is `wasm` (`features.md`), which the release build refuses. A command name that is not here, and is not a `fn` you defined, is exit 127 `unknown <name>`. There is no host binary behind it.

Columns:

- **kind**: `verb` (the agent fulfills it), `filter` (in process), `builtin` (in process, or a prefix move), `keyword` (syntax), `session` (the interactive editor only).
- **key**: the entitlement the grant must hold, `-` for none. The agent checks the key again, and the target against the host's channel or file policy.
- **mutates**: `yes` when the key is one plan mode stops and confirm asks about (`unit.restart`, `package.install`, `sysctl.set`, `file.write`).

The policy flag in the descriptions is what `sash --needs --policy` prints for that key: the systemapi channel-policy grant that adds it.

## Host verbs

Run by the local agent, one call each, behind the grant check and the agent re-check.

| form | kind | key | mutates | what |
| --- | --- | --- | --- | --- |
| `ps` | verb | ps | no | Process table as rows. No policy flag. |
| `unit status` | verb | unit.status | no | `unit status <unit>` as rows. Grant: `--service-status <unit>` (status only) or `--service <unit>`. A bare `unit` is status too; `unit <unit>` is exit 2 `usage unit`. The agent refuses status without exactly one unit (`--needs` note `not_grantable`). |
| `unit restart` | verb | unit.restart | yes | `unit restart <unit>`, exactly one unit. Grant: `--service <unit>`. |
| `journal` | verb | journal | no | Journal read. The agent has no reader yet, so no grant can carry it: it is always denied (`--needs` note `not_grantable`). |
| `package query` | verb | package.query | no | `package query <name>` as rows. Grant: `--package <name>`. |
| `package install` | verb | package.install | yes | `package install <name>`, exactly one name. Grant: `--package <name>`. |
| `sysctl get` | verb | sysctl.get | no | `sysctl get <key>`. Grant: `--tweak-sysctl <key>`. |
| `sysctl set` | verb | sysctl.set | yes | `sysctl set <key> <value>`. Grant: `--tweak-sysctl <key>`. |
| `config read` | verb | config.read | no | `config read <path>`, a clean absolute path. sash checks the `config.read` key and that the path is under a `file_read` root (outside is `denied file.read`), not the `file.read` key; the agent re-checks with its own policy. Grant: `--config <path>`. There is no `config write`. |

## Files and text

Paths go through the agent's file helper under the grant's `file_read` and `file_write` roots; sash never opens a host path. Grant: `--file-read <dir>` or `--file-write <dir>`. Reads disclose host data: `cat`, `template`, `use`, `config read`, and the command lines in `ps` print whatever the host holds, credentials included, and a recording keeps it.

| form | kind | key | mutates | what |
| --- | --- | --- | --- | --- |
| `cat` | verb | file.read | no | `cat <path>...` reads files. With no path it copies the bytes already in the pipeline and needs no key. |
| `ls` | verb | file.read | no | `ls [<dir>]` lists a directory as rows. |
| `cd` | builtin | file.read | no | `cd <dir>` moves the session prefix (sash state, not the process cwd). The target must be inside a read root. |
| `pushd` | builtin | file.read | no | `pushd <dir>` pushes the prefix. |
| `dirs` | builtin | - | no | Shows the prefix stack. |
| `popd` | builtin | - | no | Pops the prefix; popping the last entry is exit 2 `usage popd`. |
| `echo` | filter | - | no | Prints its words and a newline. A list or map prints as JSON. |
| `egrep` | filter | - | no | `egrep <re>`: keeps matching lines (Go RE2). Exactly one pattern, no flags. |
| `head` | filter | - | no | `head`, `head <n>`, or `head -n <n>`: first lines (10 by default). `head -1` and `head -<n>` print nothing and exit 0: use `head -n 1` or `head 1`. |
| `cut` | filter | - | no | `cut [-d <sep>] -f <n>`: one field per line, whitespace-split without `-d`. |
| `wc` | filter | - | no | Counts lines of text, or rows. Flags are ignored: `wc -c` and `wc -w` print the line count too. |
| `template_file` | verb | file.write | yes | `template_file <spec> <tpl> <out> [--check]`: validate and render to `<out>`. `--check` writes nothing and prints a diff (only `file.read`). |
| `template` | verb | file.read | no | `... \| template <tpl>`: render a template with the piped value. |

```sash
# expect: host 0
# nostdout: listen
cat /etc/nginx/nginx.conf | head -1
```

```sash
# stdout: listen 80;
cat /etc/nginx/nginx.conf | head -n 1
```

```sash
# stdout: 2
cat /etc/nginx/nginx.conf | wc -c
```

```sash
# expect: host 0
# drop: file.read
# call: config.read argv=config read /etc/app.conf
config read /etc/app.conf
```

```sash
# expect: host 126 denied file.read
# nocall: config.read
config read /root/app.conf
```

## Row tools

In process, no key. They take rows on `|>`; `rows`, `lines`, and `parse` lift text into rows.

| form | kind | key | mutates | what |
| --- | --- | --- | --- | --- |
| `where` | filter | - | no | `where <col> <op> <value>`, op `==` `!=` `<` `<=` `>` `>=`. |
| `select` | filter | - | no | `select <col>...`: keep columns. |
| `rows` | filter | - | no | `rows [<name>...]`: text to rows. No names is one column `line`; named columns split on whitespace, the last keeps the rest. |
| `sort` | filter | - | no | `sort by <col> [desc] [, <col>...]`. |
| `uniq` | filter | - | no | `uniq [<col>...]`: drop repeated rows. |
| `group` | filter | - | no | `group by <col>`, then `count`, `sum`, `avg`, `min`, or `max`. |
| `count` | filter | - | no | Count rows, or per group. |
| `sum` | filter | - | no | `sum <col>`, or per group. |
| `avg` | filter | - | no | `avg <col>`, or per group. |
| `min` | filter | - | no | `min <col>`, or per group. |
| `max` | filter | - | no | `max <col>`, or per group. |
| `first` | filter | - | no | `first <n>`. |
| `last` | filter | - | no | `last <n>`. |
| `skip` | filter | - | no | `skip <n>`. |
| `filter` | filter | - | no | `filter [ <expr> ]`: keep rows where it holds; `$it` is the row. |
| `each` | filter | - | no | `each [ <expr> ]`: a map adds or replaces columns, a scalar gives column `value`. |
| `lines` | filter | - | no | Text to rows with a `line` column. |
| `parse` | filter | - | no | `parse "{a} {_} {b}"`, or `parse re '<re with (?P<name>...)>'`: text to rows. |
| `sub` | filter | - | no | `sub <col> <re> with <repl>`: rewrite one column. |

## Data formats

In process, no key. Input is capped and nested data stops at depth 64.

| form | kind | key | mutates | what |
| --- | --- | --- | --- | --- |
| `json` | filter | - | no | Bytes print as one JSON string, rows as one JSON array. |
| `from json` | filter | - | no | Parse JSON text into a value. |
| `to json` | filter | - | no | Print a value or rows as JSON. |
| `from yaml` | filter | - | no | Parse one YAML document; a multi-document stream is a list. |
| `to yaml` | filter | - | no | Print a value as YAML. |
| `from toml` | filter | - | no | Parse TOML into a map; dates become RFC 3339 strings. |
| `to toml` | filter | - | no | Print a map as TOML. |
| `from csv` | filter | - | no | CSV to rows, header first; `--noheader` names columns `c1`, `c2`, ... |
| `to csv` | filter | - | no | Rows as CSV; a cell starting `=` `+` `-` `@` gets a leading `'`. |
| `from tsv` | filter | - | no | TSV to rows; `--noheader` as for CSV. |
| `to tsv` | filter | - | no | Rows as TSV. |

## Control, settings, and modules

| form | kind | key | mutates | what |
| --- | --- | --- | --- | --- |
| `let` | keyword | - | no | `let x = <word>`, `let x = [ <expr> ]`, `let x = (<pipeline>)`. |
| `if` | keyword | - | no | `if <pipeline or [ expr ]> { } else if ... { } else { }`. |
| `else` | keyword | - | no | The other branch of `if`. |
| `for` | keyword | - | no | `for x in <src> { }`, `for k, v in <src> { }`: rows, a list, a map, a pipeline, a range. |
| `in` | keyword | - | no | The source of `for`. |
| `fn` | keyword | - | no | `fn name(a, b) { }` defines a command. Depth 8. |
| `while` | keyword | - | no | `while <cond> { }`. |
| `do` | keyword | - | no | `do { } while <cond>`: body first. |
| `try` | keyword | - | no | `try { } catch [e] { }`. A `try` without `catch` is a parse error. |
| `catch` | keyword | - | no | The handler of `try`; `e` is a map `status`, `message`, `key`. |
| `return` | keyword | - | no | `return [value]` from a `fn`. |
| `break` | keyword | - | no | Leave the innermost loop. |
| `continue` | keyword | - | no | Next iteration of the innermost loop. |
| `exit` | keyword | - | no | `exit [n]`, n 0 to 255: end the script. |
| `declare` | keyword | - | no | `declare -r -i -a -A -s name = value`; `declare` lists names, `declare -p name` shows one. |
| `use` | keyword | file.read | no | `use <path>`: load `fn` and `declare -r` from a sash file; nothing else may be in it. |
| `set` | builtin | - | no | Session options: `-e -u -x -f`, `-o name`, `+o name`, `loopmax N`, `verbmax N`. `set -o record <path>` also writes a file (a `file.write` at mode 0600 at the end; refused in plan mode, asked under confirm). |
| `shopt` | builtin | - | no | `shopt -s name`, `shopt -u name`: the same option table by long name. |
| `builtin` | builtin | - | no | Lists this table and the expression functions as rows; `builtin <name>` filters it. |
| `capture` | keyword | - | no | `let x = capture { pipeline }`: the output, error text and statuses as a map; refusals are not captured. |
| `defer` | keyword | - | no | `defer { block }`: runs when the enclosing function or script ends, last first, on every way out but a hangup. |
| `finally` | keyword | - | no | `try { } catch e { } finally { }`: runs after the body and any catch, on every way out; a failing finally replaces the status. |
| `retry` | keyword | - | no | `retry N [delay D] { }`: run again on failure. N 1 to 100, D up to 1h. |
| `args` | keyword | - | no | Static script argument schema: required values, typed defaults, choices, and help. |
| `await` | keyword | - | no | `await timeout D every D { pipeline }`: poll a read-only condition, bounded by deadline and caps. |
| `timeout` | keyword | - | no | `timeout D { }`: D up to 24h, checked before each statement and verb. |
| `atomic` | keyword | file.write | yes | `atomic [deadline D] { }`: on a failure the agent restores the file writes inside, except files another writer changed meanwhile; writes are visible as they land. The key is the writes it holds. |
| `par` | keyword | - | no | `par N [as r] { }` or `par N [as r] for x in <src> { }`: branches at once, N 1 to 16. |
| `with` | keyword | - | no | `with set -e, loopmax N, prefix P, x = v { }`: scoped, restored on every way out. |
| `check` | builtin | - | no | `check "name" [ <expr> ]`: prints `ok name` or `FAIL name` (status 1). |
| `checks` | builtin | - | no | The check rows so far (`check`, `result`). |
| `origin` | builtin | - | no | `origin name`: where a name's value came from, as rows. |
| `log` | builtin | - | no | `log debug\|info\|warn\|error <msg> [ {fields} ]`: one line on stderr. |

## Session words

The interactive editor handles these; they never reach a script. In a script, under `-c`, or on piped stdin they are unknown names (exit 127). Do not put them in a script.

| form | kind | key | mutates | what |
| --- | --- | --- | --- | --- |
| `why` | session | - | no | `why <line>` explains the grant check for that line and runs nothing; bare `why` explains the last line. |
| `logout` | session | - | no | Ends the session, like Ctrl-D. A bare `exit` at the prompt does the same. |
| `bind` | session | - | no | `bind vi` or `bind emacs`. |
| `abbr` | session | - | no | `abbr name 'text'`, `abbr -e name`, `abbr`: visible rewrites of a command word. |
| `z` | session | - | no | `z part...` goes to the most used and most recent visited directory containing each part in order, inside the `file_read` roots; `z -l part...` lists candidates. Exit 1 `z: no match`. In a script, exit 2 `z: prompt only`. |
| `jump` | session | - | no | `jump name`, `jump save name`, `jump list`, `jump rm name`: bookmarks (`[a-z0-9_-]{1,32}`, at most 64), checked against the roots on every use. In a script, exit 2 `jump: prompt only`. |
| `syntax` | session | - | no | `syntax on` or `syntax off`. The denial color stays either way. |
| `trace` | session | - | no | `trace on` or `trace off`: print each stage key before it runs. In a script use `set -x`. |
| `prompt` | session | - | no | `prompt line` (default) or `prompt menu`. |

The editor also takes the whole line `confirm off`, which asks `confirm off? [y + Enter / N]` before it clears confirm mode.
