# sash language v5: the line, lint, and par

Status: approved by Ryan 2026-11-12. Every "Recommended" below is the decision, plus three explicit answers: `par` waits for a multiplexed verb pipe (its own agent contract), so section 8 is semantics only; Tab completes seen paths only; and a bare `try` without `catch` becomes a parse error (section 0). Tier 3 (WASM, provenance, atomic, replay) is `2026-11-12-sash-v6-tier3.md`.

Every v1–v4 rule holds: no PATH exec, no `&`, no `eval`; values never parsed as source; a command name is a literal; every verb goes through `machine.prepare` and the agent re-checks it; everything is bounded by the 8 MiB value cap and depth 64. Nothing in v5 adds a door to the parent. Sections 1–7 are display and editing, 8 is concurrency, 9 is a gate at the one door (`machine.do`).

## 0. `try` requires `catch`

Today `runTry` with no catch discards the failure and its stderr: `try { echo "{bad" | from json }` then `echo after` prints `after` with exit 0. That is the silent-failure trap sash avoids everywhere else. From v5, `try` without `catch` is a parse error (exit 2, `try needs catch`). `try { … } catch { }` remains the explicit way to ignore a failure, and lint's `silent-catch` still reports an empty catch. `retry`, `timeout`, and `with` are unchanged. Parse, `why`, preview, `--needs`, and `--lint` all see the same error.

## 1. Live grant highlighting

Most of this exists. `Loop` (`internal/line/editor.go`) calls `View` before every byte it reads, and `View` calls `lang.Preview` (`internal/lang/preview.go`), which parses, walks `needsEachPipe`, runs `prepare` against `denyParent`, and blocks a denied line. v5 fixes what it shows:

| case | today | v5 |
| --- | --- | --- |
| denied verb | `FaceDenied` on spans keyed to the denial, dim even with syntax off (`sgr`) | unchanged |
| out-of-root path | `bindPaths`/`needRead` return `denied file.read`, so `Preview` dims every `file.read` span on the line | `FaceOutOfRoot` on the one path word or redirect target; `bindPaths` returns the argv index it refused |
| unknown command | `FaceUnknown` (`stageFace`, exit 127) | unchanged |
| `command_from_value` | `plainArgv` fails, `paintStage` paints the stage `FaceFilter` (green: looks safe) | `FaceFromValue` on argv[0], and Block |
| subcommand from a value (`unit $op x`) | `FaceFilter` | `FaceDynamic` on that word, matching the `needs` note `dynamic` |

**Latency.** `Preview` p99 ≤ 2 ms on a `maxLine` (8192-byte) line nested 64 deep; `BenchmarkPreviewMaxLine` guards it (measured p99 about 1.15 ms on an i9-9880H). Drop the duplicate `lex` in `Preview`.

**Caching.** A one-entry memo in `Editor` keyed by (line, prefix, State generation). `State` gains a generation counter. Every write (`assign`, fn definition, `set`, `declare`, `check`) happens inside `Run`, so `Run` bumps it once when it returns, whatever ran; `SetXtrace` and `LockPlan`, the only writes outside `Run`, bump it too. One bump per `Run` cannot miss a write site. Cursor, menu, and history browsing reuse the memo.

**Paste.** `Loop` reads one byte at a time and repaints, so pasting 8 KiB costs 8192 previews of a growing line. v5 feeds every buffered byte up to the next Enter, then one `View`, then the Enter on its own. Feeding the Enter after that paint keeps each submission shown whole before its output, as the comment in `Loop` requires.

**Huge or deep lines.** `maxLine` and depth 64 cap the work. Only `[ ]` expressions (and decoded data) are capped at depth 64; block nesting (`if { if { … } }`) has no depth cap of its own and is bounded by `maxLine`. A line that fails to lex or parse is Block with the error and no partial colouring.

**Never the parent.** `Preview` uses `denyParent` and never calls `Expand`; globs list through `walk` only at Enter, in `submit`. Test: a parent that fails the test on any call while a fuzz corpus is typed key by key.

**Display only.** `submit` refuses a Block line, `Run`'s `prepare` repeats the check, and the agent re-checks.

- Decision: block `command_from_value` lines in the editor, or only colour them. Recommended: block, because the run would refuse that stage after running the statements before it, and `Preview` already blocks a denial for the same reason.
- Decision: escape format and C1 runes in the painted line, or reject them at insert. Recommended: paint them as their `needsCell` escape in reverse video and keep the bytes, because `decode` (`keys.go`) accepts any rune ≥ 0x80 and a pasted U+202E can disguise the line; rejecting would silently change pasted text. `Frame` must count columns on the painted form.

