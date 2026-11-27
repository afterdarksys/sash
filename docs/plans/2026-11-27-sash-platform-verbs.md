# sash platform verbs: `term`, `fs`, `sash connect`, `fetch`, `tls inspect`, codes

Status: build contract, written 2026-11-27 on `feat/platform-verbs`. Nothing built. Ryan's six rulings of 2026-11-27 are decisions here. Each "Open question" (§12) blocks only the phase it names. Paths are relative to this repo unless prefixed: `agent/` is `systemapi.io/systemapi-agent/`, `cp/` is `systemapi.io/` (read at branch `feat/agent-frontier-3`), `frontier` is `sah.system/docs/plans/2026-11-26-agent-frontier-contract.md`.

Every earlier rule holds: no PATH exec, no bash fallback, no `eval`, no alias, no `&`, command words literal (`command_from_value`, `internal/lang/eval.go:1401`), verbs only through `machine.do` (`eval.go:381`) and the agent re-check (`agent/sash_verb.go:151`), no adssh import, no lease minted by a session, nothing logs a path, a body or a command line (`TODO_CODEX_OR_CLAUDE.md`). Security code reads `ads-fable-utils/SECURITY-RULES.md` first.

## 0. Phases

| Phase | Feature | Repos touched | Why this phase |
| --- | --- | --- | --- |
| 1 | `term prompt`, `term confirm`, `term prompt -s`, `[ is_tty() ]` (§1) | sash | In process. |
| 1 | Terminal escaping of byte output (§2) | sash | In process. |
| 1 | `sash version --json`, `sash codes --json` (§3) | sash | In process. |
| 1 | Login-shell behaviour (§4) | sash | In process. |
| 1 | `sash connect <host>` (§5) | sash | Verified: every route and frame it needs exists (`cp/controlplane/router.js:1610`, `:1817`; `cp/playground/fleet.js:594-625`, `:1087-1127`). No server change. |
| 1 | `fs write-atomic <path>` (§6.1) | sash | Verified: the agent's `file.write` is already temp + fsync + rename (`agent/file_helper_linux.go:14-17`, `:786`, `:931`, `:1007`, `:1015`), and has been since sash writes landed (`8cb41bee`). No agent change, so no negotiation. |
| 2 | `fs tempfile`, `fs rmtemp` (§6.2, §9.1) | sash, agent | Exclusive create and removal are not reachable through the sash door today (`agent/sash_file_linux.go:90` fixes `overwrite := true`; the closed table has no remove). |
| 2 | `tls inspect` (§7, §9.2, §10.1) | sash, agent, control plane | New key, new target list, outbound connection from the host. |
| 3 | `fetch` (§8, §9.3, §10.2) | sash, agent, control plane, sahxfer | Needs transfer work that does not exist (§13 item 1). |

Order inside Phase 1: §2 (escaping) first, since §1 and §5 print through it; then §1, §3, §4, §6.1, §5.

New closed-table rows (`internal/lang/builtin.go:30`, and `skills/sash/reference/verbs.md`, which a test holds equal to the table):

| form | kind | key | mutates | phase |
| --- | --- | --- | --- | --- |
| `term prompt` | builtin | - | no | 1 |
| `term confirm` | builtin | - | no | 1 |
| `is_tty()` | function | - | no | 1 |
| `fs write-atomic` | verb | file.write | yes | 1 |
| `fs tempfile` | verb | file.write | yes | 2 |
| `fs rmtemp` | verb | file.write | yes | 2 |
| `tls inspect` | verb | net.tls_inspect | no | 2 |
| `fetch` | verb | file.fetch | yes | 3 |

`term`, `fs`, `tls` and `fetch` become reserved command words: a `fn` may not take the name (existing rule, design doc "Language"), and the second word (`prompt`, `write-atomic`, ...) and every flag must be literals. A second word or a flag from a value is exit 2 `command_from_value`, as `unit $op` is not: here the sub-word selects behaviour that the parser must see.

Exit codes are sash's: 0; 1 a failed read or check; 2 usage, type, refused by rule; 126 denied or declined; 127 unknown name; 129 hangup; 130 interrupt.

## 1. Terminal helpers (Phase 1)

### 1.1 Syntax and values

| Form | Does | Value | Status |
| --- | --- | --- | --- |
| `term prompt "<question>"` | Prints the question and a space on the terminal, reads one line. | The line as text, without its Enter. | 0 (an empty line is `""`, status 0) |
| `term confirm "<question>"` | Prints `<question> [y + Enter / N]`, reads the answer. | none | 0 for `y` or `Y` then Enter; 1 for anything else |
| `declare -s name = (term prompt -s "<question>")` | As `term prompt`, nothing echoed. | A secret (`secretVal`), bound to `name`. | 0 |
| `[ is_tty() ]` | No arguments. | `true` when a terminal can be asked (§1.2), else `false`. | - |

Exactly one question word. It is expanded like any word, then capped at 1024 bytes (exit 2 `usage term: question over 1024 bytes`) and printed through `TermText` (§2), so a question built from host data cannot drive the terminal. `term` takes no input: as a non-first pipeline stage it is exit 2 `usage term`.

### 1.2 The terminal

A terminal exists when sash's stdin and stderr are both terminals (`line.IsTerm`, `internal/line/term_linux.go:33`, `term_darwin.go:33`) and stdin is not the script. Two callers provide it through one new field `lang.Options.Term` (beside `Confirm`, `eval.go:90-94`):

