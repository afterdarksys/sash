# sash: the language

Read this when you write anything past a one-line pipeline: values, pipes, expressions, control flow, functions, options, globs, and the exit statuses and error strings a run prints.

Contents: Values · Words and quoting · Pipes · Redirection · Expressions · Binding · Control flow · Functions · Script arguments · Errors and jumps · Options · Globs and expansion · Exit statuses · Error strings

## Values

A value is a string, bytes, rows, one row, or data. Data comes from `[ ]` expressions and the `from` formats:

| kind | literal (inside `[ ]` only) |
| --- | --- |
| number | `42`, `-3`, `2.5` (int64 or float64) |
| bool | `true`, `false` |
| list | `[1, "a", $x]` |
| map | `{"port": 8080}`; keys are strings, insertion order is kept |
| null | `null` |
| path | `path("/etc/nginx")`, or `let p = path /etc/nginx` (see Paths below) |

Outside `[ ]` a bare `42` is a string. Rows are a list of maps that keeps its column order.

Access: `$cfg.server.port`, `$list[0]`, `$m["a key"]`, `$row.field`. A missing map key is `null`. An unset name, or a field on a value that has no fields, is exit 2 `type value`.

A value is never parsed again as source. `let c = "unit restart nginx"` then `echo $c` prints text; it cannot run. A command name must be a literal word on the line (see `command_from_value` below).

## Words and quoting

- `'single'` is literal. `"double"` expands `$name`; write a regex in single quotes, because `$` in double quotes starts a name.
- `$a$b` and `"$a$b"` are one word. There is no word-splitting.
- `#` starts a comment.
- `;` or a newline separates statements. `&&` and `||` chain pipelines.
- `{a,b}c` is brace expansion, `!!` and `!$` are history; both rewrite the line before it runs, so the grant check sees the result.

```sash
let x = 1
echo "dq $x" $x$x 'single $x'
```

## Pipes

`|` carries bytes: the left side is rendered to text. `|>` carries rows and accepts rows only. A row command with no row consumer prints its text table.

```sash
ps -ef | egrep sshd | head -n 5
unit status nginx |> where state == "failed"
ps |> group by user |> count |> sort by count desc
```

A byte stage before `|>` is a type error, exit 2, and that statement does not run. `rows`, `lines`, `parse`, and the `from` formats lift text into rows; a byte pipeline never calls `rows` for you.

```sash
# expect: empty 2 type |>
echo hello |> where line == "hello"
```

```sash
echo "alice 1
bob 2" | rows name n |> where n > 1
```

`json` prints bytes as one JSON string and rows as one JSON array. `to json` prints a value or rows as JSON.

```sash
ps |> select user pid |> json
echo '[{"name": "web1", "cpu": 90}]' | from json |> where cpu >= 50 |> select name
```

A pipeline fails when any stage fails. `if` on a row pipeline is true when it yields a row; on a byte pipeline, when its exit is 0. A `where` that keeps nothing succeeds and is false in `if`.

## Redirection

| form | meaning |
| --- | --- |
| `< path` | stdin from a file, through `file.read`; first stage only |
| `> path` | write the rendered text through `file.write`; last stage only; stdout stays empty |
| `<<< word` | here-string: the word and a newline as stdin, not a file read |

There is no `>>`, no `&`, and no file descriptor redirection: `2>`, `2>&1`, `1>`, `&>`, `>&`, `<&` and `N<` are exit 2 `parse: <form> is not sash redirection: use capture { } for errors`, and `>>` says `use template_file or atomic for appends`. A word and `>` need a space between them to be a word and a redirect: `echo 2 > x` writes `2`. `>` of rows writes the text table; `>` after `json` writes the JSON. Every explicit stage and redirect path of a pipeline is checked against the grant before that pipeline starts. A function body is checked as its statements are reached; earlier pipeline stages may already have run when its body is denied. For explicit stages and redirects: a path outside the roots is exit 126 `denied file.read` or `denied file.write`.

