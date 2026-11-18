# sash

The login and emergency shell of the SAH fleet: one Go binary that runs a closed table of in-process filters and agent verbs under a signed grant. The design is `docs/plans/2026-11-06-sash-design.md`; the language is in `docs/plans/` (v2 to v6) and, for agents, in the skill under `skills/sash/`.

## Command line

```text
sash [--session] [--plan] [-c script | file]   run a script; a terminal with no script is the live prompt
sash --needs [--policy] [-c script | file]      the grant a script asks for; runs nothing
sash --lint [-c script | file]                  advisory findings; runs nothing
sash --record out.jsonl --fixture fixture.json [--expires D] [-c script | file]
sash --replay rec.jsonl --fixture fixture.json [-c script | file]
sash --skill [features | language | verbs]      print the embedded Claude skill
sash --skill --list                             list the embedded skill files
```

Without `--session` the grant is empty: verbs are denied and filters still run. `--session` reads the grant and the verb socket from fds 3 to 6, as the systemapi agent or `adssh` sets them up.

## Build and test

```text
./build.sh            bin/sash (SASH_TAGS=sashwasm for wasm modules)
./build.sh test       gofmt, vet, tests and -race, with and without sashwasm
```

The release binary is built without the `sashwasm` tag.

## The Claude skill

`skills/sash/` teaches an agent the language and tells it to insert sash source onto the visible line and stop; it contains no runner and grants nothing. The same files are embedded in the binary. `sash --skill` takes no other flag, no script, and no file, reads no grant and nothing from disk, and writes only to stdout. Reference names are the bare words `features`, `language`, and `verbs`; any other spelling (`verbs.md`, `reference/verbs`, a path) is exit 2.

Install from a binary:

```sh
mkdir -p ~/.claude/skills/sash/reference
sash --skill > ~/.claude/skills/sash/SKILL.md
sash --skill language > ~/.claude/skills/sash/reference/language.md
sash --skill verbs > ~/.claude/skills/sash/reference/verbs.md
sash --skill features > ~/.claude/skills/sash/reference/features.md
```

Install from a checkout (copies `skills/sash` to `${1:-$HOME/.claude/skills}/sash`, refuses a symlinked target, prints what it wrote):

```sh
skills/install.sh [skills-dir]
```