- the editor (`line.Loop`, `internal/line/editor.go:1282`), already in raw mode;
- script mode in `cmd/sash/main.go` when the source is `-c` or a file and both fds are terminals: the asker enters raw mode for one question (`enterRaw`, `term_linux.go:11`) and restores it on every way out, including SIGTERM, SIGINT, SIGQUIT (each then ends sash as the signal says) and SIGHUP (the run's hangup path, under `--record` too); on SIGTSTP it restores, stops, and enters raw mode again when continued, and a failure to re-enter fails the question closed (`askEnds`, `watchStop`, `internal/line/signal_unix.go`).

`sash < script`, a pipe, a job, `--replay`: `Options.Term` is nil. Then `term prompt` and `term confirm` are exit 2 `term needs a terminal` and stop the script (`ctlHalt`, as `confirm needs a terminal` does, `eval.go:530-533`); `is_tty()` is `false`. `Options.Confirm` is untouched: `set -o confirm` in a script is still `confirm needs a terminal` (`eval.go:433-435`), terminal or not.

### 1.3 Reading the answer

| Event | `term prompt` | `term confirm` |
| --- | --- | --- |
| Before the question | Input queue flushed (`flushInput`, `term_darwin.go:55`, linux twin) and read-ahead discarded, so typeahead never answers. A failed flush is the "no terminal" error. After the question, armed only after 200 ms of silence (`waitQuiet`, shared with `askConfirm`); input that is never quiet for 5 s is the "no terminal" error. | The whole `askConfirm` discipline (`editor.go:1446-1527`): flush, bracketed paste on, armed only after 200 ms of silence, `y`/`Y`, second flush, Enter. Reuse the function; do not copy it. |
| Enter (CR or LF) | Ends the line. On an empty line: status 1, `term prompt: empty answer`, empty value, as Ctrl-D. | As `askConfirm`; after a no key the rest of the line is read through its Enter (at most 1 s), so it cannot answer the next question. |
| Backspace (0x7f, 0x08), Ctrl-U | Delete one rune, clear the line. | - |
| ESC | It and its sequence are dropped (CSI: through the final byte 0x40-0x7E; else ESC and one byte). | no |
| Any other byte below 0x20, 0x7f, C1 | Dropped, not stored. | no |
| Echo | Each stored rune through `TermText`; `-s` echoes nothing, not `*`. | Key echoed escaped (existing). |
| Length | 4096 bytes. One more byte: input is discarded up to Enter, exit 2 `term prompt: answer over 4096 bytes`, no value. | - |
| Not UTF-8 at Enter | exit 2 `term prompt: answer is not text`. | - |
| Ctrl-D on an empty line, EOF | status 1, `term prompt: end of input`, empty value. `try` catches; `retry` does not repeat it. | status 1 |
| Ctrl-C | status 130, `term: interrupted`, stops the line or script (`ctlHalt`). `try` does not catch. | same |
| SIGHUP | The existing hangup path (`State.HangUp`, `cmd/sash/main.go:134-151`): the read ends, status 129. | same |
| Deadline (`timeout`, `atomic` is refused below) | The read ends at the deadline: exit 2 `timeout` (existing word). Waiting counts against `timeout`. | same |

Before any question (and before a `set -o confirm` question), `Options.Show` is handed the run's stdout and stderr so far (stderr only up to the first `try` or `retry` still running): `cmd/sash/main.go` writes it through `emit`, the prompt through `termBytes`, and the Outcome no longer holds it.

### 1.4 What an answer can never do

| Rule | How it is enforced |
| --- | --- |
| It is never the answer to `set -o confirm`. | `term confirm` sets only its own status. `machine.do` still asks before every mutating verb (`eval.go:432-455`); `m.declined` and `State.confirm` are not written by `term`. |
| It cannot turn a safety mode off. | `set +o confirm` stays `confirm is locked` (`internal/lang/state.go:141-144`, `:373-374`); `set +o plan` stays locked under `--plan`; the editor's `confirm off` line asks its own question (`editor.go:1020-1032`). No `term` path calls `ConfirmOff` or `LockPlan`. |
| It never becomes a command word. | It is a value: `$ans status nginx` is exit 2 `command_from_value` (existing). `term` adds no `eval`, no `source`, no re-parse. |
| It is escaped when shown. | Echo, `echo $ans` at a terminal, errors and cells go through `TermText` or `needsCell` (`internal/lang/needs.go:968`). |
| It is untrusted text. | It gets a provenance origin `term prompt` (no hash) so `origin` names it; lint tracks it (§1.6). |

### 1.5 `-s` is only a `declare -s` binding

- **Parser.** A stage whose literal words begin `term prompt -s` is legal only as the single stage, with no redirect, of the value pipeline of a `Declare` whose `Flags` contain `s` (`internal/lang/parse.go:90-94`). Anywhere else (a `let`, a bare statement, a pipeline with another stage, a condition, a `fn` argument) is a parse error: `term prompt -s: only as declare -s name = (term prompt -s "question")`. `-s` after the question, twice, or with `term confirm` is `usage term`.
- **Evaluator.** `runDeclare` already marks the value pipe secret before it runs (`m.secretPipe`, `eval.go:781-784`); the `term prompt -s` stage refuses to read unless `m.secretPipe` is set and the pipeline has one stage (same message, exit 2), and returns `secretVal`. Every existing secret rule then applies (`skills/sash/reference/features.md` "Secrets").
- **History.** The editor already keeps no `declare -s` line; the answer was never on the line.

### 1.6 Modes

| Mode | Behaviour |
| --- | --- |
| Plan (`--plan`, `set -o plan`) | Asks as in a real run: plan reads are real, and the answer decides what the plan prints. No terminal is the same exit 2. |
| `set -o confirm` | Independent. A `term confirm` yes is followed by confirm's own question at the next mutating verb. |
| `par` | Refused. Literal `term` in a `par` body is a parse error `term inside par`; reached through a `fn` from a branch (`m.pb != nil`) it is exit 2 `term inside par`. |
| `atomic { }` | Refused statically like `use` (`atomicRefusal`, `internal/lang/atomic.go:52-99`): exit 2 `atomic_irreversible: term`. A person at a prompt would let the agent's transaction deadline restore the block. Ask before the block. |
| `retry` | Asked again on each attempt. EOF and Ctrl-C are not retried. |
| Recording (`set -o record`, `--record`) | A new tape event kind `term` beside `now`, `sleep`, `uuid` (`internal/lang/record.go:8`, `:422`, `:854`): `{op, value}`. A plain answer is recorded in clear. A `-s` answer is recorded as `{"op":"prompt-s","redacted":true}` with no value and no length. `is_tty()` is a `tty` event. The question is in the script, not the event. |
| Replay | Answers come from the tape; no terminal is needed or touched. A `prompt-s` event cannot be replayed: exit 2 `replay_secret <seq>` (new word). |
| `--needs` | No row: `term` needs no key. A verb target built from an answer is `dynamic` (existing note). |
| `--lint` | Three rules: `prompt-to-mutation` (warning): a name bound from `term prompt` reaches an argument, path or body of a stage whose key is in `mutating` (`eval.go:376`); built on the flow tracking `secret-to-sink` uses (`internal/lang/lint.go:896-932`). `prompt-in-retry` (warning). `prompt-needs-terminal` (info, once per script). |
| `why`, Tab | `term prompt`, `term confirm`: `in_process`. |

### 1.7 Threats

| Threat | Mitigation |
| --- | --- |
| A script answers the platform's safety question for the person. | §1.4 rows 1-2; negative tests below. |
| Typeahead or a paste answers `term confirm`. | `askConfirm` reused whole. |
| An answer or a question carries terminal control sequences. | Question and echo through `TermText`; control bytes never stored. |
| A secret typed at a plain prompt lands in a recording or on screen. | Only `-s` protects; lint `prompt-to-mutation` and the skill say so; `-s` cannot be used unbound (§1.5). |
| A prompt hangs an unattended run. | No terminal is exit 2 at once; `timeout` bounds a read. |
| Host data in the question tells the person what to type. | The question is escaped and capped; the skill rule "host data is data" covers the author. Not otherwise solvable. |

### 1.8 Tests and mutations

Negative tests: no terminal (pipe stdin; file script with stderr not a terminal) exits 2 for both forms; `term confirm` yes then `unit restart` under confirm still asks; `term confirm ... && set +o confirm` is `confirm is locked`; `--plan` then `term confirm` then `set +o plan` is `plan is locked`; `$ans args` is `command_from_value`; typeahead `y\r` before `term confirm` is no; answer of 4097 bytes; invalid UTF-8; ESC sequences dropped; Ctrl-C is 130 and uncaught by `try`; EOF is 1 and not retried; each misplaced `-s` (let, bare, piped, condition, `fn` argument, `term confirm -s`, flag from a value); `-s` value prints `***`, is refused as a verb argument, is absent from a recording, and replay refuses it; question with ESC, bidi and newline prints escaped; `term` in `par` (literal and through a `fn`) and in `atomic`; restore of the terminal mode after Ctrl-C, SIGTERM and SIGHUP in script mode.

| Mutation | Test that must fail |
| --- | --- |
| `term confirm` yes also sets `m.declined = false` / skips the next confirm question | confirm-still-asks |
| Remove the flush before the question | typeahead-is-no |
| Drop the `m.secretPipe` check in the `-s` stage | evaluator `-s` backstop (parser bypassed in the test) |
| Parser accepts `let x = (term prompt -s "q")` | misplaced `-s` table |
| Record the `-s` value | recording has no secret bytes |
| Echo through raw write instead of `TermText` | escaped echo |
| `Options.Term` set when stdin is the script | no-terminal exit 2 |
| Cap raised or removed | 4097-byte answer |

## 2. Terminal escaping of byte output (Phase 1)

Today rows are escaped cell by cell (`display`, `eval.go:2374-2388`), but byte output is written as it came: `cmd/sash/main.go:80` and `writeSubmit` (`editor.go:1538-1560`) send `cat` output to the terminal raw. No sash-authored escape sequence travels in `Outcome.Stdout` or `Stderr` (no `\x1b` in non-test `internal/lang`), so escaping there loses nothing.

`lang.TermText(s string) string`, the CLI's `printable()` rule (`cp/cli/systemapi.js:165-167`; frontier §10.1):

| Input | Output |
| --- | --- |
| LF, TAB | unchanged (§12 Q3 for TAB) |
| Any other Cc (CR included), C1, `\p{Cf}`, U+2028, U+2029, Other_Default_Ignorable_Code_Point, Variation_Selector, U+2800, a fifth or later `\p{Mn}`/`\p{Me}` on one base (`\p{Mc}` neither counts nor resets), anything `unicode.IsPrint` rejects | `<U+XXXX>`, upper-case hex, at least four digits |
| A byte that is not UTF-8 | `<0xHH>` |
| The literal text `<U+` or `<0x` | `<U+003C>U+`, `<U+003C>0x`, so output decodes one way |

Applied exactly at: the script-mode writes in `main.go` (stdout and stderr judged separately with `line.IsTerm`), `writeSubmit`, the `log` builtin's line, the recording-finish text (`editor.go:1309-1313`), and everything §5 prints. Not applied when the fd is not a terminal: a pipe or a file receives the bytes unchanged. Values, `>` bodies and recordings are never changed. `needsCell` stays as is for cells and messages (§12 Q3).

Threats: a file body, journal line or process name that repaints the screen, sets the title, or answers a terminal query (escaped at the last write, on the host side, before it reaches any PTY); bidi and zero-width text that hides part of a line (Cf escaped); a writer forging an escape by writing `<U+001B>` literally (the `<U+` rule).

Tests: ESC, CSI, OSC 52, BEL, CR, C1 0x9b, U+202E, U+200B, U+2028, invalid UTF-8, a literal `<U+001B>`; the same bytes to a pipe arrive unchanged (byte-for-byte golden); rows unchanged. Mutations: escape skipped for stderr; CR passed; `<U+` rule dropped; escaping applied to a pipe: each fails one of these.

## 3. `sash version --json`, `sash codes --json` (Phase 1)

Frontier §9.2 records that sash has neither. Both are argv modes in `cmd/sash/main.go` beside `--skill` (`main.go:36-38`), recognised only as exactly two arguments (`version --json`, `codes --json`). Today those are usage errors (`readSource`, `main.go:155-173`), so no script name is taken. Any other argument with them is exit 2 with the usage line. They read no grant, call no parent, open no file.

`sash version --json`, one line, one object:

```json
{"v":1,"ok":true,"code":"ok","name":"sash","version":"<Main.Version>","commit":"<vcs.revision|unknown>","modified":false,"os":"linux","arch":"amd64","grant":"sash/1","mux":1,"wasm":false,"forms":<n>,"codes":<n>,"codes_sha256":"<hex>"}
```

`version`, `commit`, `modified` come from `debug.ReadBuildInfo`; `build.sh` passes no version today and need not. `forms` is `len(builtins)`.

`sash codes --json`: `{"v":1,"ok":true,"code":"ok","name":"sash","version":"...","codes":[{"code","exit","area","meaning","fix"}]}`, sorted by `code`. The catalog is one Go table, `internal/codes`. Areas: `parse`, `grant`, `verb`, `file`, `atomic`, `secret`, `term`, `replay`, `connect`, `tls`, `fetch`, `session`.

- `code` is the first word(s) sash prints for a refusal, in catalog style: `denied`, `declined`, `command_from_value`, `prefix_unset`, `atomic_irreversible`, `replay_diverged`, `grant_bad_sig`, `term_no_terminal`, ... Messages printed today do not change; the catalog names them. Where today's message is a sentence (`confirm needs a terminal`), the catalog `code` is a token and `meaning` quotes the message.
- A test scans the fixed messages of `internal/lang`, `internal/grant`, `internal/session`, `internal/proto` and `internal/connect` and fails when one has no entry or its `exit` differs; a second pins every sentence in a reviewed fixture (frontier §16, "the gate is the reviewed text").
- **Fix-text rule** (frontier §6): every `fix` sentence that names a change has "a person", "the operator" or "an admin" as subject or decider; none says to turn a mode off, widen a grant, use another credential or channel, or names an override field. Each string is at most 512 bytes. A lint test enforces the banned phrases of frontier §6.

Threats: catalog text read by an agent as an instruction (fix-text rule, pinned fixture); a catalog that drifts from the binary (scan test); version output used to gate anything (it is a claim; frontier §9.2 keeps it display-only).

Mutations: remove a code from the table (scan test); change a `fix` to "turn off confirm" (lint and fixture); accept `sash version --json x` (usage test).

## 4. Login shell (Phase 1)

What already happens: argv[0] is not read; no arguments and a terminal on stdin starts the prompt with an empty grant (`main.go:48`, `:66-72`), where verbs are denied and filters run. `sshd` running `$SHELL -c "<cmd>"` is the `-c` path.

Login-shell mode adds only:

| Addition | Detail |
| --- | --- |
| `-l` and `--login` | Accepted as a leading flag and ignored, with or without `-c`. |
| argv[0] beginning `-` | Treated as `sash`. No other effect. |
| One notice on a hand start at a terminal | stderr: `sash: no session grant: verbs are denied, filters run`. Not printed under `--session` or for a script. |
| Docs | A person with root adds the binary's path to `/etc/shells` and runs `chsh`. sash never edits either. |

It must never: read `/etc/profile`, `~/.profile`, an rc file or `ENV`; import environment variables as names; exec anything (so `scp`, `sftp`, `rsync` and `git` over ssh into such an account are exit 127 `unknown`, by design); fall through to another shell on any error; open a network connection or read the token file (only `sash connect` does, and only when a person types it as argv); ask the agent for a grant or a lease; behave differently as root. On a fleet host an ssh login into an account whose shell is sash is a hand start: empty grant, no agent socket.

This is not the blocked "Darwin and Windows login" item: that is the agent's `sashLogin` for `pty` on non-Linux (`TODO_CODEX_OR_CLAUDE.md`), which stays false.

Tests: argv[0] `-sash` with a pty starts the prompt; `-l -c 'echo a'`; a `~/.profile` and `ENV` file that would write a marker are never read (strace-free: the test HOME holds them, and the marker never appears); `sash -c 'scp -t /tmp'` is 127; `SHELL=/bin/bash` in the environment changes nothing. Mutation: reading `$HOME/.sashrc`; falling back to `/bin/sh` on a parse error: each fails a test.

## 5. `sash connect <host>` (Phase 1)

Client mode ("sah/3"). It runs on a person's workstation and is the client half of the existing terminal protocol (`cp/docs/sahconnect-terminal-protocol.md` §1-§4), which already allows a non-browser client (`:73-76`). The host's agent still verifies the signed grant and execs the host's sash (`agent/sash_grant.go:17-23`); this client holds no grant, no key, and never speaks to an agent.

```text
sash connect [--root] <host>
```

`connect` is an argv mode, not a language word: no script, line, `fn` or session can reach it, so a host-side session cannot use it to call the control plane. It refuses `--session`, `--plan`, `--needs`, `--lint`, `--record`, `--replay`, `-c`, and any further argument (exit 2 usage). Non-interactive use is out: stdin, stdout and stderr must all be terminals, else exit 2 `connect needs a terminal`. There is no `--script`: scripts go through the existing jobs path (`cp/cli/systemapi.js:13-14`, `jobs submit`), which has its own gate, lease and audit.

### 5.1 Configuration and credential

| Item | Rule |
| --- | --- |
| `SYSTEMAPI_URL` | Required. `https` only, no userinfo, no query (stricter than the CLI's `controlUrl`, `cli/systemapi.js:271-285`, which allows `http`). Else exit 2 `connect: invalid SYSTEMAPI_URL`. |
| `SYSTEMAPI_TOKEN_FILE` | Required, absolute: the file `login` writes (`cli/systemapi.js:516-534`, `:578`). Read as the CLI reads it (`:1088-1104`): `O_RDONLY\|O_NOFOLLOW\|O_NONBLOCK`, a regular file, at most 4096 bytes, no group or other bits, and (added) owned by the effective uid. Content trimmed, 16 to 512 bytes, the CLI's `KEY_RE` (`:304`). Any failure: exit 2 `connect: token_file_rejected`. |
| `SYSTEMAPI_API_KEY` | Never read. No flag takes a credential. |
| The token | Sent only as `Authorization: Bearer` (`cp/controlplane/router.js:284`) on the two HTTPS calls. Never in argv, a URL, an environment variable set by sash, a log, or any printed line: before printing any server string, the token and the ticket are replaced by `<credential>`. Never sent on the WebSocket upgrade. |

sash does not perform the device login. With no usable token: exit 2 `connect: no login; a person logs in with the systemapi CLI`.

### 5.2 Calls, in order

| # | Call | Cite | Client checks |
| --- | --- | --- | --- |
| 1 | `GET /api/v1/hosts/resolve?name=<host>` | `router.js:1817-1822`; protocol doc §1 | `<host>` matches `[A-Za-z0-9._-]{1,253}` before any request. 200 gives `host_id`. |
| 2 | `POST /api/v1/ws-ticket {"host_id","purpose"}`; purpose `terminal:open`, or `terminal:root` with `--root` | `router.js:1610-1641`; issue at `:1191`; 60 s single use (`controlplane/tickets.js:41`, `:333`) | `ticket` matches `^stk_[A-Za-z0-9_-]{8,508}$` (`httpauth.js:35`, `cli/systemapi.js:2389`). |
| 3 | `wss://<origin>/api/v1/systems/terminal/<host_id>?ticket=&purpose=&rows=&cols=` | `fleet.js:511-512`, `:594-625`; ticket from the query (`httpauth.js:29-39`) | No `Origin` header (accepted, `httpauth.js:118-119`). No credential header. No `api_key` parameter. `rows`, `cols` 1 to 1000 (`fleet.js:287-293`). |

HTTP: 20 s timeout, body capped at 64 KiB, `Accept: application/json`, no cookie jar, no proxy from the environment, and **no redirect is followed** (`CheckRedirect` returns `http.ErrUseLastResponse`; a 3xx is `request_failed`), the CLI's rule (`cli/systemapi.js:424`). Unknown JSON fields are ignored (frontier §6 compatibility); a missing or mistyped needed field is `request_failed`.

TLS: system roots only, `MinVersion: tls.VersionTLS13` (`cp/docs/sahconnect-connect-design.md:115`; SECURITY-RULES rule 5), `ServerName` from the URL. There is no skip-verify flag, environment variable or build tag, no custom CA option, and `InsecureSkipVerify` does not appear in the package (a test greps for it). Tests inject a root pool through an unexported hook.

### 5.3 Framing: "sah/3" is the existing protocol, unchanged

No new frame. `sah/3` names this client's use of protocol doc §3:

| Direction | Frame | Cite |
| --- | --- | --- |
| client → server, text | `{"type":"pty_input","data_b64":"..."}`, `{"type":"pty_resize","rows":R,"cols":C}`, `{"type":"pty_eof"}`; each at most 64 KiB (`PTY_FRAME_MAX`) | `fleet.js:1087-1127`, `:137` |
| server → client, binary | raw PTY bytes | `fleet.js:1419` |
| server → client, text | `pty_status` (`starting`, `started`, or `error` then close 1011), `pty_exit` (`code`, `reason`, then close 1000) | `fleet.js:627-630`, `:807`, `:1149-1151`, `:1466` |

Version string: the client sends `User-Agent: sash-connect/<version> sah/3` on all three calls. It offers no `Sec-WebSocket-Protocol`: the terminal server is built without `handleProtocols` (`fleet.js:382`), so an echoed subprotocol would prove nothing (*verify against the pinned `ws` 8 behaviour before relying on this sentence*). Negotiation is by behaviour, and the refusal is fixed: the upgrade must answer 101, and the first text frame must be a `pty_status` whose payload has `status` `starting` or `started`, or `error`. Anything else (another HTTP status, a first text frame that is not that JSON, an unknown `type` before `started`) is exit 1 `connect: the server does not speak sah/3`. After `started`, an unknown text frame type is ignored and counted.

The WebSocket client is new code (§12 Q4): RFC 6455 client role only, masked frames from `crypto/rand`, text, binary, continuation, ping answered by pong, close; an inbound frame over 1 MiB or an unmasked-rule violation ends the session with `protocol`.

### 5.4 Local terminal

| Matter | Rule |
| --- | --- |
| Raw mode | Entered only after `started` (`enterRaw`); restored exactly once on every exit by a `termGuard` (`editor.go:1360-1380`) and by `watchSignals` on SIGTERM and SIGHUP. A failed restore prints `sash: terminal not restored` (`signal_unix.go:33-38`). |
| Window size | Initial `rows`, `cols` from a new `termSize(fd)` (`TIOCGWINSZ`; today's `termWidth` returns columns only, `term_linux.go:45`); unknown is 24 by 80. SIGWINCH sends one `pty_resize`, coalesced to at most 10 a second. |
| Input | Bytes read from stdin go out as `pty_input`, at most 16 KiB of raw bytes per frame. EOF on stdin sends `pty_eof`. |
| Ctrl-C | Before `started`: local, exit 130, socket closed. After: raw mode has `ISIG` off, so 0x03 is input for the host's sash. |
| Leaving a stuck session | After Enter, `~.` closes locally (exit 1 `connect: closed`); `~~` sends one `~`. |
| Hangup (SIGHUP), SIGTERM | Close the socket (the server then stops the PTY, protocol doc §4), restore, end as the signal says (129, 143). |
| PTY output | The host's own terminal stream, which must carry the host sash's cursor sequences, so it cannot be `<U+XXXX>`-escaped (§13 item 3). It passes through a filter (§12 Q5): printable text, LF, CR, TAB, BS, and CSI sequences, except those that make the local terminal answer or act outside the screen (DSR `n`, DA `c`, window ops `t`, DECRQM `$p`); OSC, DCS, APC, PM and SOS strings are dropped whole. Host data inside that stream was already escaped by the host's sash (§2). |
| Everything else printed | Only fixed client text, plus server strings reduced as §5.5 and written through `TermText`. |
| Recording | The client records nothing and offers no option. The host's agent records the session as it does for any terminal (protocol doc §5); `recording_unavailable` is the host's refusal. The host session's own `set -o record` is unaffected. |

### 5.5 What is printed, and exit codes

Server-chosen strings are never shown as prose. A code is shown only if it matches `^[a-z0-9_]{1,64}$`, then looked up in the **bundled** `connect` area of `internal/codes`; the server's `message`, `fix` or any other text is never printed, relayed or acted on. A code not in the bundle prints as `<code> (not in this sash's list)`; a non-matching one as `unlisted`. Hostname and `host_id` print through `TermText`, cut at 253 bytes.

Bundled codes: the `PTY_ERRORS` set (`fleet.js:216-221`): `ticket_invalid`, `scope_denied`, `agent_offline`, `lease_required`, `pty_user_unset`, `account_unmapped`, `recording_unavailable`, `root_unavailable`, `pty_start_failed`, `internal`, `grant_missing`, `grant_bad_shape`, `grant_star`, `grant_bad_sig`, `grant_expired`, `grant_purpose`, `grant_binding`, `sash_missing`; the HTTP refusals `invalid_name`, `not_found`, `ambiguous`, `bad_purpose`, `rate_limited`, `tickets_unavailable`, `approval_required` (`router.js:1224`), `forbidden`, `token_expired`, `fresh_token_expired`, `fresh_expired`; and the `pty_exit` reasons `exited`, `max_duration`, `agent_disconnected`, `stopped`. A test derives the first set from `fleet.js` when the control-plane checkout is present.

| Outcome | Printed (stderr) | Exit |
| --- | --- | --- |
| Usage, environment, token file, not a terminal | the fixed line | 2 |
| Network, TLS, timeout, redirect, malformed answer | `connect: request_failed` | 1 |
| HTTP refusal at step 1 or 2; `pty_status` error | `connect refused: <code>: <bundled meaning>` | 126 |
| Not sah/3 | `connect: the server does not speak sah/3` | 1 |
| `pty_exit` with code 0 to 255 | nothing | that code |
| `pty_exit` with code -1 | `session ended: <reason>: <bundled meaning>` | 1 |
| Socket closed with no `pty_exit` | `session ended: connection lost` | 1 |

Fresh login and step-up. The client never chooses a weaker path. `--root` asks for `terminal:root` and nothing else; a refusal is printed and the client stops. It never retries as `terminal:open`, never suggests another purpose, key, host or flag. Bundled sentences, exactly: `token_expired`: "your login expired; a person logs in again with the systemapi CLI". `fresh_token_expired` and `fresh_expired`: "a fresh login lasts 5 minutes; a person makes a new fresh login with the systemapi CLI just before opening a root session" (the control plane's own rule, frontier §10.2). `approval_required`: "a root session on this host needs an approval; an approver decides". `scope_denied`, `forbidden`: "this login may not open that session; a tenant admin decides who may". A root session cut off at a renew re-check (`fleet.js:961`, `:1008`, every 20 s, `:184`) arrives as one of the frames above and prints the same way.

### 5.6 Threats

| Threat | Mitigation |
| --- | --- |
| Token theft through argv, environment, URL, redirect, log, error text | §5.1; no redirect; credential replaced in every printed string; https only. |
| A token file another user can read or swap | Mode, owner, `O_NOFOLLOW`, regular file. |
| MITM | TLS 1.3, system roots, no bypass of any kind. |
| A hostile control plane or host writes instructions or terminal sequences into "error text" | Only bundled meanings print; codes by regex; `TermText`. |
| A hostile host drives the workstation terminal through PTY bytes (clipboard, title, query replies typed back as input) | The stream filter; residual: screen content is the host's (as with any remote shell). |
| Downgrade from root to a weaker or unrecorded path | No retry with another purpose; `recording_unavailable` is final. |
| The ticket leaks through the URL | It is single use, 60 s, bound to host and purpose (`tickets.js:2-3`); never printed. |
| A host session reaching the control plane with a person's credential | `connect` is argv-only and refused with `--session`; there is no language word. |
| Terminal left raw | Guard and signal watch; test kills the process with SIGTERM under a pty. |

### 5.7 Tests and mutations

Against an in-repo fake control plane (TLS, `httptest`): the three calls with exact paths, headers and frames; token never on the upgrade or in a URL; `http://` URL refused; token file as a symlink, a FIFO, mode 0640, another owner, 5000 bytes, 8 bytes; a 302 at each step is `request_failed` and the token never reaches the redirect target; bad certificate, TLS 1.2-only server; first frame not `pty_status`; error code `evil\x1b[2J`, a 300-byte code, an unlisted code; a `fix` field with text (never printed); `pty_exit` 0, 3, -1; resize; `~.`; Ctrl-C before start; SIGTERM restores the terminal; stdin not a terminal; `--root` refused never retries (the fake counts ticket requests); PTY output with OSC 52, DSR and DCS is filtered, CSI cursor moves pass; frame fuzz for the WebSocket reader and the filter.

| Mutation | Test that must fail |
| --- | --- |
| Follow redirects | redirect test |
| Add the bearer header to the upgrade | upgrade-headers test |
| Print the server's `fix` or `message` | fix-never-printed |
| Retry `terminal:open` after a root refusal | ticket-count test |
| `MinVersion` 1.2, or any `InsecureSkipVerify` | TLS 1.2 server test; source grep |
| Skip the owner or mode check | token file table |
| Pass OSC through the filter | OSC 52 test |
| Accept a non-terminal stdin | needs-a-terminal |

## 6. File helpers

Decision: both use the existing key `file.write`, not a new key. `>` is `file.write` (`eval.go:2000-2018`), `atomic.*` rides on it because it "needs exactly what a file.write needs" (`agent/sash_verb.go:158-166`), and nothing here writes outside the roots a `>` under the same grant already reaches. Reusing the key keeps one line in plan and confirm (`mutating`, `eval.go:376`), one policy flag (`--file-write <dir>`), the root-session pin (`sashPinDenied`, `sash_verb.go:125-137`), and the mux class limit and per-path lock (`agent/sash_mux.go:215`, `:230`). A second key would be the same power under another name.

### 6.1 `fs write-atomic <path>` (Phase 1)

```sash
cat /srv/app/app.conf | sub line "8080" with "9090" | fs write-atomic /srv/app/app.conf.new
```

`fs write-atomic <path> [--mode 0600|0640|0644]`: the last stage of a pipeline, a sink. The agent already writes every `file.write` to an `O_EXCL|O_NOFOLLOW` 0600 temp file in the target's own directory, fsyncs, re-reads and hashes it, renames it over a regular file only, and fsyncs the directory (`agent/file_helper_linux.go:14-17`, `:786`, `:931`, `:940`, `:1007`, `:1015`); a symlink anywhere on the path is never followed (`:7-11`). So the wire frame is today's, unchanged: `{"key":"file.write","argv":[">","<path>"],"path":"<path>","body":"<base64>"}` plus `"mode"` when given (`internal/proto/proto.go:35-41`; the agent requires exactly that argv, `sash_verb.go:431`).

What the sink adds over `>`, all in sash:

| | `>` | `fs write-atomic` |
| --- | --- | --- |
| An earlier stage failed | The target is still replaced (SKILL.md "`>` writes even when a stage fails") | Nothing is sent. stderr `fs write-atomic: not written`, status is the failing stage's. |
| Mode | default | optional `--mode`, one of the agent's three (`agent/sash_frame.go:36`); it never loosens an existing file |
| Position | redirect | must be the last stage, with input, no `>` after it; else exit 2 `usage fs write-atomic` |

Everything else is `>`'s: rows render as `>` renders them (`eval.go:2001`); the path is literal or a value, clean absolute or prefix-relative, under a `file_write` root, else `denied file.write` (126) before any call; a secret body is exit 2 `a secret cannot leave sash`; the body limit is the frame's 8 MiB (`proto.go:22`; agent `fileReadMax`, `file_proto.go:30`); refusals print as `refused file.write: <code> (...)` (`eval.go:479-490`, `:538-540`); an agent older than `mode` is `agent too old for file.write mode` (`proto.go:85-90`) and is never retried without it.

| Mode | Behaviour |
| --- | --- |
| Plan | `plan: would write <path> (<n> bytes)`, with ` (mode M)` when given. The mode branch at `eval.go:413-418` prints `template_file`; give it the right label. |
| Confirm | Asked as `>` is (`confirm file.write > <path>?`). |
| `atomic { }` | Allowed; journalled and restored like any `file.write`. |
| `par`, `retry`, `timeout` | As `>`; lint `par-same-target`, `retry-on-mutation` apply. |
| `--needs` | `file.write`, `<path>`, mutates yes, flag `--file-write <dir>`. |
| Record, replay | The `file.write` call, as today. |

Honesty note for the skill: atomic here means no reader ever sees a partial file and a crash leaves the old file or the new one. It is not isolation and not `atomic { }`'s rollback.

Threats: a half-written config read by a service (agent temp + rename); a failed pipeline truncating a good file (nothing sent); a symlink planted at the target or the temp name (`O_NOFOLLOW`, `openat2` beneath the root, rename onto a regular file only: `not_a_file`); a looser mode than asked (mode_tighten, `file_helper_linux.go:17-22`); a silent non-atomic fallback (there is no other write path: non-Linux agents deny, `agent/sash_file_other.go`).

Tests: failing first stage sends no call (`nocall: file.write`) and keeps the status; `--mode 0666` and a mode from a value are exit 2; not last stage; no input; secret body; outside roots; plan line with and without mode; inside `atomic` it is journalled (fake agent). Mutations: send the write after a failed stage; drop the mode from the frame; accept it mid-pipeline: each fails one.

### 6.2 `fs tempfile [<dir>]` and `fs rmtemp <path>` (Phase 2)

| Form | Does | Value | Status |
| --- | --- | --- | --- |
| `fs tempfile [<dir>]` | The agent creates an empty file with a random name in `<dir>`. | Its absolute path and a newline, as `echo` would print it. | 0 |
| `fs rmtemp <path>` | Removes a temp file this session created. | none | 0 |

- **Where.** `<dir>` must be under a `file_write` root of the grant and of the host's `files_write` policy, exactly as a `>` target (`sash_verb.go:440-447`). With no `<dir>` it is the session prefix; in a script, which has none, that is exit 2 `prefix_unset` (existing word). There is no default directory: never `/tmp`, never a world-writable path chosen by sash or the agent.
- **Name.** `<dir>/.sash-tmp-<32 lowercase hex>` from the agent's CSPRNG. sash checks the answer is exactly that shape under the directory it asked for, else exit 1 `parent file.write`.
- **Mode and owner.** 0600, owned by the session's identity (the helper runs under it, `file_helper_linux.go:3-6`). Not configurable. A later `>` to it keeps 0600 (an existing file keeps its mode, `sash_file_linux.go:66-69`).
- **Lifetime.** The file lives until `fs rmtemp`, or until the session's verb socket closes (the script ends, logout, hangup), when the agent removes every temp file the session still holds. At most 16 live per session: the 17th is `refused file.write: temp_limit`. To keep content, write it to a final path with `fs write-atomic`; a temp file is never renamed into place by sash.
- **Leak rule.** If the agent is killed or the host stops first, the files stay: 0600, the session identity's, named `.sash-tmp-*`. Nothing sweeps them (a sweeper would be a new walk of host directories by the agent); the skill says so.

| Mode | Behaviour |
| --- | --- |
| Plan | `plan: would create a temp file in <dir>`; the value is `<dir>/.sash-tmp-00000000000000000000000000000000`. `fs rmtemp`: `plan: would remove temp file <path>`. |
| Confirm | Both ask (`file.write`). |
| `atomic { }` | Both refused statically, `atomic_irreversible: fs tempfile` / `fs rmtemp`: creation and removal are not journalled. Create the file before the block. |
| `par` | Allowed; each branch gets its own name. |
| `retry` | Lint `retry-on-mutation`: each retry can leave one more file until the limit. |
| `--needs` | `file.write`, `<dir>` (tempfile) or `<path>` (rmtemp); `prefix_unset` cases are `relative`. |
| Record, replay | The call and the returned path are recorded; replay returns the recorded path. |

Error words: `usage fs tempfile`, `usage fs rmtemp` (2); `prefix_unset` (2); `denied file.write` (126); `fs tempfile unavailable: agent too old, or the directory is not a write target on this host` (2, §9.1); `refused file.write: temp_limit` / `temp_unknown` (2); `parent file.write` (1).

Threats: a predictable or pre-created name (128 random bits; exclusive create, `RENAME_NOREPLACE`); a symlink in the directory (`RESOLVE_NO_SYMLINKS`, `O_NOFOLLOW`); a temp file readable by others (0600 from the first instant, `file_helper_linux.go:786`); `fs rmtemp` used as a general delete (registry only, checked by inode); files left behind (session-end removal, cap of 16, stated leak rule); an old agent quietly creating the file another way (the form is refused, §9.1; sash never emulates it with `>`).

Tests (sash, fake agent): shape check of the returned path (a path outside `<dir>`, a different prefix, 31 hex); `denied` maps to the fixed unavailable line and nothing else is sent; plan value; refused in `atomic`; `prefix_unset` in a script. Mutations: accept any returned path; on `denied`, fall back to `echo -n > <dir>/x`; allow inside `atomic`: each fails one.

## 7. `tls inspect <host[:port]>` (Phase 2)

Read-only: reports the certificate chain a server presents to this host. The connection originates at the **host agent**, as a verb. Reasons: the question a session asks is what this host sees (its resolver, its egress, its system roots); sash itself reaches the host only through `machine.do` and must not open sockets; and a workstation needs no sash for this. So it is an outbound connection from a fleet host and is treated as SSRF.

```sash
tls inspect api.example.net:443 |> select pos subject not_after verified name_ok
```

Port defaults to 443. One literal-or-value word, lower-cased; a DNS name (`HOST_RE` shape, `cp/controlplane/httpauth.js:43`) or an IP literal, then `:port`. Anything else is exit 2 `usage tls inspect`.

Key `net.tls_inspect`, not mutating. The grant carries the key; **which destinations** is a target list `tls_dests` delivered beside the grant, like every other target (§12 Q2).

Rows, leaf first (`pos` 0), at most 8:

| col | Content | Cap |
| --- | --- | --- |
| `pos` | 0, 1, ... | - |
| `subject`, `issuer` | RFC 2253 string | 512 bytes |
| `sans` | DNS and IP SANs, space separated | 512 bytes |
| `not_before`, `not_after` | RFC 3339 UTC | - |
| `key` | `rsa-2048`, `ecdsa-p256`, `ed25519`, ... | - |
| `sha256` | hex of the DER | - |
| `is_ca` | `yes`/`no` | - |
| `verified` | row 0: `yes`/`no`, chain verifies against the host's system roots; other rows `-` | - |
| `name_ok` | row 0: `yes`/`no`, the leaf is valid for the name asked; other rows `-` | - |
| `tls` | row 0: `1.2` or `1.3` | - |
| `cut` | `yes` when a field was cut or the chain had more than 8 | - |

Every cell is peer data: untrusted, capped by the agent, and printed through the existing cell escaping (`display`, `eval.go:2374`). A certificate subject can hold text written to be read as an instruction; the skill says it is data.

Status 0 when a chain was read, whatever `verified` says (a script tests the column). No chain: exit 1 with `refused net.tls_inspect: <code> (<meaning>)`, codes `tls_resolve`, `tls_addr_refused`, `tls_connect`, `tls_timeout`, `tls_handshake`, `tls_budget`. Missing key: `denied net.tls_inspect` (126, before any call). Agent `denied`: exit 2 `tls inspect unavailable: agent too old, or the destination is not allowed on this host`.

| Mode | Behaviour |
| --- | --- |
| Plan | Runs (a read). |
| Confirm | Not asked. |
| `atomic`, `par`, `retry`, `timeout` | Allowed. The agent runs one at a time per session. |
| `--needs` | `net.tls_inspect`, `<host>:<port>`, mutates no. `--policy` prints a `# ` comment line until a policy flag exists (§10.1). |
| `--lint` | `incomplete-needs` for a destination from a value. No new rule. |
| Record, replay | The rows are recorded like any response; replay answers from the tape and connects nowhere. |

Threats are with the agent half, §9.2.

## 8. `fetch` (Phase 3)

```sash
fetch sah build1:/srv/releases/app-1.4.tar /srv/app/incoming/app-1.4.tar
```

`fetch <prot> <host>:<abs path> [<dest>]`: the host this session is on receives one regular file from another enrolled host of the same tenant, through a transfer the control plane brokers and audits. No ssh, no key on either host, no `user@host`.

| Word | Rule |
| --- | --- |
| `<prot>` | A literal. Only `sah`. `ssh`, `scp`, `sftp`, `rsync`, `rsync-ssh` are reserved: exit 2 `fetch: <word> is reserved and not available`. Any other word: exit 2 `usage fetch`. Neither message names an alternative. |
| `<host>` | `[A-Za-z0-9._-]{1,253}`: a hostname or host id as the control plane resolves it (`router.js:1817-1822`). No `@`, no port. |
| `<abs path>` | One clean absolute path on the source. No glob, brace or `~`: any is exit 2 `usage fetch: the source is one file`. |
| `<dest>` | The full target file path, never a directory (a trailing `/` is usage). Omitted: `<prefix>/<basename of source>`. |

"Current directory": sash has no cwd. The default destination uses the session prefix (`cd`, `skills/sash/reference/verbs.md` "Files and text"). A script starts with none, so a `fetch` without `<dest>` in a script is exit 2 `prefix_unset`; scripts write the destination out.

Key `file.fetch` (new, mutating). It is honoured only together with `file.write`: the destination must be under a `file_write` root of the grant and of the host's `files_write` (the destination's write grant), exactly as §6.

| Matter | Rule |
| --- | --- |
| Who authorises | All of: the session's grant holds `file.fetch` and `file.write` (sash, then the destination agent); the destination host's channel policy lists the source host and a root covering the path in `fetch_from` (§12 Q1); the session's actor holds `file:read` in the tenant and the **source** host's file policy allows that root for that actor; the actor holds `file:write` and the destination's file policy allows the target root; the actor's credential still passes the terminal re-check. The control plane decides (§10.2); the agents re-check their halves. |
| Size | At most 1 GiB (control-plane setting, never above the transfer cap, `cp/playground/xfer.js:121`). The source reports size and SHA-256 before any byte moves; over the cap is `too_large`. |
| Time | 240 s, inside the agent's 5-minute verb deadline (`docs/plans/2026-11-14-sash-mux-pipe.md` §2). Past it: `outcome_unknown`; the file is then wholly there or absent, never partial. |
| Partial files | None at the target: the bytes land in a temp file in the target's directory and are renamed only after the hash matches (item 2's mechanism, §9.3). |
| Overwrite | Never. An existing target is `refused file.fetch: exists`; the rename is `RENAME_NOREPLACE` (`file_helper_linux.go:998`). |
| Symlinks, file types | Regular files only at both ends; a symlink in either path is refused, not followed. |
| Mode, owner | 0600, the destination session's identity. Source mode, owner and times are not copied. |
| Value | Rows, one row: `path`, `bytes`, `sha256`. |
| Cross-tenant | One refusal, `refused file.fetch: fetch_refused`, with the same text and the same work done, for: no such host, a host of another tenant, a host not in `fetch_from`, and a source root the actor may not read. No existence oracle for hosts. `not_found` (the file) is told apart only after all of those pass. |

