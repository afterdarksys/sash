# sash boot verbs: contract

2026-11-24. Status: decided (see Decisions). Phase 1 (sash, against the double) built on `feat/boot-verbs`; see "Phase 1 build notes". Branch `docs/boot-verbs`, from `feat/init-phase1` at `f28285b`.

## Goal

A host must be able to boot from a sash script: load the drivers it needs, wait for its disks, mount them, bring up its network and start the units that depend on them. Today `docs/reference/operations.md` in sahinit says "sahinit does not discover storage, load modules or configure the network (those are services)". The example units do it with `modprobe`, `udevadm` and `udhcpc` exec units. Nothing checks those programs against a signed grant.

This contract adds six **boot verbs**:
- a sash script asks for each one;
- sahinit checks it against a signed grant and against root-owned host declarations, then performs it itself;
- sahinit re-checks every rule, as it does for the batch 1 and batch 2 verbs.

The governing documents stay in force:
- `docs/plans/2026-11-21-sash-init-contract.md`: the wire, `--describe`, `--entry`, the `sash/2` grant, the floor, and phase 1 build decisions 1 to 23;
- sahinit's `docs/design/sash-services-1.md` and `sash-services-2.md`.

This contract only adds to them. When it says "as for `fs.mount`", the rules there apply unchanged.

Out of scope:
- fsck. A disk check is a unit started with `service start` (below), as `e2fsck` is in the initramfs today.
- udev rules and firmware loading.
- Storage discovery by filesystem UUID, LVM, RAID and encryption.
- DNS configuration (a file, written with the existing file verbs).
- Firewalls. Network apply can wait for a firewall unit (`requires` in a profile), but never configures one.
- Anything after boot (decision D6).

## Decisions

Ryan settled the open decisions on 2026-11-24; they are recorded at the end.

- **D1. One interpreter.** There is no `sash-boot` and no boot-job broker. A boot script is an ordinary sash unit: `sash --session --entry start`, the same fds, the same verb socket, the same recording and audit.
- **D2. A new grant version.** The boot verbs need a signed grant version, `sash/3`, with its own purpose (`boot`), its own domain prefix (`sash-grant-v3\n`) and six new name lists. A `sash/2` grant can never carry a boot verb.
- **D3. The script names, the init declares.** The script only ever passes a **name**: a module name, or a device, mount, network profile or unit name. The grant lists the names. What a name means lives on the init side:
  - for devices, mounts and network profiles, a root-owned host declaration file (`host.json`);
  - for modules, the kernel's own module tree;
  - for units, the catalog.

  A raw source path, mount target, address or interface never comes from the script.
- **D4. sahinit performs everything with kernel interfaces, never by running a helper program:**
  - `finit_module` from the trusted module tree;
  - devtmpfs nodes checked with `openat2` and sysfs;
  - the new mount API;
  - rtnetlink;
  - the supervisor's own start and stop.

  It does not exec `modprobe`, `udevadm`, `ip` or `mount`.
- **D5. The keys are grantable.** Each boot key is in `bootKeys` and has a signed grant field that carries its targets. `--grant-request` fills that field. The "flag is never empty" rule becomes a test: every key has a systemapi flag or a grant field.
- **D6. Boot only.** The init refuses a boot verb after the boot waves end, and outside a `start` entry.
- **D7. A boot-ops unit is a oneshot.** A sash unit that holds a `sash/3` grant prepares the host and ends. Long-running daemons are separate units that it starts by name.

## The verbs

| Form | Key | Grant field | Mutates | Result row |
| --- | --- | --- | --- | --- |
| `module load NAME` | `boot.module` | `modules` | yes | `name, state, deps` |
| `device wait NAME timeout D` | `boot.device` | `devices` | no | `name, path, major, minor` |
| `mount ensure NAME` | `boot.mount` | `host_mounts` | yes | `name, target, fstype, state` |
| `network apply NAME` | `boot.net` | `net_profiles` | yes | `name, link, state, changes` |
| `service start NAME` | `boot.start` | `units_start` | yes | `name, state, since, pid` (as `service status`) |
| `service stop NAME` | `boot.stop` | `units_stop` | yes | `name, state, since, pid` |

`state` is `done` when this call changed the host, and `already` when the host already matched, so every verb is idempotent. A repeated call is never an error.

No other verb is needed:
- a disk check is a unit;
- a sysctl is a file write under `/proc/sys`, which is outside this contract;
- `service status` already exists (`svc.status`).

`module unload` is deliberately absent. Unloading a driver under a mounted disk is never a boot step.

### Syntax and shapes (sash checks them, and the init checks them again)

The shape errors are usage errors, exit 2.

- **Module names** are `[a-z0-9][a-z0-9_]{0,54}`: the kernel's name, at most 55 bytes, with `_` and never `-`. The grant and the script use the canonical spelling. `virtio-net` is `usage module load NAME`, not an alias.
  - No alias is ever resolved, so `pci:v...` and `fs-ext4` are refused.
  - No module parameter is accepted. A `/etc/modprobe.d` option is ignored, and `verbs.md` says so.
- **Device, mount and profile names** are `[a-z][a-z0-9_-]{0,31}`.
- **Unit names** are sahinit's, `[a-z][a-z0-9-]{0,62}`.
- **The device wait timeout** is required and is 1s to 60s. Anything else is `usage device wait: timeout 1s to 60s`.
- **No flags.** No form takes one (a word starting with `-` is usage), as for the service verbs.
- **No secrets.** A secret anywhere in the arguments is `secret_refused`.
- **`mount ensure` against `mount`.** `mount ensure` shares its first word with `mount SRC DIR`. A `mount` stage whose second word is `ensure` is always the boot verb, and it takes exactly one name. So `mount ensure /mnt/x` is `usage mount ensure NAME`. To keep that from hiding a real mount, the grant parser refuses `ensure` in `mount_sources` (`grant_bad_shape`).
- **`service start` and `service stop`** sit beside `service status` (`svc.status`), which is already the init's view of a service. They are not `unit start` and `unit stop`. In sash, `unit` is the systemapi agent's systemd verb (`unit status`, `unit restart`, keys `unit.*`, flag `--service`), and the boot keys must not look like channel keys (decision 1).

