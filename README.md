# sash

The login and emergency shell of the SAH fleet: one Go binary that runs a closed table of in-process filters and agent verbs under a signed grant. The design is `docs/plans/2026-11-06-sash-design.md`; the language is in `docs/plans/` (v2 to v6) and, for agents, in the skill under `skills/sash/`.

## Command line

```text
sash [-l] [--session] [--plan] [-c script | file]   run a script; a terminal with no script is the live prompt
sash --needs [--policy] [-c script | file]      the grant a script asks for; runs nothing
sash --lint [-c script | file]                  advisory findings; runs nothing
sash --record out.jsonl --fixture fixture.json [--expires D] [-c script | file]
sash --replay rec.jsonl --fixture fixture.json [-c script | file]
sash --skill [features | language | verbs]      print the embedded Claude skill
sash --skill --list                             list the embedded skill files
sash version --json                             what this build is, one JSON line
sash codes --json                               the catalog of what sash prints when it refuses or fails
```

Without `--session` the grant is empty: verbs are denied and filters still run. `--session` reads the grant and the verb socket from fds 3 to 6, as the systemapi agent or `adssh` sets them up.

## Build and test

```text
./build.sh            bin/sash (SASH_TAGS=sashwasm for wasm modules)
./build.sh test       gofmt, vet, tests and -race, with and without sashwasm
```

The release binary is built without the `sashwasm` tag.

## As a login shell

sash can be an account's shell. It then behaves exactly as a hand start: an empty grant, where verbs are denied and filters run, and one notice on stderr at the prompt (`sash: no session grant: verbs are denied, filters run`). `-l` and `--login` are accepted as the first argument and ignored, and an `argv[0]` of `-sash` changes nothing. `sshd` running `$SHELL -c "<command>"` is the ordinary `-c` path.

It reads no `/etc/profile`, `~/.profile`, rc file, or `ENV` file, takes no environment variable as a name, and executes nothing: `scp`, `sftp`, `rsync`, and `git` over ssh into such an account are exit 127 `unknown`, by design. No error falls through to another shell. It opens no connection and asks no agent for a grant or a lease, and it is the same as root. A person with root adds the binary's path to `/etc/shells` and runs `chsh`; sash never edits either file.

## Version and codes

`sash version --json` prints one object: the module version, commit and whether the tree was modified (from the binary's own build information), OS and architecture, the grant and mux versions, whether wasm is built in, the number of forms in the closed table, and the number and SHA-256 of the catalog's codes. It is a claim the binary makes about itself: for display, never to gate anything. `sash codes --json` prints the catalog (`internal/codes`): each code with its exit status, area, meaning, and fix. A code is a token; where sash prints a sentence, the meaning quotes it. Both take exactly those two arguments, read no grant, and open no file. Every fix that names a change has a person, the operator, or an admin as its subject; a test scans the fixed messages of the language, grant, session, and wire packages against the catalog, and another pins every sentence in `internal/codes/testdata/codes.golden.json`.

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