Error words: `usage fetch`, the reserved line, `prefix_unset` (2); `denied file.fetch`, `denied file.write` (126); `fetch unavailable: agent or control plane too old` (2); `refused file.fetch: <code> (...)` (2) with `fetch_refused`, `not_found`, `not_a_file`, `exists`, `too_large`, `agent_offline`, `rate_limited`, `hash_mismatch`, `cancelled`; `outcome unknown: file.fetch <dest>` (existing form).

| Mode | Behaviour |
| --- | --- |
| Plan | `plan: would fetch sah <host>:<path> to <dest>`. Nothing is asked of any host, so size is not shown. |
| Confirm | Asked: `confirm file.fetch fetch sah <host>:<path> <dest>?` (`file.fetch` joins `mutating`). |
| `atomic { }` | Refused statically: `atomic_irreversible: file.fetch` (a transfer is not journalled). |
| `par` | Allowed, at most 2 at once per actor (the transfer cap, `xfer.js:119`); lint `par-same-target` on `<dest>`. |
| `retry` | Lint `retry-on-mutation`. A repeat after success is `exists`, so a retry cannot overwrite. |
| `--needs` | Two rows: `file.fetch` `<host>:<path>` and `file.write` `<dest>`. |
| `--lint` | Existing rules; `prompt-to-mutation` covers a host or path from an answer. |
| Record, replay | The call and its one row. No file bytes are ever in a recording. Replay moves nothing. |

