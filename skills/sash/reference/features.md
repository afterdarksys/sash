# sash: features

Read this before you write a script that changes a host, runs work in parallel, handles a secret, renders a config, or records a run; and for the command-line modes (`--plan`, `--needs`, `--lint`, `--record`, `--replay`).

Contents: Plan mode · `--needs` · `--lint` · Confirm · Secrets · Templates · `atomic` · `par` · `retry` and `timeout` · `check` · `log` · `origin` · `with` · `use` · `set -o record` · Offline record and replay · `why` · Abbreviations · wasm

## Plan mode

`sash --plan <script>`, or `set -o plan` inside a run. The mutating verbs (`unit.restart`, `package.install`, `sysctl.set`, `file.write`) do not reach the agent: each prints `plan: would <command>` on stderr (a write prints `plan: would write <path> (<n> bytes)`) and succeeds. Reads run, so the logic sees real state. Plan does not change the grant: a denied mutation is still denied.

Only `sash --plan` locks plan on: then `set +o plan`, `shopt -u plan`, and `with set +o plan { }` are exit 2 `usage set: plan is locked on for this run`. `set -o plan` inside a script is not a lock: a later `set +o plan`, in the script or in a `fn` loaded with `use` from a host file, turns it off and the mutations after it run. Write every script so its `sash --plan` run reads well, and have the person run it that way first. Never write `set +o plan` into a script.

```sash
unit restart nginx
echo ready > /var/tmp/ops/flag
```

```sash
# expect: plan 2 plan is locked on
set +o plan
```

```sash
# expect: host 0
# call: unit.restart argv=unit restart nginx
# Do not copy: this shows why set -o plan is not a lock. Never write set +o plan.
set -o plan
set +o plan
unit restart nginx
```

A plan run is not the real run. A read after a planned write sees the old state, because the write never happened, so a plan run can take other branches than the real one:

```sash
# stdout: listen 8080
# nostdout: listen 9090
# stderr: plan: would write /srv/app/app.conf
echo "listen 9090" > /srv/app/app.conf
cat /srv/app/app.conf
```

The same source in a real run reads what it wrote:

```sash
# expect: host 0
# stdout: listen 9090
# nostdout: listen 8080
echo "listen 9090" > /srv/app/app.conf
cat /srv/app/app.conf
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

Static and advisory; runs nothing. Columns `line`, `rule`, `severity`, `what`. Exit 0 none or info only, 1 any warning or error, 2 usage or parse error. Rules: `unused-binding` (info), `unreachable`, `constant-check`, `retry-on-mutation`, `secret-to-sink` (error), `secret-fault`, `silent-catch`, `incomplete-needs` (error for `command_from_value`), `shadowed-secret`, `secret-file` (info), `secret-after-write`, `par-same-target`, `record-file` (info), `atomic-irreversible`, `prompt-to-mutation` (a name bound from `term prompt`, or from a `fn` that asks it, reaches an argument, a path, or the body of a mutating stage), `prompt-subcommand` (error: a name bound from `term prompt` is the word that selects what `unit`, `package`, or `sysctl` does), `prompt-in-retry` (a `retry` body asks again on every attempt), `prompt-needs-terminal` (info, once per script), `chan-to-mutation` (a name bound by `for x in recv c`, or from one, reaches an argument, a path, or the body of a mutating stage), `chan-order` (two or more branches receive from one channel and mutate), `chan-secret` (a secret reaches `send`), `chan-unread-risk` (info: a receiver loop can `break`, or a `try` is around it). `#lint:allow <rule> <reason>` on the same line suppresses a warning or info rule, never an error. A clean lint does not mean safe.

`--lint` and `--needs` refuse every other flag (`--session`, `--plan`, each other) and anything after the source: exit 2, nothing runs.

## Confirm

`set -o confirm` (terminal only) asks `confirm <key> <argv>? [y + Enter / N]` before each mutating verb, in loops and `par` too. Yes is `y` or `Y` then Enter; anything else is no, which is exit 126 `declined <key>`. `retry` does not retry a no; `try` catches it. Once on, only the editor's own `confirm off` line clears it. In a script there is no terminal: exit 2 `confirm needs a terminal`. Never write `confirm off` or `set +o confirm` into source; the person turns confirm off themselves.

