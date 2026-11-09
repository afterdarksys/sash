# sash design

Status: commits 1–9 are landed. The later series landed `rows`, `json`, a function depth cap, and the directory glob `*(/)`. Commit 9 lists the closed table from `prompt menu`. A denied row stays visible and inserts nothing. A choice inserts sash source onto the line, and a menu-built mutation shows the danger color before it runs. `prompt` does not change the grant. Module `github.com/afterdarksys/sash`. This file is the contract. `sash-mcp`, the skill, and voice are source doors in `2026-11-06-sash-doors.md`. They insert sash source and do not run a helper.

Next action: `config.write`, fleet-wide suggestion history, background jobs, aliases, and darwin and windows login stay blocked. The reasons are in [Deferred](#deferred). `sash-mcp` and voice stay designs. The skill inserts source.

## What this is

sash is the login shell and the emergency shell for the SAH fleet. One Go binary, one grant, two parents.

| Product | Role |
| --- | --- |
| sah suite (`opensah`, `sahconnect`, `sahsync`, the rest) | Transport and control plane. Stays as it is. |
| `adssh` | Administrative daemon. Starlark stays here. Parent of sash only for local admin and break-glass. |
| `sash` | The shell. Prompt, scripts, pipelines, and the fish-meets-bash line editor. |

People script in sash. Starlark is the daemon language. A fresh session does not add a `star` command, an embedded Starlark interpreter, or an import of `github.com/afterdarksys/adssh` from sash or from the systemapi agent.

Module path: `github.com/afterdarksys/sash`, directory `/Users/ryan/development/sash`. Names that stay until Ryan says otherwise: the `adssh` binary, `github.com/afterdarksys/adssh`, `adssh-mcp`, `~/.adssh`, the adssh GitHub repo. `sahcore` stays docs-only. `computeapi.io` does not grow a shell.

## Parents

The systemapi agent is the parent for fleet login. `adssh` is the parent for emergency. Both exec the same binary.

On Linux, `pty` and `pty_root` exec sash. `sessionEnv` still records the passwd shell. After `prepareSash` succeeds, `SHELL` is the looked-up sash path. A path that is not a safe env token does not start, and the path is not logged. Other actions keep the passwd shell. Break-glass resolves `adssh` and returns `adssh_missing` rather than falling open to a login shell. Recording is HMAC-chained and a failed record refuses or ends the session. An ordinary session does not call `adssh` (`systemapi.io/docs/sah-suite.md` Wired join item 7).

### Fleet login

1. For agent actions `pty` and `pty_root` (ticket purposes `terminal:open` and `terminal:root`), `argv0` is a fixed `sash` lookup. Missing binary ends the session with `sash_missing`. The passwd shell is not used.
2. Account drop, the lease timer, and HMAC recording of every PTY byte stay as they are. `pty_root` still runs only when recording mode is agent (`root_unavailable` otherwise). Root is still refused as the ordinary pty user.
3. The agent opens the grant pipe and the verb socket before the privilege drop. The grant pipe carries one signed `sash/1` document; the agent writes it and closes its end. The verb socket carries requests from sash and answers from the agent. Byte layout is the Wire section. The PTY stays user keystrokes and screen bytes. The existing input loop stays the only writer of user input to the PTY.
4. A keystroke does not travel to the control plane. The control plane authorized the session and receives the audit row. The local agent fulfills verbs.
5. sash holds no API key. The grant is not an environment variable. The child env keeps today's drops: names starting with `SYSTEMAPI`, names containing `API_KEY`, names starting with `ADSTELCO`.
6. This swap is the Linux fleet path. darwin and windows stay `sash_missing`. `sashLogin` is false there. The identity drop and the fd path are Linux-only.
7. When step 1 lands, edit the agent, sash, and Wired join item 7 in the same change. A roadmap sentence does not make the call real. Say that an ordinary session execs `sash` and still does not call `adssh`.

### Emergency

Break-glass stays `adssh` as the agent's child. `adssh` then execs the same sash binary with a locally signed grant, purpose `emergency`, on the same fds as the Wire section. The agent still records the PTY.

## Grant

Flat names only. The shell has no policy language. Group expansion happens in the parent before signing. The control plane signs a login grant from the host's channel policy, else the tenant default, else deny-all (`ps` only). Default profiles (`observe`, `operate`) are presets written into that policy: `2026-11-09-sash-default-profiles.md`.

```json
{
  "v": "sash/1",
  "actor": "ryan",
  "host": "miniapps1",
  "purpose": "terminal:open",
  "exp": 1762400000,
  "lease_id": "...",
  "entitlements": ["ps", "unit.status", "unit.restart", "journal", "package.query", "package.install", "sysctl.get", "sysctl.set", "config.read", "file.read", "file.write"],
  "file_read": ["/var/log", "/etc"],
  "file_write": ["/var/tmp/ops"],
  "allow_private": false,
  "sig": "..."
}
```

`purpose` is `terminal:open`, `terminal:root`, or `emergency`. `allow_private` defaults to false when absent.

Exit before the prompt, one stderr line, no REPL, when any of these hold:

| Stderr | Condition |
| --- | --- |
| `grant_missing` | No document on the grant pipe. |
| `grant_bad_shape` | `v` is not `sash/1`, a required field is missing, or a file root is relative. |
| `grant_star` | Any entitlement is `*`. |
| `grant_bad_sig` | Signature does not verify for this purpose's key. |
| `grant_expired` | `exp` is in the past. `exp` equal to now is still valid. |

Check order is the Wire section. A tampered document is `grant_bad_sig` even when it also contains `*` or an old `exp`. A validly signed `*` is `grant_star`.

A script, function, or loop cannot grow this set. Each invocation checks the same grant.

### Grant signing

Ryan confirmed this rule on 2026-11-06.

The control plane signs fleet grants (`terminal:open`, `terminal:root`) with an ed25519 key used only for `sash/1`. That key is not `SYSTEMAPI_ED25519_SEED`. A lease blob is a different payload (`systemapi-lease-v1` over `"<action>\n<payload>"`) and must not verify as a grant. The agent copies the signed bytes onto the grant pipe and does not hold the grant-signing private key. The agent verifies before exec. sash verifies again.

`adssh` signs only purpose `emergency`, with a per-host key. A fleet signature on an emergency grant fails `grant_bad_sig`, and the reverse fails too, because the purpose is inside the signed bytes and selects the key.

Pubkey delivery: the parent opens both pubkey files as root before the privilege drop and passes the fds. sash reads those fds and does not open either path. The pty user cannot swap the file after the drop. Which fd is which is the Wire section.

Signature covers the canonical JSON of every field except `sig`. Canonical bytes are the Wire section.

### Wire (commit 2)

`sash --session` is the only argv that reads these fds. It is argv[1], then the same `-c`, file, or stdin script. A hand start leaves the fds alone and keeps the empty grant. Grant failure prints the token on stderr and exits 2, before the script runs.

| FD | Direction | Bytes |
| --- | --- | --- |
| 3 | parent writes, sash reads to EOF | One `sash/1` document, at most 64 KiB. Zero bytes is `grant_missing`. One byte past 64 KiB is `grant_bad_shape`. |
| 4 | parent writes, sash reads | Fleet public key. Raw 32 bytes. Any other length is `grant_bad_sig`. |
| 5 | parent writes, sash reads | Emergency public key. Raw 32 bytes. Same length rule. |
| 6 | duplex | Verb socket. One process, both directions. |

The two public keys must differ. Identical keys are `grant_bad_sig`, so a purpose cannot collapse onto one key. `terminal:open` and `terminal:root` verify with fd 4. `emergency` verifies with fd 5. A fleet signature on an emergency grant is `grant_bad_sig`, and the reverse is too.

Check order, stop at the first hit:

1. Empty document: `grant_missing`.
2. Not JSON, unknown field, trailing value, `v` other than `sash/1`, missing required field, bad purpose, empty name, relative or unclean file root, or oversize: `grant_bad_shape`.
3. Keys unusable or identical, or the signature against the purpose-selected key fails: `grant_bad_sig`. A forged `*` lands here.
4. Any entitlement `*` after a valid signature: `grant_star`.
5. `exp` less than now: `grant_expired`.

The signed message is the prefix `sash-grant-v1\n` plus compact JSON of every field except `sig`, in this order: `v`, `actor`, `host`, `purpose`, `exp`, `lease_id`, `entitlements`, `file_read`, `file_write`, `allow_private`. Nil slices encode as `[]`. `allow_private` is always present. Verification rebuilds that message from the parsed fields. Raw whitespace and key order do not have to match. `sig` is standard base64 of 64 ed25519 bytes. The prefix is not `systemapi-lease-v1`, and a lease message does not verify as a grant.

A verb frame is a big-endian uint32 length and one JSON object. A length above 8 MiB is `frame too large` and the body stays unread. Request fields: `key`, `argv`, `path`, `body` (standard base64, omitted when empty). Response fields: `kind` (`bytes` or `rows`), `bytes` (standard base64), `rows`, `cols`, `exit`, `error`. Unknown JSON fields are rejected.

### What the agent re-checks

Passing the grant check inside sash is not enough for a verb the agent performs. Before the helper runs, the agent checks three things: the verb's entitlement is in the grant, the session lease still authorizes this action, and the channel or file policy allows the target. A failed re-check returns a denial. The helper does not run.

## Language

One executor for the prompt, `sash -c`, and `sash file` / `#!/usr/bin/sash`.

v1 syntax is sequences, `|`, `|>`, `&&`, `||`, quoting, `let`, `if`, `for`, `fn`, redirects, `$name`, and `$row.field`.

Values stay values. A variable is not parsed again as syntax. There is no word-splitting, no `eval`, no `PATH`, and no alias that hides the verb. An unknown name is an audit row and exit 127. The host binary of that name is not executed.

### Pipes

`|` renders the left side to text. The right side reads bytes. `ps -ef | egrep sshd | head` is the legacy line. A row command with no row consumer prints its text table, so `ps -ef` looks like the classic table.

`|>` accepts rows only. `unit status |> where state == "failed"` keeps rows. `egrep sshd |> where pid > 1` is a type error: exit 2, nothing runs. `rows` is the lift from text to rows. With no names, each line is the column `line`. Named columns split on whitespace and the last name keeps the rest of the line. A byte pipeline does not call `rows` implicitly. `echo hello |> where line == "hello"` stays a type error and runs nothing.

`if` is true for a row pipeline when it yields a row, and true for a byte pipeline when the exit is 0. A `where` that keeps nothing is success and is false in `if`. `for` walks rows.

A byte pipeline fails when any stage is non-zero. Its status is the rightmost non-zero, or 0 when every stage is 0.

`>` writes the text rendering through `file.write`. `json` records bytes as one JSON string and rows as one JSON array. `>` of a row value stays the text table. `>` after `json` writes that JSON text. Invalid UTF-8 is exit 2 and stderr `json utf8`, and nothing is printed.

### When a line runs

1. Expand history tokens, braces, and globs on the line (see [Visible expansion](#visible-expansion)). A short name is not rewritten.
2. Parse the expanded line.
3. Resolve every stage and every redirect path.
4. If any stage is denied or any path is outside the grant roots, run nothing. Denial prints `denied <key>` and exits 126. A bad path prints `denied file.read` or `denied file.write` and exits 126.
5. Run stages. Agent verbs go over the control pipe. Filters run in process.

The subcommand selects the key: `unit` is `unit.status`, `unit restart` is `unit.restart`. The same check runs on every call from a script, function, or loop.

A script continues after a statement whose status is 1, such as an `egrep` that keeps nothing. Status 126, 127, or 2 stops the rest of that script. The program status is the stopping status, or the last statement's status when the script finishes. A later `echo` must not hide a denial or a type error.

`echo` is ungated. It writes its words and a newline. `cat` with no path copies bytes already in the pipeline and does not require `file.read`. `cat` with a path is `file.read`.

`fn` is a statement. A function name used as a pipe stage, or a function call with a redirect, is exit 2 and stderr `type fn`. Each call checks the grant again. A function cannot be named one of the closed-table commands. A call deeper than the glob depth, eight, is exit 2 and stderr `fn depth`. The message does not include the call. Sequential calls still run.

An unset `$name`, or a field on a value that has no fields, is exit 2 and stderr `type value`.

`<` is legal only on the first stage of a pipeline. `>` is legal only on the last stage. `>` writes the rendered text through `file.write` and leaves stdout empty.

Commit 7 expands `!!`, `!$`, braces, and unquoted `*` / `**`. The lexer still treats `*` as a word until that expansion. A short name such as `unt` stays literal. `sash` with no arguments reads stdin as one script. A hand start uses an empty grant, so toolkit verbs deny and filters still run. `--session` is the Wire section. The live line editor arrived in commit 6.

## Where work runs

In sash, ungated, because they only transform values already in the process: `echo`, `egrep`, `head`, `cut`, `wc`, `where`, `select`, `rows`, `json`, `let`, `if`, `for`, `fn`, `dirs`, `popd`.

The local agent fulfills these, each behind its key. The verb shape matches the systemapi sys and file channels. Fulfillment is local. There is no control-plane HTTP call per command.

| Command | Key | Channel binding already on the agent |
| --- | --- | --- |
| `ps` | `ps` | process table, local |
| `unit` status | `unit.status` | `service_policy.units` or `status_units` |
| `unit restart <name>` | `unit.restart` | `sys:service` restart, `service_policy.units` only |
| `journal` | `journal` | journal read, local |
| `package query` | `package.query` | `sys:package` query |
| `package install` | `package.install` | `sys:package` install |
| `sysctl get` | `sysctl.get` | `sys:tweak` sysctl-get |
| `sysctl set` | `sysctl.set` | `sys:tweak` sysctl-set |
| `config read` | `config.read` | `sys:config` read |
| `cat`, `ls`, `<`, globs | `file.read` | file helper |
| `>` | `file.write` | file helper |

`config write` is not a sash command. The existing helper needs a signed `sys_config` lease whose payload is the path and the content hash. A sash session only has the terminal lease. The agent does not mint a sys lease and does not pass one into the helper.

The agent caps one verb result at 8 MiB and returns `result_too_large` above that. sash treats it as a failed stage. The cap bounds memory in the pty-user process.

Commands are language forms or agent verbs. They are not child processes. v1 has no `&`, no job control, and no background jobs. `pty_session_linux.go` currently assumes a job-control shell's process groups; the sash child is one process, so that kill-tree path must reap sash and the helpers the agent spawned for it, and must not expect sash to manage its own process group of pipelines.

## Files and the session prefix

`cat`, `ls`, `<`, `>`, and globs go through the file helper: pty user, `openat2`, `RESOLVE_NO_SYMLINKS`, `RESOLVE_BENEATH`, path under the grant's `file_read` or `file_write` roots. sash does not open user paths.

The session prefix is sash state, a string under one granted root. It is not the process cwd. The process cwd stays the directory the parent set (`home`, or `/` when home is missing). Relative paths join the session prefix, then the helper checks the result.

`cd` and `pushd` move that prefix. The target must already be inside a `file_read` root. The helper `stat`s it (verb key `file.read`, argv `stat`, the resolved path). A prefix that would leave the roots is `denied file.read` and the prefix stays put. `dirs` prints the stack and does not call the parent. `popd` drops the top when at least two entries remain and does not call the parent. Popping the last entry is exit 2 and stderr `usage popd`. With an empty `file_read`, the prefix is unset and path commands fail closed (`prefix_unset`, exit 2).

A glob is the existing file helper list: verb key `file.read`, argv `ls`, path under a read root. There is no `file.list` entitlement. `*` matches names in that directory, files and directories. `**` asks `ls` again for child directories that stay under the same root. `*(/)` and `**(/)` keep directories only. Any other qualifier is a type error and does not list. sash never opens the directory itself. `cat /var/log/*.log` lists, then cats each kept path. Names outside the roots are dropped.

## Interactive layer

The line editor calls the real parser. A novice menu and the advanced line emit the same sash source. `sash-mcp`, the skill, and voice are specified in `2026-11-06-sash-doors.md`. Each inserts sash source. None runs a helper.

### Feel

The text a person types is bash. `ps -ef | egrep sshd`, `&&`, redirects, and `!!` mean what those operators mean.

Before Enter, the line shows the fish answer: syntax color, a gray history suggestion, a dimmed denial with the missing key, and a danger color on a mutation.

Tab is the zsh menu. Matches come from the closed table. Each row shows the one-line description and the entitlement key. Denied matches stay in the menu, dimmed, with the key, and cannot be accepted.

The smile is the one line under a mistake. It names the next key: `denied unit.restart`, or the near miss `unit restart`.

### Live line

Distinct looks for an entitled toolkit command, a pure filter, an unknown name, and a `|>` whose left side is bytes. A denied command stays visible, dimmed, and completion shows it dimmed with the missing key. Enter runs nothing.

Denial color is authorization UI. Turning syntax colors off leaves the denial color in place. Syntax colors are a session setting.

Mutation forms take a danger color before enter: `unit restart`, `package install`, `sysctl set`, and `>`.

Completion rows show the one-line description and the entitlement key. A gray suggestion finishes the line from history. Right arrow accepts it. Up arrow searches by the typed prefix. Ctrl-R searches the same history. A near miss offers the closed-table name (`unt restart` points at `unit restart`).

Emacs bindings are the default. `bind vi` switches them for the session.

A bare `exit` or `logout` ends the interactive session with status 0, the same as Ctrl-D on an empty line. It also ends the session when `!!` or a history recall expands to it. Like `bind` and `prompt`, these are session words, not closed-table commands: with arguments, in a script, under `-c` or on piped stdin they stay unknown names (exit 127).

### Novice menu

`prompt line` is the default. `prompt menu` lists the closed table.

Entitled rows are selectable. Denied rows stay visible, dimmed, with the missing key, and selecting one inserts nothing. A selection inserts sash source onto the line and returns to that line. Choosing `unit`, then `restart`, leaves the text `unit restart `. The danger color and the preflight are the ones the line already uses. The menu has no policy of its own.

Why both faces exist: a new operator can see what this grant allows, and an operator who already thinks in pipes stays on the line. `prompt` is ungated session UI. It does not change the grant.

Commit 9 landed. A denied row inserts nothing and a mutation built by the menu shows the danger color before it runs.

### Visible expansion

No short token is rewritten on space. `unt ` stays `unt `. `!!` and `!$` rewrite on the line before they run. Brace expansion is local. Alt-. inserts the last argument. A here-string `<<< word` is stdin bytes plus a newline and is not a file read.

The grant check, the agent, and the audit row all see the expanded command.

### History, private session, trace

Suggestion history is this actor on this host. The agent stores it and returns it. Other actors and other hosts are absent from the result. sash does not treat a file in the pty user's home as the authority.

A private session exists only when `allow_private` is true. Those lines are stored so the audit has them, and they are not returned as suggestions. The agent still audits the verbs. The PTY recording still runs. Private is reduced suggestion visibility.

`trace` prints each stage and its entitlement key before the stage runs.

### Voice, MCP, and skills

Specified in `2026-11-06-sash-doors.md`.

| Door | Behavior |
| --- | --- |
| `sash-mcp` | Not a process in this tree. A later process returns sash source for the visible line. It does not run a helper and does not import `github.com/afterdarksys/adssh`. |
| sash skill | Teaches the language and inserts source onto the visible line. It does not contain a runner. |
| voice | Speech becomes text on the visible line. A yes runs that line. Audio alone does not run a mutation. This tree has no audio capture. |

A door that runs a helper on its own would be a second policy. These doors do not.

## Commit series

Each commit builds and tests alone. Security-domain commits (2, 4, 5, and the agent half of 3) read `~/development/ads-fable-utils/SECURITY-RULES.md` first, carry a `Threats:` note, and include negative tests: tampered grant, expired grant, `*`, wrong purpose key, path outside roots, symlink escape refused by the helper contract, lease denied on the agent re-check.

1. Language. Landed. Grammar, both pipes, `let` / `if` / `for` / `fn`, scripts, filters, preflight denial. Toolkit names call a `Parent` interface. Tests use a fake parent and a fixture grant. Done when `ps -ef | egrep sshd` and a row `|>` pipeline pass against the fake, a denial runs nothing, and a type error exits 2 before any call.
2. Protocol. Landed. Signed `sash/1` on fd 3, verb frames on fd 6. A test double answers verbs. Done when a bad, missing, expired, or `*` grant exits before the prompt with the stderr token in the table above.
3. Login reads. Landed. The systemapi agent execs sash for `pty` and `pty_root` on Linux and fulfills `ps`, `unit` status, `package query`, `cat`, `ls`, `config read`, `sysctl get`. `journal` stays denied until a reader exists; the agent does not shell out to journalctl. darwin and windows keep the passwd shell. Done when a Linux login can run `ps -ef | egrep sshd` and Wired join item 7 names sash.
4. Writes. Landed. `unit restart`, `package install`, `sysctl set`, and `>`. Preflight every stage. The agent re-checks the lease before the helper. Done when a denied write performs no helper call, and an allowed write matches the existing sys-channel binding.
5. Emergency parent. Landed. `adssh` execs the same binary with a locally signed grant, purpose `emergency`. Done when a fleet signature on that grant exits `grant_bad_sig`, and a valid emergency grant reaches the same prompt.
6. Live line. Landed. Real parser, denial dimming, danger color, completions, history suggestion keys, near miss. Done when a denied name is visible and Enter runs nothing, including with syntax colors off.
7. Visible expansion. Landed. `!!`, `!$`, braces, `*` and `**` via `file.read` argv `ls`, here-strings, Alt-.. A short name is not rewritten. Done when the audit row shows the expanded command.
8. History, private session, vi bindings, trace. Landed. Suggestion history is this actor on this host. A private session returns no suggestions. The verb audit and the PTY recording still contain the session. `trace` prints the stage key before the stage runs.
9. Novice menu. Landed. `prompt menu` lists the closed table. A denied row stays visible and inserts nothing. A choice inserts sash source onto the line. A menu-built mutation shows the danger color before it runs.

## Deferred

| Work | When |
| --- | --- |
| `rows` command that lifts text into rows | Landed. No names uses the column `line`. Named columns split on whitespace and the last name keeps the rest of the line. |
| `config.write` | Blocked. The helper needs a signed `sys_config` lease for the path and content hash. A sash session has the terminal lease only. The agent does not mint one. |
| Record pipelines saved as JSON | Landed as `json`. `>` of a row value stays the text table. |
| Fleet-wide suggestion history | Blocked until a control-plane store is named. v1 history stays on the agent. |
| Background jobs, `&`, job control | Blocked until the single-process reap rule is replaced. |
| Aliases, programmable completion beyond the closed table | Blocked. v1 has no alias store, and an alias must not hide the verb. Completion stays the closed table. |
| Glob qualifiers such as `*(/)` | Landed for `*(/)` and `**(/)`. Any other qualifier is a type error and does not list. |
| `sash-mcp`, the sash skill, and voice | Specified as source doors. The skill inserts source. `sash-mcp` and voice are not processes in this tree. Audio alone does not run a mutation. |
| darwin and windows login shells | Blocked. The identity drop and the fd path are Linux-only. `sashLogin` stays false. |
| Embedding Starlark or a `star` bridge | Stays in `adssh`. A future change needs an explicit Ryan request. |

## Threats

- The pty user is untrusted. A patched sash can ask for any verb. The agent re-checks the grant, the lease, and the file or channel policy, and is the component that runs the helper.
- A `*` grant or a forged signature would become a root shell. Both exit before the prompt, and the agent refuses to exec a grant it cannot verify.
- File verbs can escape through symlinks. The existing helper's `openat2` flags are the mechanism. sash never opens the path itself.
- History and private mode can be mistaken for "not audited." Suggestions are filtered. Verbs are audited. PTY bytes are recorded.
- An abbreviation can hide a mutation from the operator and from the audit. Expansion is shown on the line, and the expanded form is what the grant check, the agent, and the audit row see.
- A menu label, a spoken phrase, or an MCP tool call can hide a mutation the same way. Each of those doors inserts sash source onto the visible line. Preflight and the agent re-check run on that source. Audio and a tool result do not run a helper by themselves.

## Next action

`rows`, `json`, the function depth cap, and `*(/)` are landed. Linux login sets `SHELL` to the looked-up sash path after `prepareSash` succeeds. `config.write`, fleet-wide history, jobs, aliases, and non-Linux login stay blocked for the reasons in the table above. `sash-mcp` and voice stay designs. The skill inserts source.