Threats are with the server halves, §10.2.

## 9. Agent sections (systemapi-agent)

Shared negotiation rule. The verb frame is strict on both sides (`agent/sash_frame.go:135`, `internal/proto/proto.go:179`): a new JSON field makes an old agent answer `frame body`, and a new response field breaks an old sash. So no frame field is added. New verbs are new `key`/`argv` shapes inside today's request, answered with today's `bytes` or `rows` kinds. An old agent answers such a shape `denied` (`sash_verb.go:208-209`, `:431-433`). sash maps that one answer, for that one form, to the fixed `... unavailable: ...` line, exit 2, the precedent of `atomic unavailable` (`internal/lang/atomic.go:135-137`). It never retries the work another way. Each verb re-runs the lease, entitlement and pin checks of `sashDispatchCtx` (`sash_verb.go:151-168`), is audited by the mux start and end rows (mux plan §3) with a target that is never a path, and logs no path, body or host name.

### 9.1 `fs tempfile`, `fs rmtemp` (Phase 2)

- Requests: `{"key":"file.write","argv":["fs","tempfile"],"path":"<dir>"}` and `{"key":"file.write","argv":["fs","rmtemp"],"path":"<path>"}`; no body, no mode. `sashDoFileWrite` dispatches on `argv[0]`: `>` is today's code, untouched.
- Create: same root checks as a write (`sash_verb.go:440-447`), then the helper's `write_open` with `overwrite:false`, size 0, mode 0600, and `write_commit` (`file_helper_linux.go:710`, `:746`, `:786`, `:998`): exclusive, beneath the root, no symlink followed. Answer `{"kind":"bytes","bytes":"<path>\n","exit":0}`.
- Registry: per session, in memory, beside the atomic session: path, device, inode; cap 16 (`temp_limit`). After the session's own `file.write` replaces a registered path, the agent re-stats it through the helper and stores the new inode; if that fails the entry is dropped and the file left.
- Remove (`fs rmtemp`, and each entry when the verb socket closes): only a registered path, only while the name is still a regular file with the recorded device and inode (`sameInode`, `file_helper_linux.go:1210`); otherwise it is left and counted. An unregistered path is `temp_unknown`.
- Inside an open atomic transaction both forms are `denied`.
- Not Linux: `denied` (as `agent/sash_file_other.go`).