## 2. `sash --lint [-c script | file | stdin]`

Static, conservative, advisory. Lint parses the way `Needs` does (history, braces, parse caps). It runs nothing, calls no parent, reads only the script, and does not follow `use`.

| id | severity | triggers | does not trigger |
| --- | --- | --- | --- |
| `unused-binding` | info | a `let`, `declare`, `for` name, `catch` name, or fn parameter that no `$name` or `[ ]` reference reads anywhere in the file | names starting `_`; `declare -r` (an export for `use`); any top-level binding when the file has a `use` |
| `unreachable` | warning | a statement after `exit`, `return`, `break`, or `continue` in the same block | code after an `if` whose arms both jump (conservative) |
| `constant-check` | warning | `check` or `if` whose `[ ]` reads no name and calls no function other than pure ones on literals | `while [ true ]` with a `break` in its body |
| `retry-on-mutation` | warning | a `retry` body that reaches a stage keyed in `mutating` (`eval.go`), a `template_file` without `--check`, or a `>` redirect, including through fns called by literal name (depth 8) | mutations in a nested `try` with a catch |
| `secret-to-sink` | error | a secret-tainted name reaching `echo`, `log`, a `check` name or condition, an `if`/`while`/`filter` condition, `template`/`template_file`, a verb argument, a redirect path, a fn argument, or `exit`/`return` words | — |
| `silent-catch` | warning | `try { }` with no catch (`runTry` discards the failure and its stderr), or an empty catch block | a catch with any statement |
| `incomplete-needs` | warning; error for `command_from_value` | each `Needs` row with a note, from the `needsRun` rows (not by parsing text) | literal needs |
| `shadowed-secret` | warning | a `declare -s` name also bound by `let`, plain `declare`, `for`, `catch`, a fn parameter, or `with x =` | a second `declare -s` of the same name |

**Taint.** Lint's taint is flow-insensitive: a name bound by `declare -s`, or bound from any word, expression, or pipe that references a tainted name, is tainted everywhere in the file, iterated to a fixpoint. Run-time taint (`secretVal`, `evalCtx.tainted`, `machine.sawSecret`, `decide`) stays the only enforcement. Lint reports, at the desk, the lines a run would print as `***` or refuse with exit 2. It must over-report; a missed sink is a lint bug.

**Output and exit.** Columns `line`, `rule`, `severity`, `what`, sorted by line then rule, through `tabwriter` like `needsTable`, every cell through `needsCell`. `what` never prints a tainted literal. Exit 0 for none or info only, 1 for any warning or error, 2 for usage or parse errors with nothing on stdout, matching `--needs`.

- Decision: `sash --lint` or `sash lint`. Recommended: `--lint`, because `readSource` (`cmd/sash/main.go`) already runs a file argument, so `sash lint` would run a script named `lint`.
- Decision: suppression `#lint:allow <rule> <reason>` on the same line, or none. Recommended: allow it for warning and info rules only, with a required reason, never for errors, because `retry package.install` is a real pattern but a secret sink is never fine. The lexer drops comments (`lex.go`), so suppressions need a position side channel.

## 3. Completions

Compiled in, from `lang.Builtins`, extended with a `Flags []string` per form (`--check`, `set -e -u -x -f -o`, `shopt -s -u`, `declare -r -i -a -A -p -s`, `--noheader`). Nothing is loaded from disk. v5 completes the word at the cursor (today `tab` replaces the whole line with `row.insert`).

- Command position: forms and subcommands from the table. Denied rows are dim and insert nothing (`acceptMenu`).
- After `$`: names from `State.vars` and names bound earlier on the line, never values.
- Fn names: `State.fns` and `fn` definitions on the line.
- Paths: see the decision. Every shown path goes through `needsCell`, and every inserted path is single-quoted when it is not a plain word.

- Decision: may Tab call the agent's `ls` (key `file.read`, as `walk` does), or do paths come only from what the session has already seen? Recommended: seen only, filtered by `grant.AllowsRead` in process. "Seen" means literal paths in submitted lines, the prefix stack, and glob results at Enter. `proto.Client.Do` holds one mutex across a blocking write and read with no request id, so an `ls` cannot be cancelled, and each Tab would add an agent audit row. Revisit with a multiplexed pipe.

## 4. Right prompt

Fixed fields in fixed order, rendered once per `Frame` as literal text:

- `Grant.Purpose`, through `needsCell`, capped at 32 runes.
- Time to expiry: `Grant.Exp` minus `nowFn()`, shown as `exp 41m`, or `expired`. Display only; the agent re-checks.
- `plan` when plan mode is on (new `State.Plan()`).
- `secrets N`, a count of `secretVal` vars (new `State.SecretCount()`). Names and values are never shown.
- `!N` when `Last.Status` is non-zero.