```sash
unit status nginx |> json > /var/tmp/ops/nginx.json
egrep error <<< "one error line"
```

```sash
# expect: parse 2> is not sash redirection: use capture { } for errors
echo a 2> /srv/app/x
```

`>` writes whatever the pipeline produced, whatever its stages' statuses, also under `set -e`: a stage that fails or matches nothing still truncates the file to what came out, even nothing.

```sash
# expect: host 1
# call: file.write argv=> /srv/app/out path=/srv/app/out mode= body=""
set -e
cat /etc/nginx/nginx.conf | egrep zzz > /srv/app/out
```

So write to a new path and check it, or put the writes in `atomic { }` (which rolls back on a non-zero statement), and read the byte count in the plan output before the real run:

```sash
# expect: plan 1 plan: would write /srv/app/out (0 bytes)
cat /etc/nginx/nginx.conf | egrep zzz > /srv/app/out
```

## Expressions

Expressions live only inside `[ ]`. Outside, a line is words and pipelines, so `<` and `>` stay redirects (except right after `where <col>`, where they compare).

Operators: `+ - * / %`, `== != < <= > >=`, `&& || !`, `in`, string `+` and `*`. Arithmetic reads a string that is exactly a decimal number. Comparison does not: two strings compare as strings, so `[ "00" == "0" ]` is false. Expressions are one line, assign nothing, and cannot call a verb. Division by zero is exit 2 `type expr div zero`.

Ranges: `[ 1..10 ]`.

Functions, pure and in process (`builtin` lists them; a name not here is exit 2 `type expr unknown function <name>`):

| group | functions |
| --- | --- |
| text | `len` `upper` `lower` `trim` `split(s, sep)` `join(list, sep)` `replace(s, old, new)` `starts(s, p)` `ends(s, p)` `pad(s, n)` `repeat(s, n)` `contains(s, sub)` |
| regex (RE2, pattern up to 1 KiB) | `match(s, re)` `find(s, re)` `groups(s, re)` `sub(s, re, repl)` |
| convert | `int` `float` `str` `bool` `type` |
| list | `len` `sum` `min` `max` `avg` `sort` `uniq` `reverse` `first(l, n)` `last(l, n)` `range(a, b)` `contains(list, x)` |
| map | `keys` `values` `has(m, k)` `get(m, k, default)` `merge(a, b)` |
| encode | `sha256(s)` `base64(s)` `unbase64(s)` `hex(s)` `uuid()` |
| path | `path(s)` `starts(p, root)` (two paths compare whole components) |

```sash
let n = [ 1 + 2 ]
let ok = [ $n >= 3 && "web" + "1" == "web1" ]
echo [ split("a,b", ",") ] [ upper("x") ] [ 2 in [1, 2] ]
```

## Binding

```sash
let a = word
let b = [ {"port": 8080, "hosts": ["web1", "web2"]} ]
let c = (ps |> where user == root)
declare -r port = 8080
declare -i count = 0
echo $b.port $b.hosts[1]
```

`declare` flags: `-r` read-only (rebinding is exit 2 `readonly <name>`), `-i` number, `-a` list, `-A` map, `-P` path (a wrong type is exit 2 `type <name> <type>`), `-s` secret (see `features.md`). `declare` alone lists names, types, and flags, never values; `declare -p name` shows one. There is no `-x` and no `export`: sash starts no process.

At the prompt, names, functions, and options carry from line to line. A script and `sash -c` start fresh.

### Paths

A path is its own kind of value, not a string. Make one with `let p = path /etc/nginx`, `declare -P p = /etc/nginx`, or `[ path("/etc/nginx") ]`. It is checked once, when it is made: absolute, clean (no `.`, `..`, `//` or trailing `/`), valid UTF-8 with no control, format or line-separator character, at most 4096 bytes. A violation is exit 2 `path: <reason>`. A relative path (`path conf.d` or `path ./conf.d`) joins the current prefix; with none it is exit 2 `prefix_unset`. A glob in a path literal is exit 2 `parse: a glob is not a path`.

