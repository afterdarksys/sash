# sash `z` and `jump`: contract

2026-11-22. This contract implements item 5 of `2026-11-22-sash-language-v7.md`, whose direction Ryan approved. It covers the protocol and storage details that item left open. Two branches: sash `feat/v7` and systemapi.io `feat/sash-dirs`, the agent side.

## What the person sees (prompt only)

- **`z <part>...`** changes to the best match among visited directories. A directory matches when it contains every part, in order, as substrings. Matching is case-sensitive.
- **Ranking ("frecency"):** a directory's visit count, weighted by how recent each visit is:
  - ×4 within the last hour
  - ×2 within the last day
  - ×1 within the last week
  - ×0.25 after that

  A tie goes to the most recent visit.
- **`z` failures:**
  - No candidate: exit 1 `z: no match`.
  - The best candidate is outside the grant's `file_read` roots: it is skipped and the next one is tried. Candidates are never shown or used across roots.
- **`z -l <part>...`** lists the matching candidates, as rows `dir`, `score`, `last`, inside the roots only. It does not change directory.
- **`jump <name>`** changes to a saved bookmark.
  - **Bookmark names:** `[a-z0-9_-]{1,32}`.
  - **Root check:** the target is checked against the `file_read` roots every time it is used. Outside them, `jump` exits 126 `denied file.read`.
  - **Missing name:** exit 1 `jump: no bookmark <name>`.
- **Bookmark commands:**
  - `jump save <name>` stores the current prefix. With no prefix it exits 2 `prefix_unset`. A store holds at most 64 bookmarks.
  - `jump list` shows rows `name`, `dir`.
  - `jump rm <name>` removes one.
- **What records a visit:** `cd`, `pushd`, `z` and `jump`, after each succeeds. A private session records nothing.
- **Prompt only:** both are session commands. In a script or `sash -c` they exit 2 `z: prompt only` or `jump: prompt only`. They appear in `builtin` as kind `session`. `--needs` and `--lint` report a script use as that error.

## Agent: a `history` sub-verb (no new verb key)

The existing `history` verb (`sash_verb.go`, `sash_history.go`) gains these forms. Each runs under the same rules as `history get` and `history append`:
- the key is sha256(actor, host) from the verified grant;
- a private session (`AllowPrivate`) reads nothing and writes nothing;
- an empty history root writes nothing;
- nothing is logged.

| argv | body | result |
| --- | --- | --- |
| `dirs` | none | rows `dir`, `count`, `last` (unix seconds), at most 500, newest first |
| `visit` | the absolute clean directory (the sash path invariants) | bytes, exit 0 |
| `marks` | none | rows `name`, `dir` |
| `mark` | `name\tdir` | bytes, exit 0 (replaces any bookmark with that name) |
| `unmark` | `name` | bytes, exit 0 (removing a name that is not there is not an error) |

**Storage.** Two files sit next to `<id>.suggest`: `<id>.dirs` and `<id>.marks`. They are written the way the suggestion file is: same lock, same open helper (no symlinks), `0600`, replaced atomically, each line JSON with unknown fields refused. A file that is malformed or oversized returns no rows.

**Limits.**
- 500 directories. On overflow the lowest score is dropped first, then the oldest.
- 64 bookmarks.
- A directory path is at most 4096 bytes.
- A path fails the sash checks (not absolute, not clean, a control or format character, invalid UTF-8): the call is denied and nothing is written.

The agent does not check roots. sash checks them on every use, and the grant is what decides. Storing a directory reveals nothing the actor did not already see.

**No new capability.** An agent without these forms denies them, as it denies any unknown history argv, and sash takes the denial as "no store". `z`, `jump` and visit recording then fall back to the session alone: `z` ranks this session's visits, bookmarks last until logout, and sash prints one notice per session, `z: history store unavailable, this session only`.

**Recording.** `z` and `jump` change the prefix through `lang.MoveTo` (same path checks, grant check and stat as `cd`, with no line of source made from a stored value). A session that is recording refuses them with exit 2 `z: not while recording`, because its replay runs lines and these are not lines.

## Threats

- **z cannot widen access.** Every target goes through the same root check as `cd`. A stored directory outside today's roots is never shown and never used.
- **A script cannot depend on someone's history.** Both commands are prompt-only.
- **A bookmark cannot carry a command.** It is a path value, checked with `makePath`, so it is never parsed as source.
- **A bookmark name is never a path component.** No `/`, `.` or `..` reaches the store as a name.
- **Private sessions leave no trace,** as with history today.
- **Recording and replay.** The store calls are the editor's, like history get and append, and are never part of a recording.

## Tests (both ways)

- **sash:**
  - ranking;
  - in-order part matching;
  - out-of-root skips;
  - the `-l` rows;
  - bookmark name rules;
  - prompt-only refusals;
  - the private session;
  - the fallback without the capability;
  - visit recording after a successful `cd` only.
- **Agent:**
  - each argv form;
  - private sessions;
  - malformed, oversized and symlinked files;
  - the limits;
  - path refusals;
  - the store keyed only by the grant.

## Build order

1. Agent sub-verbs and the capability, with tests: systemapi.io branch `feat/sash-dirs`.
2. sash `z` and `jump` against a test double, then against the agent's shared test vectors.
3. Adversarial review of both before any push.