Placed with `\x1b[{col}G` from the terminal width (new `TIOCGWINSZ` in `term_*.go`). Dropped when it would overlap the line or when the width is unknown.

There is no prompt variable, no format string, and no expansion. CVE-2021-45444: zsh `PROMPT_SUBST` expanded a value (a branch name) a second time and ran code. Here no displayed value is ever parsed.

- Decision: show `secrets N` or not. Recommended: show it, because the operator needs to know a secret is live before running `declare -p` or `trace on`. The count reveals nothing the source does not.

## 5. String and collection functions in `[ ]`

`exprFuncs` (`funcs.go`) already has `len`, `upper`, `lower`, `trim`, `split`, `join`, `replace`, `starts`, `ends`, `pad`, `repeat`, `keys`, `values`, and `has`. New in v5: `contains(s, sub) → bool` and `contains(list, x) → bool` (deep equality).

**Caps.** Every result is charged before it is built against the per-evaluation budget of `dataMaxBytes` (8 MiB). `repeat` already refuses `n > dataMaxBytes/len(s)`. `contains` charges nothing new; the scan is linear.

**Taint.** An evaluation that reads a secret makes its whole result secret (`evalCtx.tainted`), so `contains($tok, "a")` is a secret bool and `decide` refuses it as a condition. It is not an oracle. An error in that evaluation is `errSecretFault`, which `try` cannot catch. Test: each function with a secret argument returns a secret, and used as an `if` condition it exits 2.

- Decision: add `starts_with`/`ends_with` as aliases, or keep `starts`/`ends`. Recommended: keep; two spellings double the surface lint and docs must cover.
- Decision: `contains` on a map. Recommended: no; `has` already answers it.

## 6. Abbreviations

`abbr name 'text'` and `abbr -e name` are interactive only, handled by the editor like `bind`. They never reach `State`, so scripts, `use`, `--needs`, and `--lint` never see them. `name` is a plain word, not `reserved`. `text` is a literal (no `$`, no `[ ]`) of at most 256 bytes with `needsCell(text) == text` and `!DeclaresSecret(text)`. Expansion is not recursive.

Space or Enter after a command-position word equal to `name` replaces the word in the buffer: the visible rewrite the alias rule in `TODO_CODEX_OR_CLAUDE.md` asks for.

- Decision: Enter on an unexpanded abbreviation expands and runs, or expands and stops. Recommended: stops, because an abbreviation can name `unit restart`, and the reader should see the verb before a second Enter.
- Decision: session-only, or a file. Recommended: session-only in v5. CVE-2022-20001 was fish running config from a repo in the current directory. An agent-stored list would need its own contract, like history. There will never be an rc file in the prefix, the home directory, or a `use` path.

## 7. History autosuggestions

This exists: `suggestionSuffix` takes the newest history line with the typed prefix, `Paint` draws it grey, and only Right or Ctrl-F at end of line inserts it. `submit` runs `e.line` only. `pushHist` and `loadHist` drop `DeclaresSecret` lines, and a private session loads no history.

v5 fix: `loadHist` drops only lines containing `\n`, `\r`, or `\x00`, so ESC, C1, and bidi bytes in agent-stored history reach the terminal raw through the suggestion and through browsing. v5 drops any history line containing a rune `needsCell` would escape, other than a backslash.

- Decision: suggest from lines that no longer parse, or only parseable ones. Recommended: any line, because the preview colours it once accepted, and parsing every history line per keystroke breaks the section 1 budget.

## 8. `par N { }`

**Shape.** `par N { s1; s2 }`: each top-level statement is a branch. `par N for x in src { body }`: each iteration is a branch. `1 ≤ N ≤ 16`. No `&` and no detached work, so the agent's one-process reap rule stands.

**State.** Each branch runs on its own `machine` over `State.clone()`, with its own `out`/`err`, prefix stack, `sawSecret`, and `ctl`. Branch bindings are discarded at the join; `checks` rows merge in branch order. A `break`, `continue`, `return`, or `exit` that would leave a branch is a parse error.

**Failure.** The first failure that escapes a branch cancels its siblings at statement and verb boundaries (where `pastDeadline` and `do` already check). An in-flight verb finishes, as with `timeout`. A `try` inside a branch keeps its failure in the branch. `par` fails with the first failure's status and message, which an enclosing `try` catches.

**Output.** Buffered per branch, emitted in branch order after the join, already redacted by `render`.

**Rows.** `par N as r { }` binds `r` to rows `branch, item, status, message, key`.

**Budgets.** `verbmax` is one shared atomic count across branches. `loopmax` counts `par for` iterations like any loop. The deadline is shared.

