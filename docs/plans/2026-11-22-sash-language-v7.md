# sash language v7: contract

2026-11-22. Ryan approved the direction on 2026-11-21 and 2026-11-22. Branch `feat/v7`, on top of `feat/niceties` (pipeline functions, `args`, structured errors, `await`). The init contract (`docs/init-contract`, `2026-11-21-sash-init-contract.md`) is separate. Its phase 1 builds on this one: `spawn`, `mkdir` and `mount` take path values, and `spawn` uses named streams.

## Rules for every item

- **No new way to run a program.** Nothing reparses a value as source, adds a `PATH` lookup, or changes the closed table's grant checks.
- **Statically visible.** `--needs`, `--lint`, plan preview and record/replay see every new form.
- **Secrets stay safe.** A secret never prints, never decides a branch, and never reaches a verb argument or a `>` body through a new form.
- **Tested both ways.** Each item has positive tests and refusal tests. Each refusal is a named exit-2 error.

## 1. Path as a primitive type

A path is a value kind of its own, not a string.

- **Make one:**
  - `let p = path /etc/nginx` or `declare -P p = /etc/nginx`;
  - in an expression, `path("/etc/nginx")`;
  - any absolute literal word in a path argument (a file verb's target, a redirect) is a path.
- **Invariants, checked once, when the path is made:**
  - absolute;
  - cleaned (no `.`, `..`, `//` or trailing `/`, except `/` itself);
  - no NUL byte, no control character;
  - at most 4096 bytes.

  A violation is exit 2 `path: <reason>`. A relative path needs the prefix: `path ./x` under `cd /srv` is `/srv/x`. Without a prefix it is exit 2 `prefix_unset`.
- **Join:** `$p / "conf.d" / $name`. Each right-hand part is one component: no `/`, not `.` or `..`, not empty. Otherwise exit 2 `path: join component`. A join cannot climb out of `$p`.
- **Accessors:** `$p.name`, `$p.dir` (a path), `$p.ext`, `$p.parts` (a list). `starts($p, $root)` compares whole components, so `/etc/nginx2` does not start with `/etc/nginx`.
- **Kinds are distinct.**
  - A glob is not a path: `*` in a path literal is a parse error, and globs stay words that expand before the run.
  - A string is not a path. In a script, a file verb given a string from data (`cat $s` where `$s` came from `from json`) is exit 2 `type path`; wrap it with `path(...)`. At the prompt, literal words keep working.
- **Printing:** a path prints as its text and renders in rows as text. `to json` writes a string.

**Why:** traversal by string concatenation (`"/srv/" + $user_input`), relative paths that change meaning after `cd`, and a string mistaken for a path all become type errors.

## 2. `defer` and `finally` (in place of `trap`)

- **`defer { block }`** runs when the enclosing function or script ends, in last-in, first-out order. It runs on a normal end, `return`, `exit`, an error, a stop under `set -e`, and a hangup. It does not run when the grant check refuses the script before it starts.
  - A defer's own failure is reported (`defer: <message>`) and the remaining defers still run. The script's exit status stays that of the first failure.
  - The defer body is checked like any statement: a mutation in it is planned under plan mode and denied without a grant.
  - At most 64 pending defers per function. More is exit 2 `defer max`.
- **`try { } catch e { } finally { }`**: `finally` runs after `try` and any `catch`, on every exit path, `return` and `break` included. A failure inside `finally` replaces the pending status, as Go and Python do, and `--lint` flags it (`finally-masks`).
- **No `trap` and no signal handlers.** A hangup runs the defers and then exits 129. For a service, the init calls `fn stop()`.

## 3. `$last` and `capture` (stderr and per-stage status)

There are no file descriptors. A verb's answer is output, an exit status and an error message.

- **`$last`** is a read-only map, set after every statement:

  | Field | Meaning |
  | --- | --- |
  | `status` | Exit status (the first failing stage) |
  | `stages` | List of each stage's status, like `${PIPESTATUS[@]}` |
  | `keys` | Each stage's verb key |
  | `code` | Stable error category (as `$e.code`) |
  | `message` | Error message |
  | `failed_stage` | Index of the first failing stage, or `null` |

  `$last` never holds stdout. A secret's text never appears in `message`, which says `***`.
- **`capture { pipeline }`** returns a map `{out, err, status, stages, keys, code, failed_stage}` and does not throw for an ordinary failure:
  - `out` is the bytes or rows;
  - `err` is the error text;
  - grant denials, secret refusals, caps and hangups still stop the script, so they are never hidden by accident.

  A capture of a secret value is itself secret.

## 4. Redirection forms that look like bash

`2>`, `2>&1`, `&>`, `>&`, `>>`, `<&`, `1>`, and `N>` for any digit N are a parse error: exit 2 `parse: <form> is not sash redirection: use capture { } for errors`. `>>` says `use template_file or atomic for appends`. Today `echo a 2> x` writes `a 2` to `x`; that silent misparse ends.

## 5. `z` and `jump` (prompt only)

- **`z <part>...`** goes to the most frequent and recent directory whose path contains each part in order. The history is per actor and per host, stored by the agent with the command history and sharing its privacy (a private session records nothing). It only ever offers directories inside the grant's `file_read` roots. `z` with no match is exit 1 `z: no match`. `z -l <part>` lists the candidates and goes nowhere.
- **`jump <name>`** goes to a saved bookmark. `jump save <name>` stores the current prefix, `jump list` lists them and `jump rm <name>` removes one. Bookmarks live with the history, are checked against the roots each time they are used, and names are `[a-z0-9_-]{1,32}`.
- **Both are session commands** (`syntax`, `why` and `abbr` are the same kind). In a script they are exit 2 `z: prompt only`, because a script must not depend on someone's history.

## 6. Named streams for `spawn` (init contract)

`spawn ... opts [ {"stdout": S, "stderr": S} ]`, where S is `journal`, `null`, `file:<path>` (a path in `file_write`), or for stderr only, `stdout`. No descriptor numbers, so there is no `2>&1` ordering to get wrong. `output NAME [stderr] [tail N]` reads what the init captured, as rows. This item is specified here, but it is built with init phase 1, because it needs the init's verbs.

## Build order

1. Item 4, the redirection parse errors: small, and it removes a live trap.
2. Item 3, `$last` and `capture`.
3. Item 2, `defer` and `finally`.
4. Item 1, the path type, the largest (it touches every file verb, `--needs` and the grant checks).
5. Item 5, `z` and `jump`.
6. Init phase 1, with item 6.

Each step is its own commit with tests, followed by `./build.sh test` and an adversarial review of the batch before any push.

## Out of scope

Background jobs (`&`), aliases, `config.write` and fleet-wide history stay out, as `TODO_CODEX_OR_CLAUDE.md` says.
