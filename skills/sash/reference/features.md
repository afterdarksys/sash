# sash: features

Read this before you write a script that changes a host, runs work in parallel, handles a secret, renders a config, or records a run; and for the command-line modes (`--plan`, `--needs`, `--lint`, `--record`, `--replay`).

Contents: Plan mode · `--needs` · `--lint` · Confirm · Secrets · Templates · `atomic` · `par` · `retry` and `timeout` · `check` · `log` · `origin` · `with` · `use` · `set -o record` · Offline record and replay · `why` · Abbreviations · wasm

## Plan mode

`set -o plan`, or `sash --plan <script>`. The mutating verbs (`unit.restart`, `package.install`, `sysctl.set`, `file.write`) do not reach the agent: each prints `plan: would <command>` on stderr (a write prints `plan: would write <path> (<n> bytes)`) and succeeds. Reads run, so the logic sees real state. Plan does not change the grant: a denied mutation is still denied.

`sash --plan` locks plan on for that run; `set +o plan` is then exit 2. Write every script so its `--plan` run reads well, and have the person run it that way first.

```sash
set -o plan
unit restart nginx
echo ready > /var/tmp/ops/flag
```

## `--needs`

```text
sash --needs [--policy] (-c script | file | stdin)
```

Reads the script, runs nothing, calls no parent, takes no grant, and prints one row per need: `key`, `target`, `mutates`, `lines`, `note`. Every branch and every `fn` body counts. `--policy` prints instead the systemapi policy flags that would grant the literal needs (`--file-read <dir>`, `--service <unit>`, ...); anything a flag cannot express is a `# ` comment line. Exit 0 complete, 1 incomplete (a note: `dynamic`, `relative`, `not_grantable`, `use: not followed`, `unpasteable`, `command_from_value`), 2 usage or parse error.

The manifest is advisory: it grants nothing, and the agent still checks every verb. Literal targets make it complete; `unit $op nginx` lists both `unit.status` and `unit.restart` as `dynamic`.

## `--lint`

```text
sash --lint (-c script | file | stdin)
```

Static and advisory; runs nothing. Columns `line`, `rule`, `severity`, `what`. Exit 0 none or info only, 1 any warning or error, 2 usage or parse error. Rules: `unused-binding` (info), `unreachable`, `constant-check`, `retry-on-mutation`, `secret-to-sink` (error), `secret-fault`, `silent-catch`, `incomplete-needs` (error for `command_from_value`), `shadowed-secret`, `secret-file` (info), `secret-after-write`, `par-same-target`, `record-file` (info), `atomic-irreversible`. `#lint:allow <rule> <reason>` on the same line suppresses a warning or info rule, never an error. A clean lint does not mean safe.

`--lint` and `--needs` refuse every other flag (`--session`, `--plan`, each other) and anything after the source: exit 2, nothing runs.

## Confirm

`set -o confirm` (terminal only) asks `confirm <key> <argv>? [y + Enter / N]` before each mutating verb, in loops and `par` too. Yes is `y` or `Y` then Enter; anything else is no, which is exit 126 `declined <key>`. `retry` does not retry a no; `try` catches it. Once on, only the editor's own `confirm off` line clears it. In a script there is no terminal: exit 2 `confirm needs a terminal`.

## Secrets

`declare -s name = value` marks a name secret. So does `declare -s tok = (cat /srv/app/token)`. Anything computed from a secret is secret: an expression that reads one, a value built from one, a `template_file` render that read one.

- A secret prints as `***` everywhere: `echo`, `trace`, `declare -p`, `log`, `origin` hashes, errors, recordings.
- A secret decides nothing: a condition that reads one (`if`, `while`, `do`, `filter`, `check`) is exit 2 `a secret cannot decide a branch`.
- A secret does not leave sash: a verb argument, a path, or `>` is exit 2 `a secret cannot leave sash`. The one exit is `template_file`, which writes such a render with mode `0600`.
- A secret in a plain word is exit 2 `a secret in a word: build it inside [ ]`.
- An evaluation that reads a secret and fails (`[ int($tok) ]`) ends the line with `a secret ended the line`, which `try` cannot catch.
- The prompt does not keep a `declare -s` line in history, and `!!` cannot recall one.

Never put a secret's text in source: a literal in `declare -s` is still in the script, the history of whoever pastes it, and the audit. Read it from a file the grant allows.

```sash
declare -s tok = (cat /srv/app/token)
template_file /srv/app/app.yaml /srv/app/app.tpl /srv/app/app.conf
```

```sash
# expect: 2 a secret cannot leave sash
declare -s t = abcd
unit status $t
```

## Templates