**Plan mode.** `plan` and `planLocked` copy with the clone, so plan mode holds in every branch. **Secrets.** `secretVal` is immutable and copies safely; taint is per evaluation.

**Verb pipe.** `proto.Client.Do` serializes on one mutex and `proto.Serve` handles one request at a time, so concurrent `Do` calls are safe but not parallel. A broken frame mid-call leaves the pipe unusable; v5 marks the client broken and fails every branch closed.

- Decision: build `par` on the serialized pipe now, or wait for a multiplexed pipe (request ids, an agent-side concurrency limit per lease). Recommended: wait. With one serialized pipe, `par` buys no verb parallelism, only overlapped `retry` delays, while adding every race above. Ship the semantics when the agent contract lands.
- Decision: under `set -o confirm`, run `par` with N forced to 1, or prompt from N branches. Recommended: force 1, so prompts arrive in source order with their context.

## 9. Confirm mode

`set -o confirm` (interactive only) asks `confirm <key> <argv>? [y/N]` (through `needsCell`) for each verb in `mutating` (`eval.go`): the set plan mode stops, including the `file.write` from `>` and `template_file`, in loops and `par`. The check sits in `machine.do` after the `verbmax`, deadline, and plan checks, so plan mode asks nothing.

The answer comes from the editor's own reader, through a new `Options.Confirm` callback that `Loop` sets. It discards buffered and pending input (`tcflush(TCIFLUSH)`), draws the prompt, then reads one key. Only `y` or `Y` is yes; Enter, any other key, EOF, or Ctrl-C is no, and Ctrl-C also stops the line.

**No tty.** `sash file`, `-c`, and stdin runs have no `Confirm`, so `set -o confirm` there is exit 2 `confirm needs a terminal`. It fails closed.

**No** is status 126 with `declined <key>`, and behaves as a denial: `retry` does not retry it, and `try` catches it with `key` set (`runTry` must accept the `declined ` prefix).

**Timeout.** Waiting counts against `timeout`. The deadline is checked again after the answer and before the parent call.

- Decision: offer "yes to all for this line". Recommended: no, because a loop is where a wrong yes costs most.

## Threats

- **Under-reporting.** Lint is advisory and must over-report secret sinks (flow-insensitive taint). `incomplete-needs` reuses `Needs` rows, so the builtin-table test covers both. A clean lint never means safe.
- **Terminal injection.** Every new surface (lint cells, the right prompt purpose, the confirm prompt, completion paths, history suggestions, the painted line) goes through `needsCell` or the section 1 and 7 escaping. Today's gaps: `loadHist`, and `Paint` of the line and suggestion.
- **Completion side effects.** No verb on Tab; the completion table is compiled in.
- **Concurrency races.** `State` maps, `machine` fields, and `checks` are unsynchronized. `par` gets clones and a shared atomic `verbmax`; the pipe is serialized and fails closed on a broken frame; `go test -race` covers `par`.
- **Confirm spoofing.** Pasted or piped `y` never answers: typeahead is flushed, the answer is one key read after the prompt is drawn, and there is no tty fallback.
- **Prompt and LLM injection.** Purpose strings, history, script literals, and lint output are data; nothing in v5 parses them as source or runs them. A human Enter, plus `y` under confirm, is the only trigger.
- **Abbreviations as code.** Session-only literals, no rc file, a visible rewrite, and Enter stops after expanding.
- **Secret oracles.** New functions taint like the old ones; `secrets N` reveals a count only.

## Known issues found while drafting (separate fixes)

- `proto.Serve` (`internal/proto/proto.go:97`) has `_ = Write(...)` on the frame-body error path, against the no-`_ =` rule.
- A bare `try { }` with no `catch` discards the failure and its stderr. Decided: section 0 makes it a parse error.
- The editor does not wrap: a line wider than the terminal draws past the right edge and the cursor column is wrong. `Frame` counts display width (East Asian wide 2, combining 0, from a compact table in `paint.go`), but line wrapping is out of scope for v5.

## Order

0. `try` requires `catch`, and the `proto.Serve` `_ =` fix.
1. Live grant highlighting: faces, the `bindPaths` index, the memo, paste coalescing, escaping.
2. `--lint`, rules in table order, `secret-to-sink` with negative tests first.
3. Completions (seen paths only).
4. Right prompt.
5. `contains`, and secret-taint tests for every function.
6. Abbreviations (session-only).
7. History autosuggestion escaping fix.
8. `par`: semantics doc only until the multiplexed pipe has an agent contract.
9. Confirm mode. If `par` is deferred, confirm can land before it; its `par` rule applies when `par` lands.