### Where boot verbs may run

sash refuses each of these before calling the init, and the init refuses them again:
- **Grant.** The verb needs a `sash/3` grant with purpose `boot`. Otherwise it is exit 126 `denied boot.KEY`, even when a `sash/1` or `sash/2` grant lists the key.
- **Entry.** It must run under `--entry start`. In `stop`, `reload`, `check` or the implicit `run`, it is exit 2 `boot_entry`. An entry is known to sash (`runEntry`) and to the init (the session it launched).
- **`await` and `check`.** It may not run in an `await` body or in `check`. A mutating boot verb is refused there as every mutation is. `device wait` is a wait in its own right and is refused there too, with exit 2 `boot_in_await`.
- **`par`.** It may not run inside `par`: exit 2 `boot_in_par`. Boot order must be the order written.
- **`atomic`.** A mutating boot verb is `atomic_irreversible`. See "atomic".

### Wire

There is no new frame field. Each verb is one `parent.Verb` with these fields:
- `Key`: the boot key;
- `Argv`: as written, with `device wait`'s timeout as whole seconds (`["device","wait","data","timeout","30"]`);
- `Path`, `Body` and `Mode`: empty. The init refuses a request that carries any of them (`denied`).

The response is one row with the columns in the table above. `deps` is the comma-separated list of modules loaded first. `changes` is the number of netlink changes made.

The init's refusals use the existing form `refused boot.KEY: CODE (why)`, exit 2 (phase 1 item 11), with the codes listed below. Any other init error is exit 1 `parent boot.KEY`.

### Per-verb rules: what sash checks, and what the init re-checks

Every verb is checked first against the common gates below, in this order:
1. the key is in `entitlements`;
2. the grant is `sash/3`, purpose `boot`;
3. the unit opted in (`sash.boot_ops`);
4. the session's entry is `start`;
5. the boot waves have not ended (`boot_over`);
6. the per-boot call budget is not spent (`boot_call_limit`);
7. no field outside `Key` and `Argv` is set.

sash checks only gates 1 and 2, the entry, the shapes and the grant list. The init checks everything.

**`module load NAME`.** NAME must be in `modules` (sash: exit 126 `denied module`). The init then:
1. Refuses when `/proc/sys/kernel/modules_disabled` is 1 (`module_disabled`), and when signature enforcement is off: `/sys/module/module/parameters/sig_enforce` is not `Y` and the kernel is not in lockdown (`module_sig_unenforced`). A binary built with the `sahinit_unsigned_modules` tag goes ahead instead and logs `module_unsigned_dev NAME` (decision 2).
2. Opens the module tree `/lib/modules/<release>`, with `<release>` from `uname(2)` and never from the request. It opens it with the trusted walk: root-owned, not group- or other-writable, no symlinks (`module_tree`).
3. Reads `modules.dep` from that tree, at most 8 MiB (`module_tree`). It finds the one line whose file's base name, with the `.ko` and any `.xz`, `.zst` or `.gz` suffix removed and `-` read as `_`, equals NAME. No line is `module_unknown`. A path in the line that is absolute, unclean or contains `..` is `module_tree`.
4. Takes NAME's dependencies from that line, in the order `modules.dep` gives them. It loads them first, each by the same steps. Dependencies come from the root-owned `depmod` output and are not listed in the grant (decision 2). They are reported in `deps`.
5. Loads each module that `/sys/module/NAME` does not already show as live:
   - it opens the file with `openat2(treefd, rel, RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS)` and `O_RDONLY|O_CLOEXEC`;
   - it calls `finit_module(fd, "", flags)`, with `MODULE_INIT_COMPRESSED_FILE` for a compressed file;
   - `EEXIST` is `already`;
   - any other error is `module_failed`, and the errno is never printed.
6. Runs at most one module load at a time host-wide, because `finit_module` cannot be cancelled. When the session's deadline passes during a load, the verb answers `timeout`. The load finishes in the kernel, and a second load of that name is `busy` until it does.

**`device wait NAME timeout D`.** NAME must be in `devices` (sash: `denied device`). The init takes the device's declaration from `host.json`: a `path`, a `kind` (`block` or `char`), and an optional `match` of sysfs attributes. It refuses when `/dev` is not devtmpfs (`device_unsafe`, checked with `statfs` magic), so no unprivileged writer can plant a node. Then, every 50 ms until D runs out:
1. It opens `path` with `openat2(devfd, rel, RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS)` and `O_PATH`. A symlink at any component is `device_unsafe`.
2. It `fstat`s the opened file. The type must match `kind` (`device_unsafe`).
3. It reads `/sys/dev/{block|char}/MAJ:MIN/<attr>` for each `match` attribute: `serial`, `wwid`, `device/serial`, `device/wwid`, `size`, `partition`, `dm/uuid`, at most 256 bytes each. Every one must be byte-equal after trimming. A mismatch is `device_mismatch`. It is reported at once and is not retried: a wrong disk is not a slow disk.
4. On success it records `NAME -> dev_t` for this boot and answers with the row.

