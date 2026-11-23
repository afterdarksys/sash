# sash as the init script language: contract

2026-11-21. Status: decided (see the two Decisions sections); phase 1 not started.

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

The signed message is the `sash/1` message with the prefix `sash-grant-v2\n` and the new fields appended in this order: `service`, `exec`, `caps`, `mounts`, `serial`. A v1 grant cannot verify as v2, and the reverse.

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
| `serial` | Unsigned 64-bit integer, at least 1. It is in the signed message, after `mounts`. |

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