- Join with `/` inside `[ ]`: `[ $p / "conf.d" / $name ]`. Each right-hand part is one component: not empty, not `.` or `..`, no `/`. Otherwise it is exit 2 `path: join component`, so a join cannot climb out of `$p`.
- Accessors: `$p.name`, `$p.dir` (a path), `$p.ext`, `$p.parts` (a list).
- `starts($p, $root)` compares whole components: `/etc/nginx2` does not start with `/etc/nginx`. A path never equals a string; `str($p)` is its text.
- A path prints as its text, and `to json` writes it as a string.
- In a script (a file or `sash -c`), a file path given to `cat`, `ls`, `cd`, `pushd`, `config`, `template`, `template_file`, `use`, `<` or `>` must be literal text or a path. A word that expands text read from a host response, such as `cat $r.name` after `ls` or a field of `from json` over `cat` output, is exit 2 `type path`: build it with `path(...)` or a join, which checks it. Text the script wrote itself (a literal, an argument) is not from the host. At the interactive prompt, and when a session recording replays, words keep working.

```sash
let conf = path /etc/nginx
for r in ls /etc/nginx/conf.d {
  cat [ $conf / "conf.d" / $r.name ]
}
```

## Control flow

```sash
let n = 0
if [ $n < 10 ] { echo small } else if [ $n < 100 ] { echo medium } else { echo large }
for x in [ 1..3 ] { echo $x }
for i, x in [ ["a", "b"] ] { echo $i $x }
for k, v in [ {"x": 1} ] { echo $k $v }
for p in ps |> where user == root { echo $p.pid }
while [ $n < 3 ] { let n = [ $n + 1 ] }
do { let n = [ $n - 1 ] } while [ $n > 0 ]
```

`for` walks a list, a map's keys (or key and value), rows, a pipeline, or a range. There is no C-style `for` and no `foreach`. `break` and `continue` act on the innermost loop.

A pipeline condition (`if`, `while`, `await`) is decided by what it produced: rows by whether there is at least one; bytes by exit 0; a function's returned bool by its value (`fn up() { return [ false ] }; if up { ... }` takes the else branch); returned rows by whether there is one. A secret never decides a branch.

Caps: one loop runs at most `loopmax` iterations (default 100000); the same cap bounds the rows of `filter` and `each`, the lines of `parse`, and the items of `par for`. One run (one prompt line, or a whole script) calls at most `verbmax` agent verbs (default 10000). Past either the run ends with exit 2 `loop max` or `verb max`, and `try` does not catch it.

```sash
# expect: host 2 verb max
set verbmax 2
ps
ps
ps
```

```sash
# expect: host 2 loop max
set loopmax 1
ps |> filter [ true ]
```

```sash
# expect: empty 2 loop max
let i = 0
while [ true ] { let i = [ $i + 1 ] }
```

## Functions

```sash
fn restart_if_failed(u) {
  if unit status $u |> where state == "failed" { unit restart $u }
}
fn add(a, b) { return [ $a + $b ] }
let r = (add 2 3)
restart_if_failed nginx
```

A function is a command: call it with words, one per parameter (a wrong count is exit 2 `usage <name>`). `return [value]` gives the call's value. Each call checks the grant again. A function cannot be named after a closed-table form (exit 2 `usage fn`). Functions accept pipes and redirects; there are no default function parameters. A call deeper than eight is exit 2 `fn depth`.

```sash
# expect: empty 2 fn depth
fn f() { f }
f
```

When a function participates in a pipeline or has a redirect, `$input` is its input value, scoped to that call. `|` supplies text; `|>` supplies rows. A function's stdout becomes its output, or an explicit `return` supplies a typed value. Mixing stdout with an explicit returned value is exit 2 `type fn: cannot mix stdout and return in a pipeline`; use `log` for diagnostics. `$input` is restored after the call, including nested calls. A parameter named `input`, or a read-only outer `input`, cannot be used for a piped call.

```sash
# stdout: HELLO
fn loud() { return [ upper($input) ] }
echo hello | loud | egrep HELLO
```