When D runs out the answer is `timeout`. The logic is that of `sahinit host device-wait`, moved into a function both share. The helper keeps its command line and gains no new behaviour.

**`mount ensure NAME`.** NAME must be in `host_mounts` (sash: `denied host mount`). The init takes the mount's declaration from `host.json`: `target`, `fstype` (`ext4`, `xfs`, `vfat` or `tmpfs`), a `source`, and `options`. The source is a device name, or `tmpfs` for tmpfs. The options are allowlisted: `ro`, `rw`, `noexec`, `noatime`, `relatime`, `nosuid`, `nodev`, `suid`, `dev`, and for tmpfs `size=` and `mode=` with sash's bounds. `nosuid` and `nodev` are the default, and only the declaration can turn them off. It then:
1. Resolves the target from `/` with `RESOLVE_NO_SYMLINKS`. It must be an existing directory, owned by root and not group- or other-writable (`mount_target`). It must not lie at, under or above a `file_write` root of the calling grant (`mount_overlap`, as for `fs.mount`).
2. For a device source, re-runs the `device wait` identity check once, without waiting. A device that is absent is `device_absent`. A `dev_t` that differs from the one `device wait` recorded is `device_changed`.
3. Checks idempotence from `/proc/self/mountinfo`, bounded to 1 MiB:
   - the target is already a mount point with the same fstype, the same source `dev_t` and the same ro/rw: `already`;
   - anything else mounted there: `mount_conflict`.
4. Mounts with `fsopen(fstype)`. The source is set by `fsconfig(FSCONFIG_SET_STRING, "source", "/proc/self/fd/N")` on the `O_PATH` fd from step 2. Then `fsconfig(FSCONFIG_CMD_CREATE)`, `fsmount` with the attribute flags, and `move_mount` onto the target's fd.
5. Checks the mounted superblock's device with `statx` on the target (`stx_dev_major` and `stx_dev_minor`). If it is not the checked `dev_t`, it detaches the mount and answers `device_changed`.
6. Records the mount in the existing persisted registry (`sash-mounts.json`), with `Service` set to `@host` and `Kind` set to `host`, keyed by mount ID. Host mounts are **not** removed when the boot-ops unit ends, nor at the next sahinit start: they belong to the host. They are removed only at shutdown, as every mount is. `umount` (`fs.mount`) refuses them (`not_mounted`).

**`network apply NAME`.** NAME must be in `net_profiles` (sash: `denied network profile`). The init takes the profile from `host.json`. A profile is one of two kinds:
- **static:**
  - `link`, a MAC address that selects the interface. A kernel name may be given in addition, and must then agree;
  - `mtu`;
  - `addresses`, at most 8 CIDRs;
  - `routes`, at most 16, each a destination and a gateway;
  - `requires`, units that must be ready first.
- **unit:** the name of a unit (a DHCP client) that is started as `service start` starts one, with the same rules except the `units_start` listing. The profile is its grant.

For a static profile the init:
1. Refuses until every `requires` unit is ready (`net_unguarded`). This is how a profile waits for a firewall unit.
2. Finds exactly one link with that MAC over `NETLINK_ROUTE` (`RTM_GETLINK`). None or more than one is `net_unknown_link`.
3. Reads its addresses and routes and computes the difference. It only adds: an address or route that is present and not in the profile is left as it is. Nothing is deleted at boot.
4. Applies the difference with `RTM_NEWLINK` (mtu, then `IFF_UP`), `RTM_NEWADDR` and `RTM_NEWROUTE`, each acknowledged (`NLM_F_ACK`). Replies are bounded to 64 KiB per message and 1 MiB per call.
5. Answers `already` when nothing changed.

The netlink client is written on `golang.org/x/sys/unix`, so sahinit keeps its single dependency. A profile can never:
- set promiscuous mode, forwarding or other sysctls;
- create a bridge, VLAN, tunnel or veth;
- touch a link it does not name.

The socket is the supervisor's, in the host network namespace.

**`service start NAME`.** NAME must be in `units_start` (sash: `denied service start`). The init checks:
- NAME is in the catalog (`not_found`);
- NAME is not the caller (`boot_self`);
- NAME is not itself a boot-ops unit (`boot_chain`), so one boot grant can never reach another;
- NAME has `"boot": false` (`boot_unit_auto`). The boot waves own boot units, and a script racing them is meaningless;
- NAME does not `need` or come `after` the caller, directly or transitively (`boot_cycle`). Otherwise each would wait for the other;
- NAME has not been started by a boot verb before in this boot (`boot_once`).

It then calls the supervisor's `start` with `user=true`, so admission, the `needs` gate, sandbox, cgroup and limits all apply, exactly as for `ctl start`. The call is bounded by the time left in the session. `running` (or `exited` for a oneshot that ended 0) is `already`. The started unit's failure is its own, because it is not essential: `essential` needs `"boot": true`. But the verb's exit tells the script.

**`service stop NAME`.** NAME must be in `units_stop` (sash: `denied service stop`). The init checks `not_found`, `boot_self`, `boot_chain` and `boot_once` as for start. It also refuses an essential unit (`boot_essential`), because a stop would quietly remove a unit that strict boot promises. Then it calls `stop` with `user=true`, so the unit is not restarted. A unit that is already stopped is `already`.

The caller's runtime lock is never held across the target's start or stop. A test starts a unit whose `needs` gate blocks, and checks that the caller's status still answers.

## The grant: `sash/3`

