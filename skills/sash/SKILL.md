---
name: sash
description: Teach the sash language and insert source onto the visible line. Use when asked to write a sash command or script. Do not run helpers, host binaries, or adssh.
---

# sash

Insert sash source onto the visible line and stop. The sash session expands it, checks the grant, and runs it. Do not exec a host binary, do not call a file helper, and do not import `github.com/afterdarksys/adssh`.

One language. A hand start has an empty grant. `--session` is the fleet or emergency parent. An unknown name is exit 127. There is no `PATH`, no bash fallback, no `eval`, no word-splitting, and no alias.

`|` carries bytes. `|>` carries rows. `rows` lifts text into rows. `json` records bytes as a JSON string and rows as an array. `>` of a row value stays the text table.

Closed table: `ps`, `unit status`, `unit restart`, `journal`, `package query`, `package install`, `sysctl get`, `sysctl set`, `config read`, `cat`, `ls`, `cd`, `pushd`, `echo`, `egrep`, `head`, `cut`, `wc`, `where`, `select`, `rows`, `json`, `let`, `if`, `for`, `fn`, `dirs`, `popd`. `config write` is not a command.

A function call deeper than eight is exit 2 and stderr `fn depth`. `*(/)` and `**(/)` list directories through `file.read`. Any other glob qualifier is a type error.
