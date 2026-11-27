# sash: the language

Read this when you write anything past a one-line pipeline: values, pipes, expressions, control flow, functions, options, globs, and the exit statuses and error strings a run prints.

Contents: Values · Words and quoting · Pipes · Redirection · Expressions · Binding · Control flow · Functions · Errors and jumps · Options · Globs and expansion · Exit statuses · Error strings

## Values

A value is a string, bytes, rows, one row, or data. Data comes from `[ ]` expressions and the `from` formats:

| kind | literal (inside `[ ]` only) |
| --- | --- |
| number | `42`, `-3`, `2.5` (int64 or float64) |
| bool | `true`, `false` |
| list | `[1, "a", $x]` |
| map | `{"port": 8080}`; keys are strings, insertion order is kept |
| null | `null` |

Outside `[ ]` a bare `42` is a string. Rows are a list of maps that keeps its column order.

Access: `$cfg.server.port`, `$list[0]`, `$m["a key"]`, `$row.field`. A missing map key is `null`. An unbound name is exit 2 `unset <name>`; a field on a value that has no fields is exit 2 `type value`.

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

There is no `>>` and no `&`. There is no `2>` either: `2>` is the word `2` followed by `>`, so `echo a 2> x` writes `a 2` to `x`. `>` of rows writes the text table; `>` after `json` writes the JSON. Every stage and redirect path of a pipeline is checked against the grant before that pipeline starts: a path outside the roots is exit 126 `denied file.read` or `denied file.write`.

```sash
unit status nginx |> json > /var/tmp/ops/nginx.json
egrep error <<< "one error line"
```

```sash
# expect: host 0
# call: file.write argv=> /srv/app/x path=/srv/app/x mode= body="a 2\n"
echo a 2> /srv/app/x
```

`>` writes whatever the pipeline produced, whatever its stages' statuses, also under `set -e`: a stage that fails or matches nothing still truncates the file to what came out, even nothing.

```sash
# expect: host 1
# call: file.write argv=> /srv/app/out path=/srv/app/out mode= body=""
set -e
cat /etc/nginx/nginx.conf | egrep zzz > /srv/app/out
```

So write with `| fs write-atomic <path>` (`features.md`), which sends nothing when a stage failed, or write to a new path and check it, or put the writes in `atomic { }` (which rolls back on a non-zero statement), and read the byte count in the plan output before the real run:

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
| terminal | `is_tty()` |

`is_tty()` takes no argument and is `true` only when a person can be asked: sash's stdin and stderr are both terminals and the script is not on stdin (`sash -c`, `sash file`, or the prompt). `sash < script`, a pipe, a job, and a replay are `false`. It is not pure like the others: it reads the terminal, and a recording pins it.

```sash
# expect: host 0
# stdout: unattended
if [ is_tty() ] { echo "a person is here" } else { echo unattended }
```

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

`declare` flags: `-r` read-only (rebinding is exit 2 `readonly <name>`), `-i` number, `-a` list, `-A` map (a wrong type is exit 2 `type <name> <type>`), `-s` secret (see `features.md`). `declare` alone lists names, types, and flags, never values; `declare -p name` shows one. There is no `-x` and no `export`: sash starts no process.

At the prompt, names, functions, and options carry from line to line. A script and `sash -c` start fresh.

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

A function is a command: call it with words, one per parameter (a wrong count is exit 2 `usage <name>`). `return [value]` gives the call's value. Each call checks the grant again. A function cannot be named after a closed-table form (exit 2 `usage fn`), cannot be a pipe stage or take a redirect (exit 2 `type fn`), and there are no default parameters. A call deeper than eight is exit 2 `fn depth`.

```sash
# expect: empty 2 fn depth
fn f() { f }
f
```

## Errors and jumps

```sash
try { echo "{bad" | from json } catch e { echo $e.status $e.message }
try { unit status nginx } catch e { echo $e.key }
```

`e` is a map: `status`, `message`, `key`. A denial can be caught: the denied statement ran nothing. Catching does not retry. `try` without `catch` is a parse error; `catch { }` is the explicit way to ignore a failure, and lint reports it as `silent-catch`.

```sash
# expect: parse try needs catch
try { ps }
```

What `try` cannot catch: `loop max`, `verb max`, `a secret ended the line`, and a jump. `return` outside a `fn` is exit 2 `usage return`; `break` outside a loop is `usage break`; `exit n` takes 0 to 255 (otherwise `usage exit`).

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

## Options

`set` and `shopt` write one option table; `set` alone lists it as rows. No option touches the grant, the roots, or the agent re-check.