`sash/3` is `sash/2` plus six lists, signed under its own domain:
- **Purpose** is `boot`, and only `boot`. `boot` is `sash/3` only.
- **Actor** is `boot:<service>`.
- **`exp`** may be 0 (never), as for `service`. The rule "exp 0 only for purpose service" becomes "exp 0 only for purposes service and boot". Any other purpose with exp 0 stays `grant_exp_zero`.
- **The signed message** is `sash-grant-v3\n`, then the JSON of these fields, in this order:
  - the `sash/1` fields: `v`, `actor`, `host`, `purpose`, `exp`, `lease_id`, `entitlements`, `file_read`, `file_write`, `allow_private`;
  - `service`, `exec`, `caps`, `mounts`, `mount_sources`, `user`, `group`, `env_names`;
  - **`modules`, `devices`, `host_mounts`, `net_profiles`, `units_start`, `units_stop`**;
  - `serial`, last, as in `sash/2`.

  It is written by Go's `encoding/json` from a struct in that order, with an absent list written as `[]`, exactly as `v2Fields` does. A `sash/1` or `sash/2` signature never verifies as `sash/3`, nor the reverse. The golden vectors pin this in both repositories.
- **Every field is required** (`grant_bad_shape` when missing). Unknown fields are refused, and nothing may follow the document.
- **Each new list** has at most 64 entries and no duplicates. Every entry must match its name grammar above (`grant_bad_shape`). `mount_sources` may not contain `ensure`.
- **Unchanged from `sash/2`:** the 64 KiB cap, the `*` rule, the root rules and `grant_root_slash`.
- **`entitlements` must list each boot key used** (gate 1). A list with no key beside it grants nothing.
- **`sash/2` is untouched.** Its message, its vectors and every signed `sash/2` grant stay valid. A boot-ops unit simply needs a `sash/3` one.

**The floor and serial.** The floor stays `sahinit-sash-floor/1`, one per **service name**, unchanged. `sash/2` and `sash/3` grants for a service share it and share one serial sequence, so raising the floor revokes both. To remove one module from a boot grant, the signer issues a new `sash/3` with a higher serial, and the floor is raised to it. Moving a unit from `sash/2` to `sash/3` raises the floor in the same way. A swap back to the old file is refused twice: by the floor, and by the unit's `boot_ops` opt-in, which requires `sash/3`.

**The host is bound.** A `sash/2` grant's `host` is not checked by sahinit in batch 1. A `sash/3` grant's is: sahinit compares it with the `host_id` of its boot-policy trust file and refuses a mismatch (`sash_grant_host`). With no trust file configured, sahinit refuses every `sash/3` grant (`sash_grant_host`). The comparison is by plain equality: a host id is public, not a secret (decision 3).

**In sash:**
- `grant.Grant` gains the six lists.
- `Boot()` stays true for `sash/2` service and `sash/3` boot: both carry the service verbs.
- A new `BootOps()` is true for `sash/3` boot only.
- `CheckService` accepts both.
- The eval gate becomes: a boot key needs `BootOps()`, and an `svc.*` or `fs.mount` key needs `Boot()`.
- `Request` emits `sash/3` when any `boot.*` row exists. Otherwise it emits `sash/2`, as today.

**The needs flag (D5).** `needsFlag` stays the systemapi flag table, so the boot keys keep `""` there. systemapi must never grant them, and `--needs --policy` prints the existing `sash/2`-style comment, worded "only a sash/3 boot grant carries this". A new table, `bootField`, maps every key in `bootKeys` to the signed field that carries its target:
- `boot.module`: `modules`; `boot.device`: `devices`; `boot.mount`: `host_mounts`; `boot.net`: `net_profiles`; `boot.start`: `units_start`; `boot.stop`: `units_stop`;
- `svc.spawn`: `exec`; `fs.mount`: `mounts`;
- every other `svc.*` key: `entitlements`.

The `needs_test.go` invariant becomes: every key has a non-empty `needsFlag` or a non-empty `bootField`, except `ps` and `journal`, and `bootKeys` equals the key set of `bootField`.

That is the fix for the archive's first fault. Its keys were in neither table, so `--grant-request` could never fill them. Worse, the eval gate (`bootKeys[key] && !Boot()`) did not cover them, so a hand-made `sash/1` terminal grant that listed `module.load` would have passed sash's check with no target list at all.

## The init side

### Opting a unit in

A sash unit gains `"sash": {"file": ..., "grant": ..., "boot_ops": true}`. Its rules (token `sash_boot_ops`):
- **The grant matches the flag.** `boot_ops` requires a `sash/3` grant, and a `sash/3` grant requires `boot_ops`.
- **It is a boot unit.** `boot_ops` requires `"boot": true`.
- **It runs once.** The describe output must have `restart never`, a `start` entry, no `run`, and `ready none` or no `ready`.
- **It is a oneshot (D7).** When `start` returns 0, every process in the unit's table must have ended. One that is still running is `boot_ops_lingering`, and the start fails. The unit then becomes `exited`, which strict boot and the `needs` gate treat as a oneshot that exited 0. This closes a real gap: today a sash unit with no live process after `start` ends `stopped`, which strict boot reads as an essential unit stopping on its own (`boot_service_failed`).

Other boot units `need` the boot-ops unit (`"needs": ["hostprep"]`). That is the ordering: modules, disks, mounts and network first, then everything that uses them. A `boot` unit cannot need a non-boot unit (`boot_needs`), so what `service start` starts is never a boot unit's dependency.

### Host declarations: `host.json`

The file is set with `--host-decl PATH`, default `/etc/sahinit/host.json`. With `--deployment-root` it is part of the active generation instead. It follows the same file rules as a unit file:
- strict JSON, closed key sets, at most 64 KiB;
- root-owned, not group- or other-writable, found by the trusted walk, not a symlink.