```sash
# stdout: active
fn services() { return [ [{"state": "active"}] ] }
fn identity() { return $input }
services |> identity |> where state == active |> select state
```

A function's output type is checked at runtime before the next row stage. `|>` refuses non-row output. Secrets cannot cross a piped function boundary or its output redirect. A plain standalone function retains its previous stdout and return behavior. Pipes remain buffered, not streaming.

## Script arguments

An optional `args { }` declaration must be the first top-level statement. Each entry is `name type required` or `name type default literal`, optionally followed by `choices "a,b,c"` and `help "description"`. Types are `string`, `int` (signed 64-bit), `bool` (`true` or `false`), and `duration` (positive, at most 24h; bound as its duration string). Names are letters, digits, and `_`, not starting with a digit, at most 64 characters (`parse: argument name ... is not an identifier`). Defaults, choices, and help must be literal, not expressions or variable references, and hold no `*`, `{` or `}` (a declaration reads nothing, so it never globs). `help` is reserved.

```text
args {
  service string required help "Service to inspect"
  wait duration default 30s
  environment string default dev choices "dev,prod"
}
unit status $service
```

Invoke with an explicit separator:

```text
sash inspect.sash -- --service nginx --environment prod
sash inspect.sash -- --help
sash -c 'args { name string required }; echo $name' -- --name nginx
```

Every option takes a separate value, including booleans. Unknown, duplicate, missing, or invalid arguments are exit 2 before source expansion or host calls. Values are data and are never reparsed as source. Generated `--help` lists the schema and runs no script commands. These arguments apply to normal script execution, including `--plan` and `--session`; the analysis and offline tape command-line modes still take only their documented arguments. Do not put secrets in command-line arguments.

```sash
# stdout: nginx 3
args {
  service string default nginx
  attempts int default 3
}
echo $service $attempts
```

## Errors and jumps

```sash
try { echo "{bad" | from json } catch e { echo $e.status $e.message }
try { unit status nginx } catch e { echo $e.key }
```

`e` is a map: `status`, `message`, `key`, `code`, `source`, `line`. `code` is a stable category: `timeout`, `denied`, `declined`, `unknown_command`, `invalid_argument`, `type_error`, `parent_error`, `refused`, `limit_exceeded`, `secret_refused`, `hangup`, `await_mutation`, `runtime_error`, or `command_failed`. This does not expose new host-specific error details. `source` is the script filename (or `script`), or the file loaded with `use`; `line` is one-based in expanded source. Function errors identify the failing body statement. A denial can be caught: the denied statement ran nothing. Catching does not retry. `try` without `catch` is a parse error; `catch { }` is the explicit way to ignore a failure, and lint reports it as `silent-catch`.

```sash
# expect: parse try needs catch
try { ps }
```

What `try` cannot catch: a mutation or session-option change attempted inside `await`, `loop max`, `verb max`, `a secret ended the line`, and a jump. `return` outside a `fn` is exit 2 `usage return`; `break` outside a loop is `usage break`; `exit n` takes 0 to 255 (otherwise `usage exit`).

How a script continues: a statement whose status is 1 (an `egrep` that keeps nothing, a `FAIL` check) or any other status a verb returns (3 stays 3) does not stop it. Status 2, 126, or 127 stops the rest of the script, and the program status is that stopping status; a failed `par` stops it at any status. With `set -e`, any failed stage ends the script.

The grant is checked statement by statement, when each is reached, not for the whole script first. Statements before a denial have already run, mutations included:

```sash
# expect: host 126 denied file.write
# call: unit.restart argv=unit restart nginx
# stdout: hi
echo hi
unit restart nginx
echo a > /etc/x
```

Run `sash --needs` on a script to see every grant it needs before running it, and group file writes that must land together in `atomic { }` (`features.md`).

A status that does not stop the script is still that statement's status; the script ends with the last statement's status, so here the run ends 0 and then 3:

```sash
# expect: host 0
# stdout: after
sysctl get fail.three
echo after
```

```sash
# expect: host 3
echo before
sysctl get fail.three
```