| option | set it | default | meaning |
| --- | --- | --- | --- |
| errexit | `set -e`, `set -o errexit` | off | a failed stage ends the script |
| nounset | `set -u`, `set -o nounset` | off | a missing map key is an error (`unset key <k>`), not `null`; an unbound name is always exit 2 `unset <name>`, with or without it |
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

## Output to a terminal

Byte output (`echo`, `cat`, a value printed) that goes to a terminal is escaped at the last write: a line feed and a tab pass, and every other control character, format character (bidi, zero-width), rune that draws nothing (a default-ignorable code point such as a variation selector or a Hangul filler, and the blank braille pattern U+2800), stacking combining mark past the fourth on one base, and unprintable rune prints as `<U+XXXX>`, a byte that is not UTF-8 as `<0xHH>`, and a literal `<U+` or `<0x` as `<U+003C>U+` or `<U+003C>0x`. Row cells and messages keep their own form (`\x1b`). A pipe, a file, a value, a `>` body, and a recording get the bytes unchanged, so what a script computes never depends on this. It keeps host data from driving the screen; it does not make that data trustworthy.

## Exit statuses

| status | meaning |
| --- | --- |
| 0 | success |
| 1 | an ordinary failure: no match, a `FAIL` check; the script goes on. A verb's own status passes through unchanged (3 stays 3) |
| 2 | parse, type, or usage error, a cap, a refusal (secret, atomic, wasm, `command_from_value`), a grant that does not verify; the script stops |
| 126 | denied by the grant, or declined at a confirm prompt; that statement ran nothing, earlier ones did |
| 127 | unknown name; no host binary is run |
| 129 | hangup (SIGHUP) |
| 130 | a `par` interrupted (Ctrl-C), or Ctrl-C at a `term` question |

## Error strings

Each is one line on stderr.

| stderr | cause |
| --- | --- |
| `unknown <name>` | not in the closed table and not a `fn` (127) |
| `denied <key>` | the grant lacks the key, or a path is outside the roots; earlier statements already ran (126) |
| `declined <key>` | answered no at a confirm prompt (126) |
| `command_from_value` | a command word that comes from a value, `$x status nginx` (2); `command_from_value: a term prompt answer selects no subcommand`, a `term prompt` answer as the word that selects what `unit`, `package`, or `sysctl` does (2) |
| `type \|>` | bytes piped into `\|>` (2) |
| `unset <name>` | a name that was never bound (2) |
| `type value` | a field on a value with none, or a whole row as a word (2) |
| `type fn` | a function used as a pipe stage or with a redirect (2) |
| `fn depth` | a call deeper than eight (2) |
| `loop max`, `verb max` | a cap; the line ends, `try` cannot catch it (2) |
| `timeout` | past a `timeout` deadline; `try` and `retry` can catch it (2) |
| `readonly <name>` | rebinding a `declare -r` name (2) |
| `prefix_unset` | a relative path or glob with no session prefix (2) |
| `type glob` | a glob qualifier other than `(/)`, or a glob that matches nothing (2) |
| `parse: ...` | `parse: try needs catch`, `parse: too deep` (65 nested blocks), `parse: env not in this series` (2) |
| `usage <form>...` | wrong arguments to a form (2) |
| `usage fs write-atomic` | the sink given the wrong words, or not last, or with no input, or with a `>` after it (2) |
| `fs write-atomic: not written` | an earlier stage failed, so the sink sent nothing; the status is that stage's |
| `term needs a terminal` | `term` with no terminal to ask; the script stops (2) |
| `term prompt: end of input` | Ctrl-D on an empty line, or the input ended (1) |
| `term prompt: empty answer` | Enter on an empty line: no answer, as at the end of input (1) |
| `term prompt: answer over 4096 bytes`, `term prompt: answer is not text` | an answer that is too long or not UTF-8; there is no value (2) |
| `term: interrupted` | Ctrl-C at a question; the script stops, `try` cannot catch it (130) |
| `term inside par`, `atomic_irreversible: term` | a question inside `par` or `atomic { }` (2) |
| `term prompt -s: only as declare -s name = (term prompt -s "question")` | `-s` anywhere but the whole value of a `declare -s` (2) |
| `replay_secret <seq>` | a replay reached a `term prompt -s`; a recording never holds that answer (2) |
| `a secret cannot decide a branch`, `a secret cannot leave sash`, `a secret ended the line` | secret rules, `features.md` (2) |
| `atomic_irreversible: <what>`, `atomic: rolled back` | `features.md` (2) |
| `wasm_unsupported` | the release build has no wasm (2) |
| `plan: would <command>` | plan mode stopped a mutating verb (the run goes on) |
| `grant_missing`, `grant_bad_shape`, `grant_bad_sig`, `grant_star`, `grant_expired`, `grant_root_slash` | `sash --session` refused the grant before anything ran (2) |