Its top-level keys:
- `devices`: name to `{path, kind, match}`. `path` is an absolute, clean path under `/dev`, with no symlink component. `match` holds at most 8 attributes from the list above;
- `mounts`: name to `{target, fstype, source, options}`. `source` must name a device in `devices`, or be `tmpfs` with `fstype: tmpfs`. Two declarations may not share a target;
- `network`: name to a static or unit profile. A unit profile's unit must exist and have `"boot": false`;
- at most 64 entries in each.

Any error refuses every boot-ops unit, with `host_decl_invalid` or, for the file rules, `host_decl_perm`. In strict boot that is `boot_refused`. A host with no file has no declarations: the boot verbs that need one answer `not_found`, and module load and service start and stop still work.

`host.json` is what makes a name safe. The grant (fleet key) says which names this service may use. `host.json` (root on this host, or the signed generation) says what each name is. Neither alone performs anything.

### Who does what

sash only sends requests. sash never opens any of these: `/dev`, `/sys`, `/lib/modules`, a netlink socket or a mount fd.

sahinit's supervisor performs each verb, as root, in its own mount and network namespaces, on the session's verb-server goroutine:

| Verb | Kernel interface | Reused from |
| --- | --- | --- |
| `module load` | `openat2`, `finit_module` | the trusted walk (`unit.ResolveExec`'s checks) |
| `device wait` | `statfs`, `openat2(O_PATH)`, `fstat`, sysfs reads | `sahinit host device-wait`, moved to a shared function |
| `mount ensure` | `fsopen`, `fsconfig`, `fsmount`, `move_mount`, `statx`, mountinfo | the `fs.mount` resolution, `sash-mounts.json` and mount-ID checks |
| `network apply` | `NETLINK_ROUTE` (`RTM_GET*`, `RTM_NEW*`, `NLM_F_ACK`) | none (new, on `x/sys/unix`) |
| `service start` / `service stop` | `Supervisor.start` and `Supervisor.stop` with `user=true` | admission, the `needs` gate, essential rules, sandbox, cgroup |

None of these runs under `setfsuid`. They are host operations, not file operations on the service's behalf, and the file verbs keep their locked-thread identity switch.

## Limits

- **Calls.** Boot verbs are 64 per service per boot. sash counts them in a `bootmax` counter, fixed at 64, that `set` cannot raise (`boot_call_limit`, exit 2). The init counts them authoritatively. The count is per boot, not per session, so a restarted session gets no fresh budget. Boot verbs also count against `verbmax`.
- **Time.**
  - `device wait` is at most 60 s per call.
  - Every verb is bounded by the session's entry timeout, which stays 2 minutes. The init never lets a verb outlive its session.
  - `service start` is also bounded by the target unit's own `timeout`.
  - A module load that outlives its session is the one exception (see the module rules).
- **Window.** A boot verb runs only before the boot waves end (`Supervisor.Booted()` false) and only in a `start` entry. After that it is `boot_over`, so a later `ctl start` of the boot-ops unit fails. Running it again means rebooting (decision 4).
- **Who.** Only a unit with `boot_ops`, a `sash/3` purpose `boot` grant and `"boot": true` may use the verbs. A `service` grant never can.
- **Idempotence.** Every mutating verb answers `already` when the host already matches. Repeating a script is safe, and a partial boot can be re-run after a reboot without undoing anything.
- **Start and stop are once per unit per boot** (`boot_once`). Together with the call budget, this rules out stop/start loops.

## plan, atomic, await, needs, lint, describe

- **Plan mode.** The five mutating verbs print a plan line and call nothing:
  - `plan: would load module NAME`;
  - `plan: would ensure mount NAME`;
  - `plan: would apply network NAME`;
  - `plan: would start service NAME`;
  - `plan: would stop service NAME`.

  `device wait` is read-only, but in plan mode it also prints `plan: would wait for device NAME (up to D)` and calls nothing, because its outcome depends on the module loads that plan skipped. Each returns status 0 and no row.
- **atomic.** The five mutating boot keys join `irreversible`, beside `svc.spawn`, `svc.signal` and `fs.mount`. So `atomic` around them is `atomic_irreversible` before anything runs. The design reason is the one the archive lacked. `atomic` promises a restore of journaled file writes, and none of these has a restore. A stop "undone" by a start is a compensating action that runs more host operations, not a restore, so `atomic` refuses it rather than pretend. `device wait` is a read and is allowed. (sahinit serves no `atomic.*` at all, so `atomic` in a service file already fails with `atomic unavailable`; the static refusal makes the reason exact.)
- **await and check.** As above (`boot_in_await`, `check_read_only`).
- **`--needs`.** The rows are `boot.module NAME`, `boot.device NAME`, `boot.mount NAME`, `boot.net NAME`, `boot.start NAME` and `boot.stop NAME`, with mutates as in the table. A name from a value is `dynamic`, and the description is then incomplete.
- **`--describe --grant-request`.** It emits `sash/3` (purpose `boot`, actor `boot:<name>`) when any boot row exists. It fills the six lists from the literal names, and `entitlements` from the keys. It exits 1 when incomplete.
- **Lint.** A new error, `boot-outside-start`: a boot verb reachable from `stop`, `reload` or `check`.
- **verbs.md.** A new section, "Boot verbs (the init, boot grant only)", with the six rows. The `builtin` table test keeps the two in step.

## Threats

What this protects against, and what it does not. Every new Go file in both repositories opens with a `Threats:` comment in this form.

- **A compromised boot script.** The script runs as an unprivileged uid, sandboxed, in its cgroup (B2). The most it can do is ask for names that are both in its fleet-signed grant and in root's declarations, each at most once where it matters, 64 times per boot, before boot ends. It cannot choose a path, a target, an address, an interface, a module file or a parameter. It **can**:
  - load a listed module that the host did not otherwise need;
  - start or stop a listed unit early;
  - leave the host half prepared, which strict boot then turns into recovery.

  Not protected: a signer who lists too much. The grant review is the control there.
- **Device and module TOCTOU.** The threat is a different disk appearing under the expected node, through hotplug, a USB device, or a module that enumerates devices in another order. It is covered in four ways:
  - identity comes from sysfs attributes in root's declaration, not from the node name;
  - `/dev` must be devtmpfs;
  - `mount ensure` re-checks the `dev_t` against `device wait`'s record and mounts through the checked fd;
  - after mounting it compares the superblock's device and detaches on a mismatch.

  Not protected: a hostile device that copies the declared serial and wwid. Hardware identity is only as good as the attributes chosen, and `verbs.md` says to prefer `wwid` or `dm/uuid`.
- **Module path traversal.**
  - The name grammar has no `/`, `.` or `-`.
  - The release comes from `uname(2)`.
  - The file comes from `modules.dep` and must match NAME by base name.
  - The path is opened `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS` beneath a root-owned tree.
  - The kernel checks the signature (`sig_enforce` or lockdown required).

  Not protected: a malicious module signed by a key the kernel trusts. That is the kernel's trust boundary.
- **Escalation by starting units.** A started unit runs with its own catalog identity and sandbox, behind admission and the `needs` gate, so the script gains nothing the catalog did not already allow. What it controls is the timing. It cannot:
  - start another boot-ops unit (`boot_chain`);
  - start itself (`boot_self`);
  - start a boot unit (`boot_unit_auto`);
  - stop an essential one (`boot_essential`).

  `peer_supervised` keeps a service from driving the control socket. This verb path is the one deliberate exception, and it is scoped by name, by grant and by the boot window.
- **Stop loops** are ruled out by `boot_once`, `user=true` (no automatic restart) and the call budget.
- **DoS by device wait.**
  - Each wait is at most 60 s, polls every 50 ms and holds no supervisor lock.
  - Only one verb per session is in flight, since the wire is serial.
  - Every wait is bounded by the 2-minute entry timeout.

  The worst case is a boot delayed by 2 minutes, then recovery.
- **Network apply exposing interfaces.** The profile, not the script, chooses the link (by MAC), the addresses and the routes. Apply only adds, never creates virtual links, never sets promiscuous mode or forwarding, and can wait for a firewall unit (`requires`). Not protected: a profile that brings up a public interface before a firewall when it lists no `requires`. Decision 5 requires it for any static profile with a non-private address (`host_decl_invalid` otherwise).
- **Grant forgery, swap and replay.**
  - Ed25519 over a domain-separated message.
  - `sash/2`, `sash/3` and the floor domains are disjoint.
  - The `host` binding.
  - The per-service floor.
  - Strict decoding with a size cap.
  - It fails closed at every step.

  No keys are new. The fleet key signs both versions.

## Failure codes

**sash** (stderr token, exit status; refusals before the init is called):

| Token | Exit | When |
| --- | --- | --- |
| `usage module load NAME`, `usage device wait NAME timeout D`, `usage device wait: timeout 1s to 60s`, `usage mount ensure NAME`, `usage network apply NAME`, `usage service start NAME`, `usage service stop NAME` | 2 | shape |
| `denied boot.module` (and the other keys) | 126 | key not in the grant, or the grant is not `sash/3` boot |
| `denied module`, `denied device`, `denied host mount`, `denied network profile`, `denied service start`, `denied service stop` | 126 | name not in the grant's list |
| `boot_entry` | 2 | outside `--entry start` |
| `boot_in_await`, `boot_in_par` | 2 | inside `await` or `check`, or inside `par` |
| `boot_call_limit` | 2 | the 65th boot verb |
| `atomic_irreversible: boot.KEY` | 2 | a mutating boot verb in `atomic` |
| `grant_bad_shape`, `grant_bad_sig`, `grant_expired`, `grant_exp_zero`, `grant_service_mismatch` | 2 | existing tokens, extended to `sash/3` |
| `refused boot.KEY: CODE (why)` | 2 | any init code below |

**sahinit** (`fail.E` tokens; each goes in `docs/reference/codes.md`, which `docs_test.go` enforces):

| Token | When |
| --- | --- |
| `boot_over` | a boot verb after the boot waves ended |
| `boot_entry` | a boot verb in a session whose entry is not `start` |
| `boot_call_limit` | more than 64 boot verbs for this service in this boot |
| `boot_once`, `boot_self`, `boot_chain`, `boot_cycle`, `boot_unit_auto`, `boot_essential` | the start and stop rules |
| `module_disabled`, `module_sig_unenforced`, `module_tree`, `module_unknown`, `module_failed` | the module rules |
| `device_unsafe`, `device_mismatch`, `device_absent`, `device_changed` | the device rules (`timeout` as for `wait`) |
| `mount_target`, `mount_conflict`, `mount_failed` | the mount rules (`mount_overlap`, `not_mounted` existing) |
| `net_unknown_link`, `net_unguarded`, `net_failed` | the network rules |
| `sash_boot_ops`, `boot_ops_lingering` | unit opt-in and oneshot rules |
| `sash_grant_host` | a `sash/3` host mismatch, or no trust file |
| `host_decl_invalid`, `host_decl_perm` | `host.json` |
| `denied`, `busy`, `not_found`, `timeout` | existing, with the meanings above |

sash's `svcRefusals` gains a short reason for each code, so `refused boot.mount: device_changed (the device is not the one device wait checked)` reads on its own.

## cmd/sash-boot and internal/bootjob

They are replaced and never merged. `archive/codex-host-ops` stays in both repositories as a reference until the boot verbs have shipped, then it is deleted.

| Archive piece | Fate |
| --- | --- |
| `cmd/sash-boot` (second interpreter, `sahinit-job/1` scope, script on fd 4) | Dropped. The boot script is a sash unit file under the trusted walk, run by `sash --session --entry start` (D1). |
| sash `unit start/stop`, `mount ensure`, `device wait`, `module load`, `network apply` with flag `""` | Replaced by the six keys above, in `bootKeys` and `bootField`, behind `BootOps()`. |
| `atomic` change without design | Replaced by the reasoned `irreversible` entry above. |
| `internal/bootjob` broker, 256-call cap | Replaced by the verb server's boot gates and a 64-per-boot budget. |
| `bootjob.Host` `modprobe` exec | Replaced by `finit_module` from the trusted tree. |
| `deploy.Resource` named resources | Kept as an idea: `host.json` names, inside the generation when deployed. |
| mountinfo idempotence check, `mount_conflict` | Kept as an idea, extended by `dev_t` checks. |
| `network.apply` as "start a designated unit" | Kept as the `unit` profile kind. |
| `docs/examples/host/modules.json`, `network.json` | Replaced by an example boot-ops unit, `host.json` and a script under `docs/examples/host/`. |

## Build phases and tests

Each phase ends with the usual loop: build, adversarial review, fix, verify, then ask before any push.

**Phase 1: sash, against the double.** On a branch from `feat/init-phase1`.
- `sash/3` in `internal/grant`: shape, `Message`, `Sign`, `Verify`, `Request`, `BootOps`.
- The golden file `testdata/sash-grant-v3-vectors.json`: valid, tampered, wrong key, expired, oversized, v2 and v3 domain swaps, purpose `service` on v3, `ensure` in `mount_sources`, and duplicate and oversized lists.
- The six verbs in `builtin.go`, `svc.go`, `bootKeys`, `bootField`, `needsMutates`, `mutating` and `irreversible`.
- The gates: entry, `await`, `par` and the call budget.
- Plan lines, `--needs`, `--grant-request` and the lint rule.
- `prototest.Init` serves `boot.*` against a fixture `host.json` and re-checks every rule, with a fake kernel.
- `verbs.md`, `features.md`, and a cross-reference from the init contract.

Negative tests:
- grants: tampered, expired, oversized, wrong key, a `sash/2` grant listing `boot.module`, a `sash/1` grant listing it;
- names: each verb with a name missing from the grant;
- traversal: `module load ../x`, `module load a/b`, `module load x.ko`, `module load virtio-net`, `mount ensure /mnt/x`, `device wait a/b timeout 5s`;
- placement: a boot verb in `stop`, `check`, `await`, `par` and `atomic`;
- limits: the 65th call, and a 61 s timeout.

**Phase 2: sahinit, the checks without the kernel.**
- The `sash/3` parse, with the copied vectors compared both ways.
- `--host-decl` and its loader.
- `boot_ops` unit validation, and the oneshot `exited` state.
- The verb server's gates and per-verb checks over a `Host` interface extended with `LoadModule`, `WaitDevice`, `EnsureMount`, `ApplyNetwork`, `StartUnit` and `StopUnit`, backed by a fake.
- The `sash_grant_host` binding.
- `codes.md`, `units.md`, `config-files.md` and `cli.md`.

Negative tests:
- everything in phase 1 again, at the server;
- a forged `modules.dep` line with `..` or an absolute path, and a name matching only after a wrong suffix strip;
- `host.json` with a symlink path, a duplicate target, an unknown key or bad permissions;
- the start and stop rules: self, chain, cycle, boot unit, essential, twice;
- `boot_over` after `SetBooted(true)`;
- a sash entry other than `start`;
- a lingering spawned process at the end of `start`.

**Phase 3: sahinit on Linux.**
- `finit_module`, the device check, `fsmount`, rtnetlink, and start and stop wired to the supervisor.

Linux e2e tests (`scripts/test-linux.sh`, privileged container):
- netlink in a fresh network namespace on a dummy link and a veth peer (a MAC-selected link, add only, `already` on re-run, `net_unknown_link`);
- `mount ensure` on a loop device: `already`, `mount_conflict`, `device_changed` by swapping the loop backing between wait and mount, and the host mount surviving the unit's end;
- device identity mismatch;
- a symlinked `/dev` component;
- `service start` and `stop` against real units, and the deadlock test above.

Module loading is not tested in a container.

**Phase 4: QEMU (`scripts/test-boot.py --scenario bootverbs`).**

The kernel builds `virtio_blk` and `virtio_net` as modules and boots with `module.sig_enforce=1`. The guest has a second disk with a known serial and a user-mode NIC.

The image has these units:
- `hostprep`, a boot-ops sash unit with a `sash/3` grant;
- `app`, a boot unit that `needs` `hostprep`;
- `dhcp`, `"boot": false`.

The `hostprep` script runs:
```sash
module load virtio_blk
device wait data timeout 30s
mount ensure data
module load virtio_net
network apply lan
service start dhcp
```
The scenario asserts:
- the `BOOTTEST:` lines;
- `/data` is mounted `nosuid,nodev` with the right device;
- the link has the profile address;
- `app` ran after `hostprep`;
- `hostprep` is `exited`.

Negative scenarios, each ending in `BOOTTEST: recovery`:
- `bootverbs-tampered`: one byte of the grant changed;
- `bootverbs-unsigned`: a module whose signature trailer was stripped, giving `module_failed`;
- `bootverbs-wrongdisk`: the disk serial differs, giving `device_mismatch`;
- `bootverbs-ungranted`: the script asks for a module not in the grant.

## Decisions (Ryan, 2026-11-24)

1. **`service start` and `service stop`.** In sash, `unit` stays the systemapi agent's systemd verb. Keys are `boot.start` and `boot.stop`.
2. **Modules need kernel signature enforcement, with a development override chosen at build time.**
   - Release builds refuse every load unless `sig_enforce` is `Y` or the kernel is in lockdown (`module_sig_unenforced`).
   - The override exists only in a binary built with the Go build tag `sahinit_unsigned_modules`. Nothing at run time can turn it on: no flag, no environment variable, no unit or `host.json` field, no deployment.
   - In such a binary, a load with signature enforcement off goes ahead. Each such load is logged as `module_unsigned_dev NAME`.
   - The binary announces itself:
     - `sahinit --version` and the startup log line carry `unsigned-modules`;
     - `ctl status` reports `"build": ["unsigned-modules"]`.
   - `build.sh` builds release artifacts without the tag. A test checks that the default build refuses an unsigned load.
   - The tag relaxes only the signature check. `modules_disabled`, the trusted module tree, `modules.dep` path checks, the grant list and the host binding all still apply.
   - Dependencies are loaded implicitly from the root-owned `modules.dep`; they are not listed in the grant.
3. **`sash/3` is bound to the host.** Its `host` must equal the boot-policy trust file's `host_id`. With no trust file, every `sash/3` grant is refused.
4. **Boot only, for now.** After the boot waves end, every boot verb is `boot_over`.
   - **Follow-up:** post-boot `network apply`, plus link changes and hot-plugged disks, need their own contract with their own purpose.
   - This is tracked in sah.system `docs/followups.md`.
5. **A static profile with a non-private address must list `requires`.** Otherwise `host.json` is refused with `host_decl_invalid`. The firewall unit is ordered before the address, but it does not need the address to exist:
   - nftables rules match values, not live addresses. A rule on `ip daddr 203.0.113.7` or on a set of addresses loads, and stays loaded, before any interface carries that address.
   - Matching on `iifname "eth0"` is a string match, so it also loads before the link exists. `iif` is an index and needs the link, so firewall units should use `iifname`.
   - So rules stick across the address appearing later. The firewall unit needs no ANY or placeholder source; `requires` only makes sure the filter is in place before the public address goes live.
   - The firewall unit's own configuration (for example through nftapi) is out of scope here.

## Phase 1 build notes (2026-11-24)

Where the contract left a choice, phase 1 took the fail-closed one. Phase 2 should match each, or change both sides together.

1. **`ensure` in `mount_sources` is refused in `sash/2` as well as `sash/3`** (`grant_bad_shape`). The syntax rule says "the grant parser"; no version is named. A `sash/2` grant could never use it anyway: a type mount's source is its type and `ensure` is no type, and a bind needs a path. The `sash/2` message and vectors are unchanged.
2. **`device wait`'s timeout is whole seconds.** `1.5s` and `500ms` are `usage device wait: timeout 1s to 60s`, never rounded; `1m` is 60. A bare `30` is refused in a script (it is not a duration); on the wire the init receives `"30"` and refuses anything but an integer 1 to 60 (the double does).
3. **Every boot verb inside `await` or `check` is `boot_in_await`**, the mutating ones included (not the generic `await: mutation refused`), and the run stops there: a refusal is never polled again as "not ready". In the `check` entry `boot_entry` comes first, since sash checks the entry before the placement.
4. **sash's order:** the key and the `sash/3` grant (in prepare, `denied boot.KEY`), then the entry, `await`/`check`, `par`, then the name (`denied module` and the others), all before any stage of the pipeline runs; the budget is counted at the call. Under `--entry` a denial (126) exits 2, as init contract item 12 already says for every denial.
5. **The budget counts every call that reaches the door**, in plan mode too and including calls the init then refuses, once per run on the session state. The init's own count is per boot and authoritative.
6. **An init refusal is always `refused boot.KEY: CODE (why)`**, `timeout` included (the service verb `wait` prints a bare `timeout`; the boot verbs follow this contract's single form).
7. **`mount ensure` of a device source with no `device wait` for that device earlier in this boot is `device_changed`** in the double: there is no checked `dev_t` to mount through. Phase 2 should refuse the same way.
8. **`module`, `device` and `network` are reserved**: no `fn` may take those names. `module X` (any second word but `load`) is `usage module load NAME`, likewise for `device` and `network`.
9. **The double loads a module's dependencies from the end of its `modules.dep` line** (as modprobe does, so each finds its own dependencies live) and reports them in the order the line gives. Phase 3 should confirm that order against the kernel's.
10. **The double checks the host binding as part of gate 2** (before the opt-in), and refuses an empty `host_id` and an empty grant `host` alike.
11. **`--needs --policy`** words the comment "only a sash/3 boot grant carries this" for `boot.*`; the `svc.*` and `fs.mount` rows keep the `sash/2` wording, since a `sash/2` grant still carries them.

For phase 2:
- The golden vectors are `internal/grant/testdata/sash-grant-v3-vectors.json` (`sash-grant-v3-vectors/1`): fixed test keys (seeds 32×0x01 fleet, 32×0x02 emergency, 32×0x03 wrong), `now`, and per case the document, the expected token and, for valid cases, the exact signed message. `go test ./internal/grant -run TestV3Vectors` fails if the code's bytes drift from the file.
- The double's host.json fixture and loader are `prototest.HostFixture` and `prototest.ParseHostDecl` (`internal/proto/prototest/boot.go`): a reference for the strict loader, not its implementation.