## `defer` and `finally`

There is no `trap`. `defer { block }` runs when the enclosing function, or the script, ends: on success, `return`, `exit`, an error, a `set -e` stop or a cap, last deferred first. A hangup stops the session at once, defers included. A defer that fails is reported, the others still run, and the first failure keeps its status. A defer body is checked, planned and denied like any statement, and cannot `break`, `continue` or `return` (exit 2). At most 64 pending defers per function (exit 2 `defer max`).

`try { } catch e { } finally { }` runs `finally` after the body and any catch, on every way out, a `return` or `break` included. A `finally` that fails replaces the status.

```sash
# stdout: body
# stdout: second
# stdout: first
defer { echo first }
defer { echo second }
echo body
```

## `$last` and `capture`

sash has no file descriptors: a verb answers with output, an exit status, and an error message. After every statement `$last` is a read-only map: `status`, `stages` (each stage's status, like bash `${PIPESTATUS[@]}`), `keys` (each stage's verb), `failed_stage` (index of the first failing stage, or `null`), `code` and `message` (as `$e`). `let last = ...` is exit 2 `readonly last`.

`let r = capture { pipeline }` binds a map of `out` (the output: text, rows or data), `err` (the error text, which is not printed), `status`, `stages`, `keys`, `failed_stage`, `code` and `message`, and an ordinary failure (a failed command, bad data, a timeout) does not fail the statement. A refusal is not captured: a denial, an unknown name, a secret refusal, a cap, or a hangup fails exactly as it would without `capture`. A secret stays `***` in the map.

```sash
# stdout: 1 [0,1,0] 1
echo hi | egrep zz | head -n 1
echo $last.status $last.stages $last.failed_stage
```

```sash
# stdout: 2 json syntax
let r = capture { echo x | from json }
echo $r.status $r.err
```

## Options

`set` and `shopt` write one option table; `set` alone lists it as rows. No option touches the grant, the roots, or the agent re-check.

| option | set it | default | meaning |
| --- | --- | --- | --- |
| errexit | `set -e`, `set -o errexit` | off | a failed stage ends the script |
| nounset | `set -u`, `set -o nounset` | off | a missing map key is an error, not `null` |
| xtrace | `set -x`, `set -o xtrace` | off | print `trace <stage>` on stderr before each stage |
| noglob | `set -f`, `set -o noglob` | off | `*` stays a literal word on later lines |
| plan | `set -o plan` | off | mutating verbs print `plan: would ...` and do not run |
| confirm | `set -o confirm` | off | ask before each mutating verb; terminal only |
| pipefail | always on | on | `set +o pipefail` is exit 2 |
| loopmax | `set loopmax N` | 100000 | iterations one loop may run |
| verbmax | `set verbmax N` | 10000 | agent verbs one run (prompt line or script) may call |

`+` turns a flag off (`set +e`, `set +o plan`). `shopt -s name` and `shopt -u name` are the long names. The long name needs `-o`: `set errexit` is exit 2 `usage set`.

`set loopmax N` and `set verbmax N` take 1 up to the default. `set` may lower a cap and raise it back to its default, never past it; the same holds inside `with`.

```sash
# expect: empty 2 above the ceiling
set loopmax 200000
```

```sash
set -e
set loopmax 1000
set loopmax 100000
```

Locks: under `sash --plan`, `set +o plan` is exit 2 `usage set: plan is locked on for this run`. Once confirm is on, `set +o confirm`, `shopt -u confirm`, and `with set +o confirm` are exit 2 `confirm is locked`. Without a terminal, `set -o confirm` is exit 2 `confirm needs a terminal`. `set -o record <path>` is in `features.md`.

## Globs and expansion

`*` and `**` list through `file.read` (argv `ls`) under a read root; sash never opens the directory. `**` descends into child directories under the same root. `*(/)` and `**(/)` keep directories only; any other qualifier is exit 2 `type glob`, and so is a glob that matches nothing. A quoted `*` is literal.

