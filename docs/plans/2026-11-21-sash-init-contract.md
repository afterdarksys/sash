# sash as the init script language: contract

2026-11-21. Status: decided (see the two Decisions sections); phase 1 built on `feat/init-phase1` (see "Phase 1 build decisions").

## Goal

Ryan is writing a systemd replacement (called **the init** here until it has a name). Its services should be written in sash. The init itself is not in this repository yet. When it arrives, the work is to wire it to this contract and fix what is missing.

This contract defines the sash side so it can be built and tested now, with a test double standing in for the init, as the agent is today.

## Decisions (Ryan, 2026-11-21)

1. **The init does all process work.** sash never forks, execs or signals anything itself. The init offers sash a small set of new verbs over the existing verb socket. sash checks each one against the grant, the init checks it again and performs it, and plan mode, `--needs`, `--lint`, recording and audit apply to them as to every other verb.
2. **Each service runs under a signed boot policy stored on disk.** No control plane is needed at boot. The policy is a grant signed offline by the fleet key, bound to one host and one service.
3. **Both declarative and scripted.** A service file is one sash script:
   - a static `service { }` block the init reads without running anything;
   - optional lifecycle functions (`fn start()`, `fn stop()`, `fn reload()`, `fn check()`) for the parts that need logic.

   A service with no functions is purely declarative.

## The parent is the init

The Wire section of `2026-11-06-sash-design.md` is unchanged: fd 3 is the grant, fd 4 the fleet key, fd 5 the emergency key, fd 6 the verb socket, and `sash --session` is the only argv that reads them. The init opens them exactly as the agent does. sash cannot tell which parent it has, and does not need to.

New argv, which composes with `--session`:

| Command | Meaning |
| --- | --- |
| `sash --describe file` | Prints the `service { }` block as JSON, plus the `--needs` rows of every lifecycle function. Runs nothing, takes no grant, calls no parent. Exit 0 complete, 1 incomplete (a dynamic target), 2 parse error or no `service` block. |
| `sash --session --entry start file` | Runs the top level (definitions only; see below), then calls `fn start`. Likewise `stop`, `reload` and `check`. An entry the file does not define is exit 2 `entry_missing`. |
| `sash --plan --entry start file` | Plan mode, as today. `plan: would spawn ...`. |

In a file with a `service` block, top-level statements may only be `service`, `args`, `fn`, `let` (literals), `declare` and `use`. Anything else is exit 2 `service_top_level` at parse time, so `--describe` and `--entry` never run stray statements.

## The `service` block

First top-level statement (before `args`), literal values only, like `args`:

```sash
service {
  name nginx
  description "Web server"
  requires network-online
  after network-online syslog
  wants metrics-agent
  user www
  group www
  run /usr/sbin/nginx "-g" "daemon off;"
  ready tcp 127.0.0.1:80 timeout 30s
  stop signal TERM timeout 20s
  restart on-failure delay 2s burst 5 within 60s
}
```

| Key | Meaning |
| --- | --- |
| `name` | `[a-z0-9][a-z0-9_.-]{0,63}`. It must equal the grant's `service` field. |
| `requires`, `after`, `wants` | Other service names. The init orders and fails on these. sash only records them. |
| `user`, `group` | Default identity for `spawn`. |
| `run` | Program and its arguments, for a purely declarative service. The init spawns it itself. When `fn start` is defined, `run` is not allowed (`service_run_and_start`). |
| `ready` | `tcp HOST:PORT`, `unix PATH`, `file PATH`, `notify` or `none`, then `timeout D`. The init waits on it after start. |
| `stop` | Signal and timeout before the init sends KILL. |
| `restart` | `never`, `on-failure` or `always`, plus `delay`, and `burst N within D`. |

Unknown keys, duplicates, non-literals and bad values are exit 2 at parse time.

## New verbs (performed by the init)

Every new verb has an entitlement key, a target the grant must list, and a mutates flag. All of them appear in `verbs.md`, `--needs`, `--lint` and plan mode.