`template_file <spec> <tpl> <out> [--check]` reads `spec` and `tpl` through `file.read`, validates, renders, and writes `out` through `file.write`. The spec is YAML, JSON, or TOML by extension (`.yaml`, `.yml`, `.json`, `.toml`): a map of variables, or `vars:` plus `schema:` (`type` string, number, bool, list, map; `required`, `enum`, `pattern`, `default`). A schema error is exit 2 and writes nothing. `--check` writes nothing, prints a line diff of `out` against the render (exit 1 when they differ), and needs only `file.read`. In plan mode `template_file` shows its diff.

`... | template <tpl>` renders with the piped value as the vars and prints the result.

Template syntax: `{{ expr }}` (the `[ ]` language; lists and maps render as JSON), `{{ if expr }} ... {{ else if expr }} ... {{ else }} ... {{ end }}`, `{{ for x in expr }} ... {{ end }}`, `{{# comment }}`, and `{{-` / `-}}` to trim whitespace. Vars are `$name` per top-level key and `$vars` for the whole map. A template cannot run a verb, read a file, or include another template.

Mode: a render that read a secret is written with mode `0600`, and its `--check` and plan diffs print `files differ (a secret is in the render or the file; not listed)`. An agent too old for the mode is exit 2 `agent too old for file.write mode`; sash never retries without the mode. Known limit: an encoding of a secret (base64, hex) is not its text and is written with the default mode.

```sash
template_file /srv/app/app.yaml /srv/app/app.tpl /srv/app/app.conf --check
cat /srv/app/app.yaml | from yaml | template /srv/app/app.tpl
```

## `atomic`

```sash
atomic deadline 120s {
  template_file /srv/app/app.yaml /srv/app/app.tpl /srv/app/app.conf
  echo on > /srv/app/flag
  check "conf" [ true ]
}
```