```sash
cat /var/log/nginx/*.log | egrep error
echo /srv/app/**
```

A run accepts `echo /srv/app/*(/)`, but `--lint` and `--needs` read a script before expansion and refuse the qualifier as a parse error (exit 2), so a script that uses one cannot be linted:

```sash
# expect: parse expected separator
echo /srv/app/*(/)
```

Globs, braces, and history expand once, before the line or script runs, against the prefix it starts with. In a script the prefix starts unset, so write globs as absolute paths: a relative glob is exit 2 `prefix_unset` even after a `cd`. A short name is never rewritten.

```sash
# expect: host 2 type glob
echo /srv/app/*.nomatch
```

## Exit statuses

| status | meaning |
| --- | --- |
| 0 | success |
| 1 | an ordinary failure: no match, a `FAIL` check; the script goes on. A verb's own status passes through unchanged (3 stays 3) |
| 2 | parse, type, or usage error, a cap, a refusal (secret, atomic, wasm, `command_from_value`), a grant that does not verify; the script stops |
| 126 | denied by the grant, or declined at a confirm prompt; that statement ran nothing, earlier ones did |
| 127 | unknown name; no host binary is run |
| 129 | hangup (SIGHUP) |
| 130 | a `par` interrupted (Ctrl-C) |

## Error strings

Each is one line on stderr.

| stderr | cause |
| --- | --- |
| `unknown <name>` | not in the closed table and not a `fn` (127) |
| `denied <key>` | the grant lacks the key, or a path is outside the roots; earlier statements already ran (126) |
| `declined <key>` | answered no at a confirm prompt (126) |
| `command_from_value` | a command word that comes from a value, `$x status nginx` (2) |
| `type \|>` | bytes piped into `\|>` (2) |
| `type value` | an unset name, or a field on a value with none (2) |
| `type fn` | a function used as a pipe stage or with a redirect (2) |
| `fn depth` | a call deeper than eight (2) |
| `loop max`, `verb max` | a cap; the line ends, `try` cannot catch it (2) |
| `timeout` | past a `timeout` deadline; `try` and `retry` can catch it (2) |
| `readonly <name>` | rebinding a `declare -r` name (2) |
| `prefix_unset` | a relative path or glob with no session prefix (2) |
| `path: <reason>`, `type path` | a path that is not absolute and clean, a bad join, or host text used as a script's file path (2) |
| `type glob` | a glob qualifier other than `(/)`, or a glob that matches nothing (2) |
| `parse: ...` | `parse: try needs catch`, `parse: too deep` (65 nested blocks), `parse: env not in this series` (2) |
| `usage <form>...` | wrong arguments to a form (2) |
| `a secret cannot decide a branch`, `a secret cannot leave sash`, `a secret ended the line` | secret rules, `features.md` (2) |
| `atomic_irreversible: <what>`, `atomic: rolled back` | `features.md` (2) |
| `wasm_unsupported` | the release build has no wasm (2) |
| `plan: would <command>` | plan mode stopped a mutating verb (the run goes on) |
| `grant_missing`, `grant_bad_shape`, `grant_bad_sig`, `grant_star`, `grant_expired`, `grant_root_slash` | `sash --session` refused the grant before anything ran (2) |
| `grant_exp_zero`, `grant_service_mismatch`, `grant_identity_mismatch` | exp 0 on a grant that is not a boot grant; a service file under a grant for another service, or with another user or group (2) |
| `service_top_level`, `service_run_and_start`, `service_glob`, `entry_missing` | a service file's rules, `features.md` "Service files" (2) |
| `secret_refused`, `spawn_shell`, `env_refused: NAME`, `denied exec`, `denied cap`, `denied user`, `denied group`, `denied fs.mount`, `denied mount source`, `mount_overlap`, `mount_opt_refused`, `denied cwd`, `denied probe: ...` | a service verb's refusals, `verbs.md` (2 or 126) |
| `refused <key>: <code> (<why>)` | the init refused a service verb: `unknown_name`, `name_in_use`, `denied`, `ceiling` (2) |