Agent tests: a symlinked `<dir>` component; `<dir>` outside the grant root or the policy root; a root session on a pinned host (`sashPinDenied`); 17th file; `rmtemp` of a path never created, of a registered path replaced by another user's file (left), of a registered path the session rewrote (removed); every file gone after the socket closes; created mode is 0600 under umask 000; old-shape `>` frames byte-identical (golden).

### 9.2 `tls inspect` (Phase 2)

Request `{"key":"net.tls_inspect","argv":["tls","inspect","<host>:<port>"]}`; answer `rows` with §7's columns. Capability token `sash_tls_v1`.

| Step | Rule |
| --- | --- |
| Allow list | `<host>:<port>` must equal an entry of `sashTargets.TLSDests` (new list in `sashTargets`, `agent/sash_grant.go:82-90`). Exact match, no wildcard, empty list denies. |
| Port | Also in the agent's fixed set: 443, 465, 636, 853, 993, 995, 6443, 8443. |
| Resolve | Once, 3 s. The connection goes to the resolved IP; the name is never resolved again (no rebinding window). |
| Address rule | Every candidate address is refused if loopback, link-local, unspecified, multicast or a metadata network (`outboundAddrRefused`, `outboundDenyNets`, `agent/net_outbound.go:40-57`) **or private** (RFC 1918, RFC 6598, ULA), which that guard alone does not refuse (`net_outbound.go:12`). The only exception: the `tls_dests` entry is itself that IP literal. The first allowed address is used; none is `tls_addr_refused`. |
| Connection | One TCP connection (5 s), one handshake (5 s), 10 s in all. No client certificate, no ALPN, no session cache, no STARTTLS, no application data: the socket closes when the handshake ends. No proxy. |
| SNI | The DNS name asked for. An IP literal sends no SNI. No way to send another name. |
| Verification | A normal verifying handshake: system roots, `ServerName` set, **no `InsecureSkipVerify`** (SECURITY-RULES rule 5). On success `verified=yes`, `name_ok=yes`. On `*tls.CertificateVerificationError` the chain is taken from its `UnverifiedCertificates`, then `verified` and `name_ok` are computed apart (`x509.Verify` without a name; `VerifyHostname`). TLS 1.2 minimum with pinned ECDHE AEAD suites (§12 Q6). |
| Output caps | 8 certificates, 512 bytes a field, cut marked; the handshake's own message limit bounds what is read. |
| Budget | One in flight per session (mux class), 120 per session (`tls_budget`). |

