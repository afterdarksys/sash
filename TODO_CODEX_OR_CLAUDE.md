# Left out on purpose

These stayed out of the later series. Do not implement one until Ryan names a contract for that item. A stub that always denies, a second writer, a minted lease, or a hidden runner is not the contract.

Source of the block reasons: `docs/plans/2026-11-06-sash-design.md` section Deferred. Source doors that already exist: `docs/plans/2026-11-06-sash-doors.md` and `skills/sash/SKILL.md`.

Landed and closed, do not reopen: `rows`, `json`, function depth 8, `*(/)` and `**(/)`, Linux `SHELL` after `prepareSash` (system api commit `256716c`). sash `main` design pointer is `b43dc49`.

## Do not

- Do not add PATH execution, a bash fallback, or a host binary for an unknown name. Unknown names stay exit 127.
- Do not import `github.com/afterdarksys/sash` from the system api agent. Do not import `github.com/afterdarksys/adssh` from sash. The agent does not import adssh.
- Do not mint a `sys_config` lease from a sash session. The session has a terminal lease only. A failed re-check returns denied and does not call the helper.
- Do not log a path, a body, a seed, an upload id, or a command line.
- Do not rename adssh, the sash module, `adssh-mcp`, `~/.adssh`, or the GitHub repo.
- Do not push. Do not amend landed commits. Do not stage `.env.production`, `.claude/`, `.context/`, or `docs/sah-installer-design.md` in the system api tree.
- Security-domain code reads `~/development/ads-fable-utils/SECURITY-RULES.md` first. New security paths need a `Threats:` note and negative tests. Do not swallow errors with `_ =`.
- No AI attribution in commits, PR text, tags, or release notes.

## config.write

Own key. Do not reuse `config.read`. The existing writer is `configWriteViaHelper(target, content, lease *Lease)` in the system api agent `config_helper.go`. The helper rebuilds `sysConfigLeasePayload("write", target, sha256(content))` and calls `helperAuthorize("sys_config", payload, lease, now)`. That lease binds the exact path and the content hash.

A sash session only has a terminal lease (`pty` / `pty_root`). Passing nil is `lease_missing`. Passing the terminal lease puts a different capability into the handoff, and a non-nil lease can trigger the unit before the helper refuses. Do not invent a second writer. Do not put channel or file policy inside the signed grant.

A real contract names who signs the `sys_config` lease for that path and that content hash, and how a failed re-check returns denied before any helper call. Negative tests, if that binding is added: path outside the grant, symlink, missing lease, wrong content. Do not log the content or the path.

## Fleet-wide suggestion history

v1 history is this actor on this host, stored by the agent. Files are `<hex>.suggest` and `<hex>.audit` under the agent history root. Private sessions write the audit and are not returned as suggestions. History is not an entitlement.

A later series has to name the control-plane store. Do not invent that store, do not copy suggestion files between hosts, and do not treat history as a grant.

## Background jobs

The agent reap rule is one process. Adding `&` or job control without replacing that rule is a security regression. A real contract replaces the reap rule first, then names how a background job is started, reaped, and audited. Do not add `&` on top of the current rule.

## Aliases

v1 has no alias store. An alias must not hide the verb. Expansion has to stay visible on the line before the grant check, the same way `!!`, `!$`, braces, and globs already rewrite before the check. Do not add a hidden alias. Completion stays the closed table until a separate contract says otherwise.

## Darwin and Windows login

`sash_session_other.go` (`//go:build !linux`) returns `errSashMissing`, and `sashLogin` is false. The identity drop and the fd path are Linux-only. Do not flip `sashLogin` to true on darwin or windows without that drop and fd path. Missing sash stays `sash_missing`. There is no fall-open to bash or to the passwd shell for `pty` / `pty_root`.

## sash-mcp process and voice executor

Specified as source doors only. Each door inserts visible sash source onto the line. The editor submit path expands, preflights, and the agent re-checks. A tool result, a skill step, and an audio buffer do not run a helper.

Do not build an MCP process or a voice executor in this tree. A later sash-mcp process would return source text and must not open the verb socket, call `prepareSash`, or call `adssh-mcp`. Voice turns speech into text on the visible line. A later yes submits it. Audio alone does not mutate. This tree has no audio capture and no speech executor. The skill tells an agent to put source on the line and stop. Do not tell the skill to run adssh tools. Starlark stays in adssh. Do not add a `star` command.