## Secrets

`declare -s name = value` marks a name secret; so does `declare -s tok = (cat /srv/app/token)`. Secrecy follows that binding, not the file and not the text. A value is secret when it is the bound name or is computed from it: an expression that reads it, a word built from it, a `template_file` render that sash links to it (below).

For those values:

- They print as `***`: `echo`, `trace`, `declare -p`, `log`, errors, and the `origin` hash.
- They decide nothing: a condition that reads one (`if`, `while`, `do`, `filter`, `check`) is exit 2 `a secret cannot decide a branch`.
- They cannot be a verb argument, a path, or the body of `>`: exit 2 `a secret cannot leave sash`. Only `template_file` writes a render it knows is secret, at mode `0600`.
- Rows an `each` computed from one hold `***`; they are not written either, as they are or rendered later in the pipeline (`| cat`, `|> json`): `>` and `fs write-atomic` are exit 2 `a secret cannot leave sash`, and a `let` that binds such rows binds a secret.
- One in a plain word is exit 2 `a secret in a word: build it inside [ ]`.
- An evaluation that reads one and fails (`[ int($tok) ]`) ends the line with `a secret ended the line`, which `try` cannot catch.
- The prompt does not keep a `declare -s` line in history, and `!!` cannot recall one.

What is not protected: the same secret reached any other way. Reading the token file again with `cat`, or the same text arriving inside another file, gives a plain value. It prints in clear, `>` writes it at the default mode, it passes as a verb argument, and lint says nothing. sash matches the text of bound secrets only in recordings, `template_file` renders, and `origin` hashes.

So: read a secret file once, only into `declare -s`, and never `cat` it, or a file that holds the same secret, any other way. Never put a secret's text in source: a literal in `declare -s` is still in the script, in the history of whoever pastes it, and in the audit.

```sash
# expect: host 0
# stdout: ***
# nostdout: s3cr3tvalue
declare -s tok = (cat /srv/app/token)
echo $tok
```

```sash
# expect: host 0
# stdout: s3cr3tvalue
declare -s tok = (cat /srv/app/token)
cat /srv/app/token
```

```sash
# expect: host 0
# call: file.write argv=> /var/tmp/leak path=/var/tmp/leak mode= body="s3cr3tvalue\n"
# nolint: secret
declare -s tok = (cat /srv/app/token)
let c = (cat /srv/app/db.yaml | from yaml)
echo $c.password > /var/tmp/leak
```

```sash
# expect: empty 2 a secret cannot leave sash
declare -s t = abcd
unit status $t
```

## Templates

`template_file <spec> <tpl> <out> [--check]` reads `spec` and `tpl` through `file.read`, validates, renders, and writes `out` through `file.write`. The spec is YAML, JSON, or TOML by extension (`.yaml`, `.yml`, `.json`, `.toml`): a map of variables, or `vars:` plus `schema:` (`type` string, number, bool, list, map; `required`, `enum`, `pattern`, `default`). A schema error is exit 2 and writes nothing. `--check` writes nothing, prints a line diff of `out` against the render (exit 1 when they differ), and needs only `file.read`. In plan mode `template_file` shows its diff.

`... | template <tpl>` renders with the piped value as the vars and prints the result.

Template syntax: `{{ expr }}` (the `[ ]` language; lists and maps render as JSON), `{{ if expr }} ... {{ else if expr }} ... {{ else }} ... {{ end }}`, `{{ for x in expr }} ... {{ end }}`, `{{# comment }}`, and `{{-` / `-}}` to trim whitespace. Vars are `$name` per top-level key and `$vars` for the whole map. A template cannot run a verb, read a file, or include another template.

Mode: a render is secret, and written with mode `0600`, only when sash can link it to a secret bound earlier in the same run: the spec's or the template's response hashes the same as a response a secret was bound from, or holds a bound secret's text (four runes or more anywhere, or a shorter text as a whole field), or the rendered output or a decoded spec value holds one. A secret that was not bound with `declare -s` in this run is written at the agent's default mode, in clear. So bind the spec file itself as a secret before the render; lint reports that as `secret-file`:

```sash
# expect: host 0
# call: path=/srv/app/db.conf mode=0600
# lint: secret-file
declare -s spec = (cat /srv/app/db.yaml)
template_file /srv/app/db.yaml /srv/app/db.tpl /srv/app/db.conf
```

```sash
# expect: host 0
# call: path=/srv/app/db.conf mode= body="password=s3cr3tvalue\n"
template_file /srv/app/db.yaml /srv/app/db.tpl /srv/app/db.conf
```

A secret render's `--check` and plan diffs print `files differ (a secret is in the render or the file; not listed)`. An agent too old for the mode is exit 2 `agent too old for file.write mode`; sash never retries without the mode. Known limits: an encoding of a secret (base64, hex) is not its text and is written with the default mode, and a secret bound after the write cannot reach back to it (lint `secret-after-write`).

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

The block asks the agent to journal each file before it writes it, and on any failure to restore every journaled file. D is 1s to 600s (default the agent's 600 s). What that does and does not promise:

- Writes land as they happen. Services and other readers see each file change before the block ends; there is no isolation.
- A restore puts back only what the agent journaled, and leaves as found any file another writer changed meanwhile: `refused atomic: atomic_inconsistent`. The host is then half changed, and the agent keeps the journal for an operator.
- If the session is lost mid-block (killed, the socket closed) and not by a hangup, sash sends no abort. The restore is the agent's, and the writes can stay until the transaction's deadline (up to 600 s); `refused atomic: atomic_restored` reports a restore the agent made at the deadline.
- The agent refuses a write, which aborts the block, past 64 files (`atomic_files`), when the snapshots would pass 32 MiB (`atomic_size`), when a file's mode is not 0600, 0640, or 0644 (`atomic_mode`), or when another user owns the file (`owner_changed`). These limits are the agent's; sash prints its messages for them as `refused file.write: <code> (...)`.

Refused statically, before the agent is asked anything, exit 2 `atomic_irreversible: <what>`: any `unit.restart`, `package.install`, or `sysctl.set` stage (a subcommand from a value counts), `use`, a nested `atomic`, and `par`, anywhere in the body or in a `fn` it calls by literal name. A `break`, `continue`, `return`, or `exit` that would leave the block is a parse error; `atomic` inside a `par` branch is too. A grant without `file.write` is `denied file.write` before begin.

Rollback: a failed `check` (with or without `set -e`), a non-zero statement, an uncaught error, a timeout or the block's deadline, or a cap stops the block, prints `atomic: rolled back`, and aborts; the block fails with that status (2 when it was 0), which `try` catches. A rolled-back run never ends 0, even after `try` or `exit 0`, so rolled-back writes cannot pass for landed ones. If the agent cannot open a transaction the block does not run: `atomic unavailable: agent too old, or no file write targets in the grant`. Plan mode prints `plan: would begin atomic`, the writes, then `plan: would commit atomic`.

```sash
# expect: host 1 atomic: rolled back
# call: file.write argv=> /srv/app/flag
# call: atomic.abort
# nocall: atomic.commit
atomic {
  echo on > /srv/app/flag
  check "never" [ false ]
}
```

```sash
# expect: empty 2 atomic_irreversible: unit.restart
atomic { unit restart nginx }
```

## `par`

```sash
par 4 { unit status nginx; unit status sshd }
par 4 as r for u in [ ["nginx", "sshd"] ] { unit status $u }
```

Each top-level statement (or each iteration) is a branch; N is 1 to 16 (`usage par: N is 1 to 16`), and the agent's in-flight limit caps it further (1 against an agent without the multiplexed pipe). Output is buffered and printed in branch order after all finish. Branch bindings are discarded at the join; `checks` merge. `as r` binds rows `branch, item, status, message, key`. A branch fails when its last status is not 0, so an `egrep` that matches nothing fails the `par`. The first failure cancels the rest at their next statement or verb; a verb already started finishes, and a mutating one that finished after the cancel is reported (`branch 1: unit.restart nginx finished after cancel (exit 0)`). `par` fails with `par: branch N failed (exit S)`, which `try` catches; uncaught, a failed `par` stops the script whatever its status, 1 included. A first Ctrl-C cancels the branches and waits (`par: interrupted`, exit 130); a second stops waiting and prints `outcome unknown: <key> <target>` for each mutating verb still out, whose result only the agent audit then holds. `verbmax` is shared. Under confirm, N is 1. Parse errors: `par inside par`, `atomic inside par`, and a jump that would leave a branch.

```sash
# expect: host 1 par: branch 1 failed (exit 1)
# nostdout: after
par 2 { egrep zz <<< a; echo b }
echo after
```

`branch { a; b }` makes several statements one branch of a `par N { }` body; anywhere else it is `parse: branch outside par`. `branch` is a reserved word, as `par` is.

```sash
# expect: host 0
# stdout: a1
par 2 { branch { echo a1; echo a2 }; echo b }
```

```sash
# expect: parse branch outside par
if [ true ] { branch { echo a } }
```

**Channels.** `par N chan jobs(16) { ... }` declares channels for that `par` only (1 to 4, buffer 1 to 64). `send jobs <value>` queues one value and waits while the buffer is full; `for x in recv jobs { }` takes them in order and ends when every branch that sends on `jobs` has ended. A channel is a name in the `par` text, never a value: it cannot be bound, returned, passed to a `fn`, or used in a nested `par`, and `send`/`recv` cannot be in a `fn` or a `retry` (`parse: send inside fn`, `parse: recv inside retry`); a whole channel `par` inside a `fn` or a `retry` is fine, each attempt a fresh `par`. Every channel needs a sending branch and a receiving branch, and no branch both sends on and receives from one. `chan`, `send`, and `recv` are reserved words. All branches start; the agent's in-flight limit gates door calls, not branch starts, and with one slot (confirm, replay, an agent without the multiplexed pipe) one branch runs at a time, the lowest-numbered runnable one, switching only when it waits on a channel or ends. Values left unread fail the `par` (`par: chan jobs: N unread`, exit 1); a send with no receiver left is status 1 (`send jobs: no receiver left`); all branches waiting is `par: deadlock: ...`, exit 2, which `try` does not catch. Bytes do not cross: lift them with `lines` or `rows`. A value is copied at `send`, at most 8 MiB, and 32 MiB across a `par`'s buffers. A secret stays a secret and a `term prompt` answer still selects no subcommand after it crosses; `origin` on a received value shows where the sender got it. Use one receiving branch to write a shared file, not several. `--needs` adds nothing for a channel, and a verb on a received value is `dynamic`. Lint carries a sent value into the names `recv` binds, so `secret-to-sink` and the prompt rules see through a channel, and adds `chan-to-mutation`, `chan-order`, `chan-secret`, and `chan-unread-risk`. A recording holds each `send`, `recv`, and close in order, with the value's hash, never the value, and replay follows that order or is `replay_diverged`.

```sash
# expect: host 0
# stdout: nginx done
par 4 chan jobs(16), done(16) {
  for p in ps |> where cmd == nginx { send jobs $p }
  for j in recv jobs { unit status $j.cmd; send done [ {"cmd": $j.cmd} ] }
  for j in recv jobs { unit status $j.cmd; send done [ {"cmd": $j.cmd} ] }
  for r in recv done { echo "$r.cmd done" }
}
```

```sash
# expect: host 1 par: chan jobs: 2 unread
par 2 chan jobs(4) {
  for i in [ [1, 2, 3] ] { send jobs $i }
  for j in recv jobs { echo $j; break }
}
```

```sash
# expect: host 2 par: deadlock: branch 1 recv a; branch 2 recv b
# nostdout: caught
try {
  par 2 chan a(1), b(1) {
    for x in recv a { send b $x }
    for y in recv b { send a $y }
  }
} catch { echo caught }
```

```sash
# expect: host 2 a secret cannot leave sash
# stdout: ***
# nocall: unit.restart
declare -s tok = (cat /srv/app/token)
par 2 chan c(1) { send c $tok; for t in recv c { echo $t; unit restart $t } }
```

```sash
# expect: parse send inside fn
fn push(x) { send jobs $x }
```

## `retry` and `timeout`

`retry N [delay D] { }` runs the block again on failure, up to N times (N 1 to 100, D up to 1h). A failure is what `try` catches; a cap or jump passes through; a confirm no is not retried. `timeout D { }` (D up to 24h) checks the deadline before every statement and agent call; past it is exit 2 `timeout`, which `try` and `retry` catch. A verb already in flight finishes. Durations are Go durations: `500ms`, `2s`, `5m`, `1h`. Lint warns `retry-on-mutation` when a retry body can repeat a mutation.

```sash
retry 3 delay 2s { package query nginx }
timeout 30s { package query nginx }
```

## `fs`

`... | fs write-atomic <path> [--mode 0600|0640|0644]` is a sink: the last stage of a pipeline, with input, and no `>` after it (anything else is exit 2 `usage fs write-atomic`). It writes what was piped to `<path>` through the same `file.write` as `>`: the same key, the same write roots (`denied file.write` before any call), the same plan line, the same confirm question, the same refusals, and a secret body is exit 2 `a secret cannot leave sash`. `write-atomic` and `--mode` are literal words, and so is the mode.

What it adds over `>`: when an earlier stage, or its `<` read, failed, nothing is sent. stderr says `fs write-atomic: not written`, the status is the failing stage's, and the file on the host is as it was.

```sash
# expect: host 1 fs write-atomic: not written
# nocall: file.write
cat /etc/nginx/nginx.conf | egrep zzz | fs write-atomic /srv/app/out
```

```sash
# expect: host 0
# call: file.write argv=> /srv/app/app.conf.new path=/srv/app/app.conf.new mode= body="listen 9090\n"
cat /srv/app/app.conf | lines |> sub line "8080" with "9090" | fs write-atomic /srv/app/app.conf.new
```

```sash
# expect: plan 0 plan: would write /srv/app/app.conf.new (12 bytes) (mode 0640)
# nocall: file.write
cat /srv/app/app.conf | fs write-atomic /srv/app/app.conf.new --mode 0640
```

Atomic here means one thing: the agent writes a temporary file beside the target and renames it over, so no reader ever sees a partial file, and a crash leaves the old file or the new one. It is not isolation: two writers of one path still race, and the last rename wins. It is not `atomic { }`: a later failure does not roll this write back, unless the write is inside an `atomic { }` block, where it is journalled and restored like any `file.write`. `--mode` is one of the agent's three modes and never loosens a file that exists; an agent too old for a mode refuses (`agent too old for file.write mode`, exit 2), and the write is not sent without it. `--needs` reports `file.write` and the path; lint's `par-same-target` and `retry-on-mutation` apply.

## Asking the person

`term prompt "<question>"` prints `script asks: <question>` on the terminal and gives the line the person types, without its Enter. `term confirm "<question>"` prints `script asks: <question> [y + Enter / N]`. sash's own questions (`confirm <key> ...?` under `set -o confirm`, `confirm off?`) never begin with `script asks:`, so a script's question cannot pass for one of them; its status is 0 for `y` or `Y` then Enter and 1 for anything else. Both take exactly one question word, at most 1024 bytes, and no input: `term` is the first stage of its pipeline. `prompt` and `confirm` are literal words: from a value they are exit 2 `command_from_value`.

A terminal can be asked only when sash's stdin and stderr are both terminals and the script is not on stdin: the prompt, `sash -c`, or `sash file`. `sash < script`, a pipe, a job, and a replay have none: `term` is then exit 2 `term needs a terminal` and the script stops there. Test `[ is_tty() ]` first in a script that may run unattended.

```sash
# expect: host 2 term needs a terminal
# nostdout: after
# lint: prompt-needs-terminal
let name = (term prompt "unit to check?")
echo after
```

```sash
# expect: host 0
# stdout: unattended
if [ is_tty() ] {
  term confirm "continue?" && echo yes
} else {
  echo unattended
}
```

An answer is text a person typed, and a person can be told what to type: treat it as you treat host data. It is a value, never a command word (`$ans status nginx` is exit 2 `command_from_value`), and never the word that selects what `unit`, `package`, or `sysctl` does: a value that carries an answer, directly, through another binding, an expression, or a `fn`'s parameter or return, is refused there before any call (`unit $ans nginx` is exit 2 `command_from_value: a term prompt answer selects no subcommand`; lint reports `prompt-subcommand`). This follows how values flow, not which branch an answer chose: a literal subcommand inside `if [ $ans == "restart" ] { ... }` runs. An answer can still be an argument, such as the unit to restart. A yes to `term confirm` sets only that statement's status: under `set -o confirm` the next mutating verb still asks its own question, and no answer changes plan mode or confirm mode. Lint warns `prompt-to-mutation` when an answer reaches a mutating stage.

```sash
# expect: host 2 term needs a terminal
# lint: prompt-to-mutation
# nocall: unit.restart
let unit = (term prompt "unit to restart?")
unit restart $unit
```

Before a question, everything the script has printed so far is written to the terminal, escaped as all output is, so the person sees it before answering; it is not printed again at the end. Standard error written inside a `try` or `retry` waits until that block ends, since a `try` may move it into its catch. A pipe or a file still gets the bytes unchanged. The question and the echo are escaped, so neither can drive the terminal. What was typed before the question is thrown away, and so is anything typed until the keyboard has been quiet for 200 ms after the question appears; if input never goes quiet for 5 s there is no answer (exit 2 `term needs a terminal`). A no at `term confirm` takes the rest of its line, through its Enter, with it. So typing does not answer a question it was not meant for. Backspace and Ctrl-U edit the line; escape sequences and control characters are dropped. An answer is at most 4096 bytes (`term prompt: answer over 4096 bytes`, exit 2) and must be text (`term prompt: answer is not text`, exit 2). Ctrl-D on an empty line or the end of input is status 1 `term prompt: end of input` with an empty value, and `retry` does not ask again. Enter on an empty line is the same: status 1 `term prompt: empty answer` with an empty value, so `set -e` stops there, `try` catches it, and `retry` does not ask again; an empty answer never reaches the next statement with status 0. Lint's `prompt-to-mutation` still warns where an answer, empty or not, reaches a mutating stage. Ctrl-C is status 130 `term: interrupted`; it stops the script and `try` does not catch it. A signal sent to sash while it asks (SIGTERM, SIGINT, SIGQUIT, SIGHUP, also under `--record`) puts the terminal mode back first; after SIGTSTP the terminal is back to normal while sash is stopped, and the question goes on when it is continued. Inside `timeout` the wait counts: the read ends at the deadline, exit 2 `timeout`.

`term` is refused in `par` (`parse: term inside par`; reached through a `fn`, exit 2 `term inside par`) and in `atomic { }` (exit 2 `atomic_irreversible: term`): a person at a prompt would let the transaction's deadline restore the block, so ask before the block. In `retry` it asks on every attempt; lint warns `prompt-in-retry`. Under plan mode it asks as in a real run.

```sash
# expect: parse term inside par
par 2 {
  term confirm "first?"
  echo second
}
```

```sash
# expect: host 2 atomic_irreversible: term
# nocall: atomic.begin
atomic {
  term confirm "write it?"
  echo x > /srv/app/flag
}
```

A recording keeps a plain answer in clear, as a `term` event; a replay answers from the recording and asks nobody. A plain `term prompt` echoes what is typed and is not for a secret.

To read a secret, write exactly `declare -s name = (term prompt -s "<question>")`. Nothing is echoed, not even a `*`, and the value is a secret from its first byte: every rule under Secrets applies. `-s` is legal only there, as the single stage of that value with no redirect: in a `let`, a bare statement, a pipeline with another stage, a condition, or with `term confirm` it is refused before anything runs. A recording stores only that a secret was asked for, with no value and no length, and a replay of such a recording is exit 2 `replay_secret <seq>`.

```sash
# expect: host 2 term needs a terminal
# nostdout: after
declare -s tok = (term prompt -s "token?")
echo after
```

```sash
# expect: parse term prompt -s: only as declare -s name = (term prompt -s "question")
let tok = (term prompt -s "token?")
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

`with` items, then a block: `set` flags (`set -e`, `set -o plan`), `loopmax N`, `verbmax N` (same ceiling as `set`), `prefix <dir>` (checked like `cd`), and `name = value`. Each is restored on every way out. `with set -f { }` and `with set -o noglob { }` are refused (`usage with: noglob is per line; use set -f`), `with noglob { }` is not an item (`parse: with item noglob`), a read-only name cannot be overridden, and `with env` is `parse: env not in this series`.

```sash
with set -e, loopmax 1000, prefix /var/log { ls }
```

```sash
# expect: empty 2 usage with: noglob is per line; use set -f
with set -f { echo x }
```

```sash
# expect: parse with item noglob
with noglob { echo x }
```

## `use`

`use <path>` reads a sash file through `file.read` and loads only its `fn` definitions and `declare -r name = value` lines. Anything else in the file is exit 2 `use: only fn and declare -r name = value`. The path is a literal; there is no `source` of a value and no `eval`. `--needs` does not follow it (note `use: not followed`).

## `set -o record`

`set -o record <path>` records every later statement (verb requests and responses, clock readings, sleeps, uuids) and writes the recording at the end through `file.write` with mode `0600`, through the agent. The path is a literal under a `file_write` root. `set +o record`, the end of the script, or logout ends it. Only bound secrets are redacted: a request or response is redacted when a `declare -s` read it, or when any of its fields contains a bound secret's text of four runes or more (a shorter secret only when a whole field equals it). A redacted response is replaced whole, keeping only its kind, exit, and length (`"redacted":true`), so a config file that holds a secret is missing from the recording entirely. Every other response is in the recording as it came, including a credential file read with a plain `cat`, `config read` output, and `ps` command lines. A recording past about 6 MiB is split into `<path>.1` .. `<path>.N` segments, each chained to the last. Refused in plan mode (`record: refused in plan mode (plan writes nothing)`), inside `with`, through `shopt`, in a `par` branch, and while one is open. Recordings are for test and debug: anyone can recompute the hash chain, so it proves consistency, not who made it. Writing one is a `file.write`, so plan mode refuses it and confirm asks before it.

## Offline record and replay

```text
sash --record out.jsonl --fixture fixture.json [--expires D] (-c script | file)
sash --replay rec.jsonl --fixture fixture.json (-c script | file)
```

`--record` runs the script against a fixture, a strict JSON file naming a grant (`entitlements` without `*`, clean absolute `file_read` and `file_write` roots, never `/`) and canned `results` (`key`, optional `argv`, `kind` `rows` with `cols` and `rows`, or `bytes`, `exit`, `error`). No host is reached; the only file written is the new recording (mode 0600, refused if it exists). `--expires` is 1m to 365 days, default 7 days.

`--replay` takes the fixture's grant only and answers every call from the recording. A `par` with channels replays its channel ops in the recorded order, each value checked against its recorded hash (a secret's is redacted and not checked). Any difference is exit 2: `replay_diverged <seq>`, `replay_other_script`, `replay_expired`, `replay_bad_chain <seq>`, `replay_bad_segment <n>`, `replay_truncated <seq>`. A recording never authorizes anything.

## `why`

Interactive only. `why <line>` explains the grant check for that line and runs nothing: one row per stage and redirect, `command`, `key`, `granted`, `reason`, `fix`. Reasons include `granted`, `in_process`, `no_session`, `not_in_grant`, `outside_roots`, `prefix_unset`, `not_grantable`, `unknown`, `usage`, `value`, `value_path`. `fix` names the policy grant or profile that adds a missing key. It is display text; the agent still re-checks.

## Abbreviations

Interactive only: `abbr name 'text'` defines one, `abbr -e name` erases it, `abbr` lists the names. Space or Enter after the name in command position replaces it on the visible line, and Enter stops after expanding so the person sees the verb. The text is plain words (no `$`, `[ ]`, quotes, `;`, `|`, `&`, `#`, `<`, `>`), at most 256 bytes. Session only: no rc file, never in a script (exit 127), never seen by `--needs` or `--lint`. There are no aliases.

## wasm

`wasm m = <literal path> sha256 <64 lowercase hex>` loads a WebAssembly module through `file.read` and checks its hash; `[ m::fn(args) ]` calls an export as pure computation (JSON in, JSON out, no imports, no clock, no filesystem, a 1 s deadline, counted against `verbmax`). A secret argument is refused (`wasm_secret`). It needs a build with the `sashwasm` tag.

The shipped release binary is built without the `sashwasm` tag. There, a `wasm` statement or a module call exits 2 `wasm_unsupported` and reads nothing. Do not write `wasm` for a release host.

```sash
# expect: empty 2 wasm_unsupported
wasm m = /srv/ops/fmt.wasm sha256 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
```
