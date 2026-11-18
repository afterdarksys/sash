---
name: sash
description: Write sash, the restricted shell whose statements run verbs on SAH fleet servers under a signed grant, and insert that source onto the visible line. Use when asked to write, fix, explain, or review a sash command, pipeline, or script, or its --plan, --needs, or --lint output. Do not run it, and do not run helpers, host binaries, sash itself, or adssh tools.
---

# sash

Insert sash source onto the visible line and stop. The person's sash session expands it, checks it against the grant, and runs it when they press Enter. Do not run sash or the script yourself, do not exec a host binary, do not call a file helper or the agent, and do not use `github.com/afterdarksys/adssh` or its tools. This skill grants nothing and contains no runner.

sash is the login and emergency shell of the SAH fleet: one Go binary that runs a closed table of in-process filters and agent verbs (`unit restart`, `package install`, `cat`, `>`, ...). Every verb is checked against a signed grant, then re-checked by the host's agent, which performs it and audits it. A hand start (`sash` without `--session`) has an empty grant: verbs are denied and filters still run.

## Habits

- **Write for plan mode first.** A script should read well under `sash --plan`, where `unit restart`, `package install`, `sysctl set`, and every file write print `plan: would ...` instead of running. Suggest the person runs `--plan` before the real run.
- **Suggest `sash --lint` and `sash --needs` on a script.** Lint finds secret leaks, unreachable code, and retried mutations; needs prints the grant the script asks for. Both run nothing. Keep targets literal so the manifest is complete.
- **Never put a secret in source.** Read it from a granted file with `declare -s tok = (cat <path>)`. A secret prints as `***`, cannot decide a branch, and cannot leave sash except through `template_file` at mode 0600.
- **Use only names in the closed table.** An unknown name is exit 127: there is no `PATH`, no bash fallback, no `eval`, no word-splitting, and no alias. A command word must be a literal: `$x status nginx` is exit 2 `command_from_value`.
- **Pick the right pipe.** `|` carries bytes, `|>` carries rows; `rows`, `lines`, `parse`, or `from json` lift text into rows. Bytes into `|>` is exit 2.
- **Write absolute paths and globs in scripts.** A script starts with no prefix, and globs expand before it runs.
- **Group related file writes in `atomic { }`, and keep mutations out of `retry`.**

```sash
unit status nginx |> where state == "failed"
ps -ef | egrep nginx | head -n 5
cat /etc/nginx/nginx.conf | egrep listen
```

## Reference files

Read the one you need; each stands alone.

- `reference/verbs.md`: the closed table, every name with its kind, entitlement key, and whether it mutates. Read it before using a name you are not sure exists.
- `reference/language.md`: values, quoting, `|` and `|>`, redirection, `[ ]` expressions and functions, `let`, `if`, `for`, `while`, `fn`, `try`/`catch`, `set` options and caps, globs, exit statuses, and the error strings a run prints. Read it for anything past a one-line pipeline, or to explain an error.
- `reference/features.md`: plan mode, `--needs`, `--lint`, confirm, secrets, templates, `atomic`, `par`, `retry`/`timeout`, `check`, `log`, `origin`, `with`, `use`, recording and replay, `why`, abbreviations, and wasm. Read it before a script that changes a host, runs in parallel, or touches a secret.

## Installing

This skill ships inside the sash binary. A person installs it; it is not a step in writing sash: `sash --skill > ~/.claude/skills/sash/SKILL.md`, and `sash --skill language`, `sash --skill verbs`, `sash --skill features` into `~/.claude/skills/sash/reference/<name>.md`. `sash --skill --list` lists the files.