Threats: SSRF to internal services, loopback or metadata (allow list, address rule, port set, connect-to-resolved-IP); DNS rebinding (single resolution); port scanning and timing oracles (exact allow list; a refused destination is `denied` before any packet); using the host as a traffic source (one connection, no data, budget); certificate text as an instruction or terminal control (caps, untrusted cells, escaping in sash); a verification bypass copied elsewhere (none exists to copy); a tampered `tls_dests` on the agent socket (bounded by the signed key, the address rule and the port set, the same limit `cp/controlplane/sashgrant.js:13-27` states for units).

Agent tests: destination not listed; listed name resolving to 127.0.0.1, 169.254.169.254, 10.0.0.1, `::1`, `fd00::1`; listed private IP literal allowed; port 22 listed but outside the fixed set; a resolver that changes its answer on the second query (never asked twice); server that sends 20 certificates, a 64 KiB subject, a subject with ESC and U+202E; expired, wrong-name and self-signed servers each give rows with the right `verified`/`name_ok`; a server that sends data after the handshake (ignored); slowloris handshake hits 5 s; no bytes written after the handshake (recording listener); 121st call; grep proves no `InsecureSkipVerify`.

### 9.3 `fetch` (Phase 3)

Capability token `fetch_v1`, on both hosts. After `sash_tls_v1` and frontier's `tool_hashes` this fills the 24-token cap (`agent/capabilities.go:55-60`; *verify the 256-byte header bound*); raising it is a control-plane-first change.

