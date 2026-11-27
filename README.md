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
sash connect [--root] <host>                    a terminal on a fleet host, for a person at a workstation
```

Without `--session` the grant is empty: verbs are denied and filters still run. `--session` reads the grant and the verb socket from fds 3 to 6, as the systemapi agent or `adssh` sets them up.

## Build and test

```text
./build.sh            bin/sash, built -trimpath (SASH_TAGS=sashwasm for wasm modules)
./build.sh test       gofmt, vet, tests and -race, with and without sashwasm
```

The release binary is built without the `sashwasm` tag.

## As a login shell

sash can be an account's shell. It then behaves exactly as a hand start: an empty grant, where verbs are denied and filters run, and one notice on stderr at the prompt (`sash: no session grant: verbs are denied, filters run`). `-l` and `--login` are accepted as the first argument and ignored, and an `argv[0]` of `-sash` changes nothing. `sshd` running `$SHELL -c "<command>"` is the ordinary `-c` path.

It reads no `/etc/profile`, `~/.profile`, rc file, or `ENV` file, takes no environment variable as a name, and executes nothing: `scp`, `sftp`, `rsync`, and `git` over ssh into such an account are exit 127 `unknown`, by design. No error falls through to another shell. It opens no connection and asks no agent for a grant or a lease, and it is the same as root. A person with root adds the binary's path to `/etc/shells` and runs `chsh`; sash never edits either file.

## Version and codes

`sash version --json` prints one object: the module version, commit and whether the tree was modified (from the binary's own build information), OS and architecture, the grant and mux versions, whether wasm is built in, the number of forms in the closed table, and the number and SHA-256 of the catalog's codes. It is a claim the binary makes about itself: for display, never to gate anything. `sash codes --json` prints the catalog (`internal/codes`): each code with its exit status, area, meaning, and fix. A code is a token; where sash prints a sentence, the meaning quotes it. Both take exactly those two arguments, read no grant, and open no file. Every fix that names a change has a person, the operator, or an admin as its subject; a test scans the fixed messages of the language, grant, session, wire, line editor, connect, and command packages against the catalog, and another pins every sentence in `internal/codes/testdata/codes.golden.json`.

## Connecting to a host

`sash connect <host>` runs on a person's workstation. It is the client half of the control plane's existing terminal protocol, which it calls "sah/3" and names in its `User-Agent`; nothing is negotiated, and a server that does not answer as that protocol does is refused (`connect: the server does not speak sah/3`). It resolves the host (`GET /api/v1/hosts/resolve`), asks for a ticket for `terminal:open`, or `terminal:root` with `--root` (`POST /api/v1/ws-ticket`), and opens the terminal WebSocket with that ticket, which is used once. The host's agent still checks the signed grant and starts the host's sash. The client holds no grant and no key, records nothing, and never speaks to an agent.

It is an argument of the process and nothing else: no script, `-c` line, function, or session can reach it, and behind any flag it is the usage line. It takes no script, and stdin, stdout, and stderr must all be terminals. Scripts go through the control plane's jobs path, which has its own gate.

It reads two environment variables and no other. `SYSTEMAPI_URL` is the control plane's `https` address, with no user, query, or path. `SYSTEMAPI_TOKEN_FILE` is the absolute path of the file the systemapi CLI's login writes; sash does not log in. The file is opened without following a symlink and must be a regular file owned by this user, with no group or other permission bit, at most 4096 bytes, holding one token. The token is sent only as the bearer of the two HTTPS calls: never on the WebSocket upgrade, in a URL or argv, or in anything printed. TLS is 1.3 or later with the platform's own roots (on Linux, the platform's bundle files, read by sash itself; on macOS, the platform verifier), which `SSL_CERT_FILE` and `SSL_CERT_DIR` do not change, no redirect is followed, and no proxy is read from the environment; sash has no option that changes any of these.

A refusal is printed and the client stops. With `--root` it asks for `terminal:root` and nothing else, and a refusal of it is final: it never asks again for another purpose. Everything the client prints is its own fixed text, and the host's name only when it matches the control plane's own hostname rule (otherwise the host id alone). A code the control plane or the host sends is shown only when it is a plain token, with this build's meaning for it (area `connect` of `sash codes --json`), and the server's own words are never shown. The host's terminal output is filtered, always: text and the cursor, colour, and erase sequences pass; window titles, clipboard writes (OSC 52), hyperlinks, DCS, APC, PM, and SOS strings, and every sequence that makes the terminal answer are dropped. After Enter, `~.` closes the session here and `~~` sends one `~`. The terminal mode is put back on every way out, SIGTERM and SIGHUP included, and on every way out the client resets the terminal modes a host may set: the character set, the keypad and cursor-key modes, autowrap, cursor blink and visibility, the scroll region, colour and style, the alternate screen, and bracketed paste.

The exit status is the host shell's own; 2 for usage, the environment, the token file, or no terminal; 1 for a failed request, a server that does not speak sah/3, or a lost connection; 126 for a refusal; 130 for Ctrl-C before the session starts, or a SIGINT sent to the client during it (in the session Ctrl-C is the host's input).

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