| Form | Key | Target checked | Mutates | Result |
| --- | --- | --- | --- | --- |
| `spawn NAME PROGRAM args... [opts MAP]` (MAP keys: `user`, `group`, `cwd`, `umask`, `env`, `limits`, `caps`, `stdout`, `stderr`) | `svc.spawn` | PROGRAM in `exec` | yes | row `name, pid, started_at` |
| `signal NAME SIG` | `svc.signal` | NAME was spawned by this service | yes | exit 0 |
| `wait NAME timeout D` | `svc.wait` | NAME was spawned by this service | no | row `name, exit, signal, ended_at`; exit 2 `timeout` |
| `proc NAME` | `svc.proc` | NAME was spawned by this service | no | row `name, pid, state, started_at` |
| `service status NAME` | `svc.status` | any service | no | row `name, state, since, pid` |
| `probe tcp HOST:PORT` / `unix PATH` / `file PATH` / `notify NAME` | `svc.probe` | `file_read` for paths; tcp is loopback unless `allow_private` | no | exit 0 when ready; usable inside `await` |
| `mkdir PATH [mode M] [owner U:G]` | `file.write` | `file_write` | yes | exit 0, idempotent |
| `chown PATH U:G`, `chmod PATH M`, `touch PATH` | `file.write` | `file_write` | yes | exit 0 |
| `ln PATH TARGET` (symlink) | `file.write` | both in `file_write` | yes | exit 0 |
| `rm PATH` (a file or empty directory, never recursive) | `file.write` | `file_write` | yes | exit 0 |
| `mount SRC DIR [type T] [opts O]`, `umount DIR` | `fs.mount` | DIR in `mounts` | yes | exit 0 |

Rules:

- **Names, not pids.** `signal`, `wait` and `proc` take the name given to `spawn`. The init keeps a name-to-process table per service, so a script cannot signal another service's process or an arbitrary pid.
- **No shell.** `spawn` takes a program and an argument list. There is no shell string or `PATH` lookup; PROGRAM must be absolute and listed in the grant.
- **Options are one map after `opts`**, a `[ ]` data value, never parsed as source. `with` is not reused: it already scopes `set` flags and caps.
- **`env` values are data.** A secret bound with `declare -s` may be passed only as an `env` value. That is a new, single exception to "a secret cannot be a verb argument": the init writes it to the child's environment and never logs it, and the frame and recording carry it redacted. A secret anywhere else in `opts` or in the arguments stays exit 2 `secret_refused`.
- **`caps` and `limits` are capped.** They must be within the grant's `caps` list and the init's ceilings.
- **Plan mode** prints `plan: would spawn nginx /usr/sbin/nginx as www:www` and runs nothing. Reads such as `proc`, `probe` and `service status` still reach the init.

## The boot grant (`sash/2`)

A boot grant is a `sash/2` document: every `sash/1` field, plus the fields below. `sash/1` keeps working, unchanged, for the existing purposes.

| Field | Meaning |
| --- | --- |
| `purpose` | `service`. It verifies with the fleet key (fd 4), like `terminal:*`. |
| `service` | The service name. It must equal the `service` block's `name`, else `grant_service_mismatch` before anything runs. |
| `actor` | `service:<name>`. |
| `exec` | Absolute program paths `spawn` may start. |
| `caps` | Linux capabilities `spawn` may grant. |
| `mounts` | Mount points `mount`/`umount` may touch. |
| `mount_sources` | What `mount` may mount: a filesystem name (`tmpfs`) or a path, equal to or beneath which SRC must lie. |
| `env_names` | The environment names `spawn` may set (`[A-Z][A-Z0-9_]{0,63}`); a name on the denylist is refused even when listed. |
| `user`, `group` | The only identity `spawn` may run as. The service block's `user` and `group` must equal them (`grant_identity_mismatch`); `root` only when the grant says so. |

The signed message is the `sash/1` message with the prefix `sash-grant-v2\n` and the new fields appended in this order: `service`, `exec`, `caps`, `mounts`, `mount_sources`, `user`, `group`, `env_names`, `serial`. A v1 grant cannot verify as v2, and the reverse.