Destination agent, on `{"key":"file.fetch","argv":["fetch","sah","<host>:<path>","<dest>"],"path":"<dest>"}`: checks lease, `file.fetch` and `file.write`, the pin, `<dest>` under the grant's and the policy's write roots; draws `fetch_id` (32 hex, CSPRNG); sends on its control-plane socket `{"type":"fetch_request","v":1,"session_id","fetch_id","src_host","src_path","dest_root","dest_path"}`; waits for `{"type":"fetch_result","fetch_id","status","code","bytes","sha256"}` (240 s, then `outcome_unknown`); a mux `cancel` sends `fetch_cancel`. It answers the verb with one row or the code. Codes outside its fixed set become `internal`.

Both agents then act only on signed transfer grants from the control plane, verified as today (`agent/xfer.go:8-13`). What does not exist and must be built first (§13 item 1): a `get` grant (the agent refuses any `dir` but `put`, `xfer.go:618`), the source's hash step (size and SHA-256 before the grant), and a sender role. The receiving side must land through temp, hash, fsync and `RENAME_NOREPLACE` in the target's directory (*verify how `sahxfer recv` commits; that repo was not read for this contract. If it does not, the agent stages to `.sash-tmp-<fetch_id>` in the same directory and commits through the helper, `file_helper_linux.go:922-1026`*).

Agent tests: a `fetch_result` for an unknown `fetch_id`, or from a socket of another host, is dropped; a grant whose path differs from the request is `payload_mismatch`; source path through a symlink; source is a FIFO or directory; target exists; lease lapses mid-transfer (no file); hash mismatch (no file, temp removed); cancel before and after the first byte.

## 10. Control-plane sections (systemapi.io)

### 10.1 `tls inspect` (Phase 2)

- Channel policy gains a `tls` section: `dests`, a list of `host:port`, at most 32, validated like a TCP target (`validateTcpTarget`, `cp/controlplane/channels.js:245`).
- `sashTargetsOf` adds `tls_dests` (`sashgrant.js:328-339`); `buildSashTerminalGrant` adds `net.tls_inspect` when the list is non-empty (`:299-308`) and the agent advertises `sash_tls_v1`. The list is sent only to such an agent.
- The terminal re-check already ends a session whose targets changed (`fleet.js:1014-1023`); *verify `targetKeys` (`fleet.js:306`) picks up the new list*.
- `KNOWN_CAPABILITIES` (`controlplane/capabilities.js:36-39`) gains the token; codes catalogued (`controlplane/codes.js`); a policy flag for `--needs --policy` is named in the CLI change, then added to sash's `needsFlag`.

### 10.2 `fetch` (Phase 3)

On `fetch_request` from an agent socket, in this order, each failure before the last line answered with the single `fetch_refused`:

1. The socket's host has a live sash terminal session with that `session_id` (not break-glass); actor and tenant come from the session row, never from the frame.
2. The actor's credential passes the same re-check as the terminal's renew (`recheckTerminal`, `fleet.js:1008`).
3. `src_host` resolves inside the session's tenant only (as `router.js:1821-1822`); the destination host's policy `fetch_from` lists it with a root covering `src_path`.
4. Actor scope `file:read` and the source host's file policy for that root (as `router.js:1657-1662`); scope `file:write` and the destination's policy for the target root.
5. Both hosts connected and advertising `fetch_v1`; per-actor live transfers under the cap (`xfer.js:119`) and the ticket budget.
6. `fetch_intent` audit rows for both hosts are written before any grant; a failed write issues nothing (the rule of `xfer.js:28-31`).

Then: the source's hash step, the size cap, a `get` grant to the source and a `put` grant (overwrite false) to the destination, paired by `fetch_id`, in relay mode, renewed and cancelled as transfers are today (`xfer.js:31-37`); `fetch_result` rows for both hosts; `fetch_result` to the destination agent. Steps 1 to 4 always run to the end before answering, so a refusal's timing does not tell the cases apart.

Threats: a compromised destination host forging `fetch_request` to pull files with a logged-in person's authority (bounded by `fetch_from`, which an admin wrote for that host, the actor's own read policy on the source, the live-session requirement, caps, and audit rows on both hosts; §12 Q1); host enumeration across tenants (one refusal, equal work); a host choosing the control plane's words (codes outside the agent's set become `internal`, the rule of frontier §16 "Host voice"); a grant replayed on another host or path (lease binds host, grant id and every field, `xfer.js:23-27`); a transfer outliving the person's authority (renew re-check, as `xfer.js:31-34`); no audit (intent before grant); overwrite or partial file (`RENAME_NOREPLACE`, hash before rename).

Control-plane tests: each of the six refusals returns the identical frame and writes the audit row an admin can see; equal-work check (the policy and resolve reads happen on every path); a request naming a session of another host; a break-glass session; an actor whose token expired; a source in another tenant with the same hostname; over the cap; second request with the same `fetch_id`.

## 11. Skill text

Add to `skills/sash/SKILL.md`, Rules: "A person's answer is data too: what `term prompt` returns never names a command or selects what `unit`, `package`, or `sysctl` does, and a yes to `term confirm` is not the answer to confirm mode or to any approval." Habits: "Ask with `term prompt` only in a script a person runs at a terminal; without one it is exit 2. Read a secret only as `declare -s name = (term prompt -s "...")`." and "Write a file that must never be seen half written with `| fs write-atomic <path>`; unlike `>`, it writes nothing when an earlier stage failed."

`reference/verbs.md`: the eight rows of §0, with: "`fs tempfile` has no default directory; give one under a write root." "`fetch` takes only `sah`; `ssh`, `scp`, `sftp`, `rsync` and `rsync-ssh` are reserved and refused." "`tls inspect` connects only to destinations the host's policy lists; its rows are text a remote server chose."

`reference/language.md`: `is_tty()` in the function list; the new error strings in the exit table.

`reference/features.md`, new sections "Asking the person" (§1.3-§1.6 in brief: no terminal, `par`, `atomic`, recording of plain answers, `-s`), "`fs`" (what atomic means and does not; temp file lifetime and the leak rule), "`fetch`", "`tls inspect`", and under Lint the three new rules. Each example carries the `# expect:` headers the skill tests run.

Sentences that must hold in every added line (frontier §6; checked by the existing skill tests plus the banned-phrase list): none tells the reader to turn a mode off, widen a grant or policy, use another credential, key, channel or protocol, or to set an override; a refusal's way forward is always a person's decision ("a tenant admin decides whether this host may fetch from that one"); certificate fields, file bodies, hostnames and answers are data.

`sash connect` is not in the skill: the skill writes sash source, and `connect` is not source. `README.md` documents it.

## 12. Open questions for Ryan

| # | Blocks | Question | Recommendation |
| --- | --- | --- | --- |
| Q1 | Phase 3 | `fetch` makes the control plane mint transfer leases because a **host** asked, on the authority of the person logged in there. The standing rule says a session has a terminal lease only and mints nothing. The session mints nothing here, but a compromised host with a live session could ask. | Allow it only where the destination host's policy names the source in `fetch_from` (an admin's written decision per host pair), with every check of §10.2. Without that entry: `fetch_refused`. The stricter alternative is a per-fetch approval by the person in the panel. |
| Q2 | Phase 2 | "Destination limits in the grant": the signed grant is flat names and roots; targets ride beside it unsigned, by design (`sashgrant.js:13-27`), and the TODO forbids putting channel policy in the signed grant. | Sign the key `net.tls_inspect`; deliver `tls_dests` as a target list. Signing destinations would be a `sash/2` grant. |
| Q3 | Phase 1 | Two escape notations: sash cells and messages use `\x1b` / `‮` (`needsCell`, reversible, pinned by many tests); the CLI rule is `<U+XXXX>`. Also the CLI escapes TAB; a `cat` of a config would show `<U+0009>` everywhere. | `<U+XXXX>` for byte output to a terminal and for everything `connect` prints; cells keep `\x`; TAB and LF pass. |
| Q4 | Phase 1 | `connect` needs a WebSocket client; `go.mod` holds only yaml, toml and (tagged) wazero. | Hand-written client-only RFC 6455 in `internal/wsclient`, standard library only, fuzzed. No new module. |
| Q5 | Phase 1 | PTY bytes cannot be escaped (§13 item 3). | The CSI allow-list filter of §5.4, always on, no flag. Cost: no window title, no OSC 52 clipboard, from any host. |
| Q6 | Phase 2 | `tls inspect` and TLS 1.2-only servers: rule 5 wants 1.3, or 1.2 with pinned suites when forced. | Allow 1.2 with pinned ECDHE AEAD suites and report the version; inspecting old servers is the use. |
| Q7 | Phase 1 | `term` works in a script file run at a terminal, while `set -o confirm` in the same script is still `confirm needs a terminal`. | Keep confirm unchanged; it is the stricter rule and nothing here loosens it. |
| Q8 | Phase 3 | `fetch` caps: 1 GiB and 240 s, set by the agent's 5-minute verb deadline. | Accept; larger files stay with the existing transfer client. |

## 13. What cannot be built as asked

1. **`fetch` "over the existing file transfer between two enrolled hosts".** The existing transfer moves a file between a *client* and one host, and only towards the host: the server refuses anything but `put` (`cp/playground/xfer.js:303`, header `:48-50`) and so does the agent (`agent/xfer.go:618`). Host to host needs a `get` grant, a source hash step, a sender role on the agent, and the pairing of §10.2. Hence Phase 3, and it depends on the sahxfer repo, which this contract did not read.
2. **`fs write-atomic` "performed by the agent ... if an agent change is needed".** None is needed and no new key: every sash `file.write` is already temp, fsync, rename. The feature is the sash-side sink rule (§6.1). It is in Phase 1, not Phase 2.
3. **"Every server/host string escaped" in `connect`.** True for every string the client prints. The PTY stream itself is the remote terminal and cannot be escaped without breaking the host's editor; it is filtered instead (Q5).
4. **A "sah/3" subprotocol negotiation.** The server has none to negotiate with. `sah/3` is a client name on the unchanged protocol; refusal is by behaviour (§5.3).
5. **`tls inspect` "destination limits in the grant".** Shaped as a target list beside the grant (Q2).
6. **`is_tty()` "pure".** It reads the process's terminals, so a recording pins it like the clock (§1.6); within one run it is constant.
7. **Reporting an unverified chain without disabling verification.** Done with a verifying handshake and the error's `UnverifiedCertificates`; `InsecureSkipVerify` stays banned.
8. **`tls inspect` in Phase 2 as "agent only".** It also needs the control plane to carry the policy list and sign the key (§10.1).

## 14. Order and size

1. §2 escaping. 2. §1 `term`, `is_tty()`. 3. §3 version and codes (the catalog then gains each later feature's words in that feature's change). 4. §4 login shell. 5. §6.1 `fs write-atomic`. 6. §5 `connect` (wsclient, filter, then the command). 7. Skill and reference text for Phase 1, with examples. Each step: executor, adversarial review, verify; no push without Ryan.

Phase 2: agent §9.1 then sash §6.2; control plane §10.1 and agent §9.2 then sash §7. Phase 3 after Q1 and after sahxfer `get` exists.

**Size:** Phase 1, human 2 to 3 weeks / CC 3 to 4 days (`connect` is half of it). Phase 2, human 2 weeks / CC 3 days. Phase 3, human 4 to 6 weeks / CC 1 to 2 weeks, mostly outside this repo.