The file writes inside either all stay or are all restored by the agent from its own journal. D is 1s to 600s (default the agent's 600 s).

Refused statically, before the agent is asked anything, exit 2 `atomic_irreversible: <what>`: any `unit.restart`, `package.install`, or `sysctl.set` stage (a subcommand from a value counts), `use`, a nested `atomic`, and `par`, anywhere in the body or in a `fn` it calls by literal name. A `break`, `continue`, `return`, or `exit` that would leave the block is a parse error; `atomic` inside a `par` branch is too. A grant without `file.write` is `denied file.write` before begin.

Rollback: a failed `check` (with or without `set -e`), a non-zero statement, an uncaught error, a timeout or the block's deadline, or a cap stops the block, prints `atomic: rolled back`, and aborts; the block fails with that status (2 when it was 0), which `try` catches. A rolled-back run never ends 0, even after `try` or `exit 0`, so rolled-back writes cannot pass for landed ones. If the agent cannot open a transaction the block does not run: `atomic unavailable: agent too old, or no file write targets in the grant`. `refused atomic: atomic_restored` and `refused atomic: atomic_inconsistent` report a restore at the deadline and files left as found. Plan mode prints `plan: would begin atomic`, the writes, then `plan: would commit atomic`.

```sash
# expect: 2 atomic_irreversible: unit.restart
atomic { unit restart nginx }
```

## `par`

```sash
par 4 { unit status nginx; unit status sshd }
par 4 as r for u in [ ["nginx", "sshd"] ] { unit status $u }
```

Each top-level statement (or each iteration) is a branch; N is 1 to 16 (`usage par: N is 1 to 16`), and the agent's in-flight limit caps it further (1 against an agent without the multiplexed pipe). Output is buffered and printed in branch order after all finish. Branch bindings are discarded at the join; `checks` merge. `as r` binds rows `branch, item, status, message, key`. The first failure cancels the rest at their next statement or verb; a verb already started finishes, and a mutating one that finished after the cancel is reported. `par` fails with `par: branch N failed (exit S)`, which `try` catches. `verbmax` is shared. Under confirm, N is 1. Parse errors: `par inside par`, `atomic inside par`, and a jump that would leave a branch.

## `retry` and `timeout`

`retry N [delay D] { }` runs the block again on failure, up to N times (N 1 to 100, D up to 1h). A failure is what `try` catches; a cap or jump passes through; a confirm no is not retried. `timeout D { }` (D up to 24h) checks the deadline before every statement and agent call; past it is exit 2 `timeout`, which `try` and `retry` catch. A verb already in flight finishes. Durations are Go durations: `500ms`, `2s`, `5m`, `1h`. Lint warns `retry-on-mutation` when a retry body can repeat a mutation.

```sash
retry 3 delay 2s { package query nginx }
timeout 30s { package query nginx }
```

## `check`

`check "name" [ expr ]` records a row and prints `ok name` or `FAIL name`. A failed check is status 1 and stops the script only under `set -e` (or rolls back an `atomic`). `checks` returns the rows (`check`, `result`). Names are at most 256 bytes; a run keeps at most 10000 checks.

```sash
let p = (ps |> where cmd == nginx)
check "nginx running" [ len($p) > 0 ]
checks |> to json
```

## `log`

`log debug|info|warn|error <msg> [ {fields} ]` writes one line on stderr with an RFC 3339 time and the fields in key order. Control bytes become spaces; a secret logs as `***`.

```sash
log info "restarted" [ {"unit": "nginx"} ]
```

## `origin`

`origin name` (a name, not `$name`) returns rows `origin, key, target, line, hash`: which verb and target a value came from, and the sha256 of the response. A secret's hash, and the hash of anything it came from, prints `***`. An unknown name is exit 2 `unset <name>`. Provenance never goes into the audit or `log`.

```sash
let conf = (cat /etc/nginx/nginx.conf)
origin conf
```

## `with`

`with` items, then a block: `set` flags (`set -e`, `set -o plan`), `loopmax N`, `verbmax N` (same ceiling as `set`), `prefix <dir>` (checked like `cd`), and `name = value`. Each is restored on every way out. `noglob` is refused (`usage with: noglob is per line; use set -f`), a read-only name cannot be overridden, and `with env` is `parse: env not in this series`.

```sash
with set -e, loopmax 1000, prefix /var/log { ls }
```

## `use`

`use <path>` reads a sash file through `file.read` and loads only its `fn` definitions and `declare -r name = value` lines. Anything else in the file is exit 2 `use: only fn and declare -r name = value`. The path is a literal; there is no `source` of a value and no `eval`. `--needs` does not follow it (note `use: not followed`).

## `set -o record`

`set -o record <path>` records every later statement (verb requests and responses, clock readings, sleeps, uuids) and writes the recording at the end through `file.write` with mode `0600`, through the agent. The path is a literal under a `file_write` root. `set +o record`, the end of the script, or logout ends it. Secrets are redacted (`***`). A recording past about 6 MiB is split into `<path>.1` .. `<path>.N` segments, each chained to the last. Refused in plan mode (`record: refused in plan mode (plan writes nothing)`), inside `with`, through `shopt`, in a `par` branch, and while one is open. Recordings are for test and debug: anyone can recompute the hash chain, so it proves consistency, not who made it.

## Offline record and replay

```text
sash --record out.jsonl --fixture fixture.json [--expires D] (-c script | file)
sash --replay rec.jsonl --fixture fixture.json (-c script | file)
```

`--record` runs the script against a fixture, a strict JSON file naming a grant (`entitlements` without `*`, clean absolute `file_read` and `file_write` roots, never `/`) and canned `results` (`key`, optional `argv`, `kind` `rows` with `cols` and `rows`, or `bytes`, `exit`, `error`). No host is reached; the only file written is the new recording (mode 0600, refused if it exists). `--expires` is 1m to 365 days, default 7 days.

`--replay` takes the fixture's grant only and answers every call from the recording. Any difference is exit 2: `replay_diverged <seq>`, `replay_other_script`, `replay_expired`, `replay_bad_chain <seq>`, `replay_bad_segment <n>`, `replay_truncated <seq>`. A recording never authorizes anything.

## `why`

Interactive only. `why <line>` explains the grant check for that line and runs nothing: one row per stage and redirect, `command`, `key`, `granted`, `reason`, `fix`. Reasons include `granted`, `in_process`, `no_session`, `not_in_grant`, `outside_roots`, `prefix_unset`, `not_grantable`, `unknown`, `usage`, `value`, `value_path`. `fix` names the policy grant or profile that adds a missing key. It is display text; the agent still re-checks.

## Abbreviations

Interactive only: `abbr name 'text'` defines one, `abbr -e name` erases it, `abbr` lists the names. Space or Enter after the name in command position replaces it on the visible line, and Enter stops after expanding so the person sees the verb. The text is plain words (no `$`, `[ ]`, quotes, `;`, `|`, `&`, `#`, `<`, `>`), at most 256 bytes. Session only: no rc file, never in a script (exit 127), never seen by `--needs` or `--lint`. There are no aliases.

## wasm

`wasm m = <literal path> sha256 <64 lowercase hex>` loads a WebAssembly module through `file.read` and checks its hash; `[ m::fn(args) ]` calls an export as pure computation (JSON in, JSON out, no imports, no clock, no filesystem, a 1 s deadline, counted against `verbmax`). A secret argument is refused (`wasm_secret`). It needs a build with the `sashwasm` tag.

The shipped release binary is built without the `sashwasm` tag. There, a `wasm` statement or a module call exits 2 `wasm_unsupported` and reads nothing. Do not write `wasm` for a release host.

```sash
# expect: 2 wasm_unsupported
wasm m = /srv/ops/fmt.wasm sha256 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
```
