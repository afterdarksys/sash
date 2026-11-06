# sash source doors

Status: specified. `sash-mcp` and voice are not processes in this tree. The skill file teaches the language. None of these doors runs a helper.

## Shared rule

A door inserts sash source onto the visible line. The existing editor submit path expands that source, preflight checks it, and the agent re-checks the grant before a helper runs. A tool result, a skill step, and an audio buffer do not run a helper by themselves.

A door does not call the parent, the file helper, or a host binary. It does not import `github.com/afterdarksys/adssh`. The systemapi agent does not import sash and does not import adssh. Starlark stays in adssh. There is no `star` command.

## sash-mcp

A later process, separate from this module. Its tools return sash source text for the visible line. A tool does not open the verb socket, does not call `prepareSash`, and does not call `adssh-mcp`. Building that process is a later change. This tree does not contain one.

## Skill

`skills/sash/SKILL.md` tells an agent to put sash source on the line and stop. The skill does not grant entitlements and does not contain a runner.

## Voice

Speech becomes text on the visible line. A later yes submits that line through the editor. Audio alone does not run a mutation. This tree has no audio capture and no speech executor.