**Making one:**
1. `sash --describe --grant-request file` prints an unsigned `sash/2` body built from the literal needs.
2. A fleet tool outside sash (systemapi or `sash-fleet-pubkey`'s signer) signs it for one host.
3. The init stores it as `/etc/<init>/grants/<name>.grant`, root:root 0600, and passes it on fd 3.

**Expiry and revocation:** decision B1 below (never expires; revoked by serial floor).

## Exit statuses the init acts on

| Exit | `start` | `stop` | `check` |
| --- | --- | --- | --- |
| 0 | started (the init then waits on `ready`) | stopped | healthy |
| 1 | failed (restart policy applies) | failed (the init sends KILL after `stop` timeout) | unhealthy |
| 2 | configuration or grant error: the init does **not** restart; it marks the service failed and logs the stderr token | same | same |
| 3 | not ready in time (`await` timeout inside `start`) | n/a | n/a |

`check` runs under the `await` rules: read-only, and mutations are refused even in plan mode.

## Example

```sash
service {
  name app
  requires postgres
  after postgres network-online
  user app
  group app
  ready tcp 127.0.0.1:8080 timeout 60s
  stop signal TERM timeout 30s
  restart on-failure delay 5s burst 3 within 120s
}

fn start() {
  mkdir /run/app mode 0750 owner app:app
  declare -s dbpass = (cat /etc/app/db.secret)
  await timeout 30s every 1s { service status postgres |> where state == ready }
  spawn main /opt/app/bin/server "--port" 8080 opts [ {"env": {"DB_PASS": $dbpass}, "stdout": "journal"} ]
}

fn stop() {
  signal main TERM
  try { wait main timeout 25s } catch e { signal main KILL }
}

fn check() {
  probe tcp 127.0.0.1:8080
}
```

## Build phases

1. **Phase 1, sash only (buildable now):**
   - parser: the `service` block and the top-level restriction;
   - `--describe`, `--entry` and `--grant-request`;
   - the `sash/2` grant (shape, signature, `grant_service_mismatch`);
   - the new verbs in the closed table with their keys, targets, plan output, `--needs` and `--lint` rules;
   - a test double for the init side of the verb socket;
   - the skill and `verbs.md` updated.

   Tests cover every refusal: an unlisted program, another service's process name, a shell string, a secret outside `env`, a v1/v2 signature swap, a service-name mismatch, and a top-level statement in a service file.
2. **Phase 2, with the init:** the init implements the parent side (grant files, the fds, the verb handlers, the name-to-process table, restart and readiness from the `service` block, ordering from `requires`/`after`/`wants`), then the end-to-end tests. This is where Ryan's code comes in.

## Decisions (Ryan, 2026-11-21, second round)

- **B1. A boot grant never expires**, on bare metal, in containers and in VMs: a service must always be able to boot. `exp` is `0`, which means "never", and only purpose `service` may carry it; any other purpose with `exp` 0 is `grant_bad_shape`. Time cannot revoke a boot grant, so revocation is explicit:
  - each grant carries a `serial`;
  - the init keeps a floor per service in a root-only file;
  - a grant below the floor is refused before sash starts.

  The serial is signed. Raising the floor needs root on the host, or a new signed floor document; which of the two is a phase 2 question with the init.

  **Research item:** whether containers and VMs, being short-lived and rebuilt from images, should carry a boot grant at all, or receive one at instance start from the control plane. Bare metal keeps the never-expiring grant either way.
- **B2. sash runs as a per-service unprivileged user** under the init, never as root. A parser or interpreter bug in sash then reaches only what that service's grant allows, and every privileged step (spawn as another user, mount, chown) is done by the init after its own check. The grant's `actor` is `service:<name>`; the Unix user is the init's choice and is not in the grant.
- **B3. Verb names as proposed:** `spawn`, `signal`, `wait`, `proc` and `probe`.
- **B4. Configurable.** For a service with `run` and no `fn start()`, the init either:
  - spawns `run` directly from the `--describe` output; or
  - starts `sash --session --entry run`. `run` is an implicit entry that does `spawn main <run...>` under the service's user and group, so the run is audited like any scripted one.

  The setting belongs to the init, globally with a per-service override. sash supports both and does not know which is in use. The plan is to measure both and choose a default.

## `sash/2` additions from these decisions

| Field | Meaning |
| --- | --- |
| `exp` | `0` means never, allowed only for purpose `service`. |
| `serial` | Unsigned 64-bit integer, at least 1. It is in the signed message, last. |

Phase 1 covers both: the shape and signature rules, and `grant_exp_zero` for another purpose. The serial floor itself is enforced by the init in phase 2. sash only prints the serial in `--describe --grant-request` output and checks that it is present.

## Reconciliation with sahinit (2026-11-23)

The init now exists: **sahinit** (`afterdarksys/sash-init`). Pushed branches: `fix/hardening-1`, `feat/policy-lifetime`, `feat/policy-renew` and `feat/cgroups`. Phase 1 stays sash-only, against a test double. These points fix how the two fit together:

- **Two documents, two keys, never conflated.**
  - **sahinit's boot policy** (`sahinit-boot-policy/2`, signed by the boot-policy key) admits the host's service catalog.
  - **A service's `sash/2` grant** (signed by the fleet key) authorizes that service's verbs.

  sahinit's own design forbids treating one as the other. A service runs only when both admit it.
- **Lifetime.** `sash/2` boot grants follow decision B1 (never expire, signed serial). That matches sahinit's default `never` lifetime with its signed revocation floor. The `sash/2` serial floor is a per-service floor that sahinit enforces in phase 2, in the same store and with the same rules as its policy floor (monotonic, signed, fail closed).
- **Units.** sahinit units are strict JSON. In phase 2, sahinit derives a service's unit (identity, ordering, readiness, stop, restart) from `sash --describe`, or checks a hand-written unit against it. Phase 1 makes the `--describe` JSON stable and documented for that purpose.
- **Identity and confinement.** sash runs as a per-service, non-root user (B2), sandboxed and in the service's cgroup. That puts it inside sahinit's cgroup guarantees (`feat/cgroups`): it is not `cgroup_trusted`. `spawn` children go into the same service cgroup.
- **Named streams (sash v7 item 6)** are part of phase 1. `spawn ... opts [ {"stdout": S, "stderr": S} ]` takes `journal`, `null`, `file:<path>` (a path in `file_write`), or, for stderr only, `stdout`. `output NAME [stderr] [tail N]` (key `svc.output`, read-only) reads what the init captured, as rows.

## Phase 1 build decisions (2026-11-23)

What phase 1 settled where the contract left a choice. Items marked **confirm** want Ryan's word before phase 2 builds on them.

1. **Grant shape** (field order and the identity and mount-source fields revised by the review, item 21). `sash/2` is purpose `service` only, and purpose `service` is `sash/2` only. Its `exp` is `0` (never) or a real time, which then expires as usual. `exp` 0 on any other purpose is `grant_exp_zero`, checked with the shape before the signature; for `sash/1` that was `grant_expired` before. `exec` and `mounts` are clean absolute paths (not `/`), matched exactly, never as a prefix. `caps` are `CAP_[A-Z0-9_]{1,32}`. `serial` is a uint64, at least 1. `actor` must be `service:<service>`. Every `sash/2` field is required: a missing one is `grant_bad_shape`, not a zero value.
2. **When the service name is checked.** Under `--entry`, before expansion and parse: the file must have a `service` block, and a delivered grant must be `sash/2` for that name (a `sash/1` grant is a mismatch too). A hand start (no grant) skips the check, so `--plan --entry` works offline, but every verb is then denied. A boot grant without `--entry` is exit 2 `entry_missing`.
3. **`fn check`.** `check` is already a builtin (`check "name" [ ]`). In a service file `fn check()` at the top level is the lifecycle entry and is kept aside, so the builtin keeps its meaning inside the file. Lifecycle functions take no parameters, and `fn run` is refused: `run` is only the implicit entry.
4. **Top level.** `let` and `declare` there take only a literal word or a `[ ]` of literals (lists and maps of literals, no names, no function calls); `declare` needs a name. `use` is allowed. A `use` file may define the entry; that is checked when the entry is called (`entry_missing`).
5. **Options on the wire** (**confirm**). `spawn`'s options travel as one JSON object in the frame's `body` (base64, as for `file.write`); there is no new frame field. sash writes the grant's `user` and `group` (which the block must match) into it when the script does not set them. An `env` secret is `{"secret": "<value>"}`: the init must receive the value to set it, so the frame carries it, marked so the init never logs it. "Redacted" is read as: never in the plan line, a trace, a recording (the whole request is redacted, with no body hash) or the init's audit.
6. **Where a secret may go.** Only as a value of a literal `env` map inside a literal `opts` map: each value there is evaluated on its own, so the secret is known to be that value. An `env` map or `opts` held in a name is evaluated whole, and a secret anywhere in it is `secret_refused`. A secret whose `***` fails a service verb's shape check is reported as `secret_refused`, not usage.
7. **File verbs on the wire.** `mkdir`, `chown`, `chmod`, `touch`, `ln` and `rm` are key `file.write` with the operation as `argv[0]` and the first path as `path`. The systemapi agent accepts only `argv [">", path]` on `file.write`, so it denies them: none can become an empty write. Inside `atomic` they are `atomic_irreversible`, as are `spawn`, `signal` and `mount`.
8. **Arguments.** No service verb takes a flag (a word starting with `-` is usage), except a `spawn` argument; the word `opts` in `spawn` is only the marker before a `[ ]` map. Paths follow the v7 rules (literal text or a path value in a script) and are made absolute and clean against the prefix before the grant check, so the init gets the checked path. Process names are `[a-z0-9][a-z0-9_.-]{0,63}`; signals are HUP INT QUIT KILL USR1 USR2 TERM CONT STOP WINCH; `limits` keys are nofile, nproc, core, memlock, stack, as, cpu, fsize with non-negative integers; modes are three octal digits, or four with a leading 0 or 1.
9. **`probe`.** HOST is an IP literal or `localhost`, never a name lookup. Without `allow_private` only loopback; with it also private and link-local; never a public address. A `unix` or `file` PATH needs a `file_read` root, not the `file.read` key. Not ready is exit 1.
10. **`mount`.** DIR is exactly one of `mounts`. SRC is an absolute clean path or a bare name (`tmpfs`, `proc`). `type` and `opts` are checked for shape only; the ceilings are the init's.
11. **The init's refusals.** `unknown_name`, `name_in_use`, `denied` and `ceiling` print `refused <key>: <code> (<why>)` and exit 2; `wait`'s `timeout` prints `timeout`, exit 2. Any other init error is exit 1 `parent <key>`.
12. **Entry exit statuses.** 0 stays 0. `start` that fails after an `await` ran out is 3. 2, 126 (denied) and 127 (unknown) are 2. Everything else, `exit N` and a hangup included, is 1.
13. **`check`** runs with the await polling rules on and no condition depth, so every mutation is refused and plan mode does not loosen it.
14. **Plan line.** `plan: would spawn NAME PROGRAM args... as U:G`: the arguments are printed (quoted when they hold a space), the options never. The file verbs print their own line (`plan: would mkdir /run/app mode 0750 owner app:app`).
15. **`--describe`.** The document is `"v": "sash-describe/1"`, pretty-printed: `service` (durations as `*_ms` integers, empty lists `[]`, absent `ready`, `stop`, `restart` as `null`), `entries` in the order start, stop, reload, check, run, `needs` per entry (key, target, mutates, lines, note), and `complete`. An entry's needs follow every function it calls by literal name; a top-level `use` adds a `use: not followed` row to every entry. The golden file is `cmd/sash/testdata/app.describe.json`.
16. **`--grant-request`.** The unsigned `sash/2` body in signed field order. Entitlements come from every row's key; `file_read`, `file_write`, `exec`, `caps` and `mounts` only from rows without a note, as exact paths, not parent directories. `allow_private` is set by a probe of a private address. `host` and `lease_id` are empty and `serial` 0 for the signer, so the request never verifies as printed. Exit 1 when incomplete.
17. **`--needs` rows.** A spawn's capabilities are `svc.spawn cap NAME`, options from a name `svc.spawn opts` (dynamic), a probe `svc.probe KIND TARGET`, a `file:` stream a `file.write`. No systemapi flag grants the boot keys, so `--policy` prints a comment for each.
18. **Lint.** `spawn-shell` (warning): a `spawn` or `run` of sh, bash, dash, zsh, ash, ksh, busybox or env. `check-mutates` (error): `fn check` reaches a mutation.
19. **Globs.** A run still lists every glob in the file before the top level runs, as for any script, so a glob in a service function is a host read at load. Superseded by item 21: a service file with a glob is `service_glob`.
20. **Test double.** `prototest.Init` keeps a name-to-process table per service, fake processes, probe readiness and file and mount tables, and re-checks every verb against its own copy of the grant. `prototest.Agent.Init` routes the service verbs to it over the real pipe, serial and mux.

21. **Review fixes (2026-11-23).**
    - **Identity.** `sash/2` signs `user` and `group` (required, user and group names). The block's `user`/`group`, when set, must equal them, else `grant_identity_mismatch` before anything runs. `spawn`'s `user`/`group` opts may only name them (else exit 126 `denied user` or `denied group`); omitted means the grant's, and sash always sends both. A numeric id cannot be named. `--grant-request` copies the block's identity.
    - **Environment.** `spawn` env refuses `LD_*`, `DYLD_*`, `MALLOC_*`, `GCONV_PATH`, `LOCPATH`, `NLSPATH`, `HOSTALIASES`, `RES_OPTIONS`, `GLIBC_TUNABLES`, `TZDIR`, shell startup and option names (`BASH_ENV`, `ENV`, `IFS`, `SHELLOPTS`), and interpreter search paths and hooks (`PYTHONPATH`, `PYTHONHOME`, `PYTHONSTARTUP`, `PERLLIB`, `PERL5LIB`, `PERL5OPT`, `RUBYLIB`, `RUBYOPT`, `NODE_OPTIONS`, `NODE_PATH`, `JAVA_TOOL_OPTIONS`, `_JAVA_OPTIONS`, `JDK_JAVA_OPTIONS`, `LUA_PATH`, `LUA_CPATH`): exit 2 `env_refused: NAME`.
    - **Mounts.** `sash/2` signs `mount_sources`. SRC must equal a listed name, or be a clean path equal to or beneath a listed path (`denied mount source`). `bind`, `rbind` and `remount` (as `type` or in `opts`) need a path SRC. DIR may not be at or under a `file_write` root, nor hold one (`mount_overlap`), so a mount never puts another tree under the write roots. `--grant-request` lists the literal sources.
    - **The init re-checks, never trusts sash:** the exec list, caps, the identity, the env names, the mount source and overlap, and every path's roots. The double (`prototest.Init`) does each.
    - **Symlinks.** `mkdir`, `chown`, `chmod`, `touch`, `ln` and `rm` must not follow a symlink at any component of PATH, the last included. The init resolves PATH beneath its `file_write` root with `openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS)` and acts with `fchownat(AT_SYMLINK_NOFOLLOW)`/`lchown`, `fchmodat` on the opened fd, and `unlinkat`; `ln`'s TARGET is only the link's text. The double refuses a path with a symlink component (`symlink`).
    - **Globs.** A service file (one whose first statement is a `service {` block) with any glob or brace pattern is exit 2 `service_glob`, checked before expansion, in a run, `--describe`, `--needs` and `--lint`. This replaces item 19.
    - **Names.** A process name starts with a letter (`[a-z][a-z0-9_.-]{0,63}`), so it never reads as a pid.
    - **Boot keys.** `svc.*` and `fs.mount` are denied unless the grant is `sash/2` purpose `service`, even when a `sash/1` grant lists them.

22. **Re-check fixes (2026-11-23): allowlists, not denylists.** The tables live in `internal/grant/policy.go`; sash and the init double both use them, and the init must apply the same rules.
    - **Mounts.** The type is exactly one of `tmpfs`, `proc`, `sysfs`, `devpts`; a bind is never a type, only the option `bind` or `rbind`, and takes a path SRC (a type takes a name). Options are exactly, lowercase: `ro`, `rw`, `nosuid`, `nodev`, `noexec`, `bind`, `rbind`, `remount`, `size=N` and `nr_inodes=N` (digits, optional `k`, `m`, `g` or `%`), `mode=OCTAL` (3 or 4 digits), `uid=N`, `gid=N`. Anything else (`overlay`, `lowerdir=`, `suid`, `dev`, `exec`, `x-bind`, `move`, `BIND`) is exit 2 `mount_opt_refused`. sash always adds `nosuid,nodev` and the argv the init gets lists every option; the init refuses a mount without both. The init resolves a path SRC and the mount point with `RESOLVE_NO_SYMLINKS`.
    - **Environment.** The allowlist is the signed `env_names`; the item 21 denylist stays as a second gate, extended with `OPENSSL_CONF`, `OPENSSL_ENGINES`, `CLASSPATH`, `PYTHONWARNINGS`, `PERL5DB` and the `GIT_` prefix. `PATH` is not on the denylist: like any name it is allowed only when `env_names` lists it, so a signer decides it explicitly. `--grant-request` fills `env_names` from the literal env keys of spawn calls, never a denied one; an env map from a name is a dynamic `svc.spawn env $` row and the description is incomplete.
    - **cwd** is a clean absolute path (relative to the prefix) at or under a `file_read` or `file_write` root, else exit 126 `denied cwd`; the init resolves it without symlinks.

Deferred to phase 2, with sahinit: the serial floor and its store; the caps and limits ceilings; real processes, the cgroup, `notify` and output capture; `ready`, `restart` and the ordering from `requires`, `after` and `wants`; grant files and the fds; the fleet signer for grant requests; units derived from `--describe`; item 5's secret form; and the container and VM research item.
