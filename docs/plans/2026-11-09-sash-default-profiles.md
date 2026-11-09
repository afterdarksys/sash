# sash default profiles

Status: approved by Ryan 2026-11-09. Landed on systemapi.io branch `sash-default-profiles`. Two interface amendments from implementation are marked below.

## Problem

A fleet sash session gets the grant `buildSashTerminalGrant` signs from `effectivePolicies(registry, hostId)`: host column, else tenant default, else deny-all. Nothing writes a default, so a new host signs `["ps"]` and `ls` is `denied file.read`.

A hand start (`sash` with no `--session`) is a different gap. It has an empty grant and a parent that answers `no parent`. A profile does not change that. This contract does not touch the hand start.

Two shape gaps block a useful read-only default:

1. `service_policy.units` grants `unit.status` and `unit.restart` together. A unit cannot be status-only.
2. `journal` is never granted. The agent has no reader (`sash_verb.go` denies it before dispatch).

## Decision

A profile is a preset. Applying it writes concrete policy columns through the existing writers. It is not a stored reference.

- The tenant route (`policyroutes.js` `tenantWrite`) and the host route (`hostWrite`) and `scripts/channel-policy.js` stay the only writers. Version check, dry run, dangerous gate, approval-class gate, and the audit row all apply unchanged.
- The audit row's `extra` carries `profile: "<name>@<version>"`. The columns it wrote are the record. The profile name is a label.
- Editing a profile definition later changes no tenant and no host. A profile is applied again by hand, with a reason, the same as any policy write.
- Expansion happens in the control plane before signing. sash still sees flat names. sash does not change.

Why not a stored reference: a reference would let one edit to `profiles.js` widen every tenant at once, outside the version check and the signed-intent approval gate.

## Service status split

`service_policy` gains an optional `status_units` list. Same unit-name shape (`SERVICE_UNIT_RE`), same `POLICY_MAX_ENTRIES` cap. Absent means `[]`.

| Field | sash | `sys:service` channel, config reload |
| --- | --- | --- |
| `units` | `unit.status` and `unit.restart` | every action, unchanged |
| `status_units` | `unit.status` only | ignored |

`units` keeps its meaning, so no existing host gains or loses anything. The `sys:service` channel has no status action, so `status_units` never reaches `servicePolicyAllows`.

Grant build: `unit.status` when `units ∪ status_units` is non-empty. `unit.restart` when `units` is non-empty. Targets gain `status_units`.

Agent: `sashTargets` gains `StatusUnits []string \`json:"status_units"\``. `sashDoUnit` checks `exactGrant(Units) || exactGrant(StatusUnits)`. `sashDoUnitRestart` checks `Units` only. An old agent ignores the new field and denies a status-only unit. A new agent with an old control plane sees no `status_units`. Both orders fail closed. Land the agent first anyway.

The agent's own unit and `sah-*-helper@` units are refused in `status_units` exactly as in `units`.

## Profiles v1

`PROFILES` in `controlplane/policyrules.js`, frozen objects, each with a `version`. Amended from a separate `profiles.js`: every policy the CLI and the routes write is built in `policyrules.js`, so a profile is built there too. A profile is a shape. Units are parameters the operator passes, because a unit list is host-specific.

| Profile | Writes | Grant on a Linux host |
| --- | --- | --- |
| `observe` | file `{ path: "/var/log", read: true }`; `status_units` = the `--unit` arguments | `ps`, `file.read` on `/var/log`, `unit.status` when units were given |
| `operate` | `observe`, plus `units` = the `--restart` arguments | adds `unit.restart` on those units |

Profiles write `tcp`, `package`, `config`, and `tweak` as empty. `observe` and `operate` are not approval-class on an enforced tenant: `/var/log` is not under `/etc`, and they grant no config path, sysctl, or tweak unit. A frozen host still goes through the approval gate, as every write does today.

There is no `none` profile; `clear` already writes every column empty. There is no `admin` profile. Package install, sysctl set, config read, and file write stay explicit per-host writes with their own reason.

The `systemapi policy` CLI and `sahpolicy.py` take `--service-status`. They do not take `--profile` yet; the routes and `channel-policy.js` do.

Interfaces (amended: a profile is an option on the existing full replace, not a new verb or route):

- `node scripts/channel-policy.js set --tenant <t> --host <h> --profile observe|operate [--service-status <u>]... [--service <u>]... --reason <text>`. Host only: the script does not set tenant defaults, and this contract does not change that.
- `PUT /api/v1/hosts/:hostId/channel-policy` and `PUT /api/v1/tenants/:tenant/channel-policy` take `{ profile, grants, reason, if_version }`. `?dry_run=1` returns `profile` with the diff. The tenant route is how a tenant default is set.
- A list the profile does not open is refused with rule `profile_closed`. An unknown name is `bad_profile`. An empty list of any key is ignored.

## Out of v1

| Item | Why |
| --- | --- |
| `journal` | No agent reader. A reader is its own contract: which units, which fields, size cap, and that it is not a `journalctl` shell-out. |
| The actor's home as a read root | `~` is not a grant root. The control plane does not know the pty user's home. Resolving it on the agent would put an unsigned root next to a signed grant. |
| `package.query` without install, `sysctl.get` without set | Same split as `status_units`. Follows once this one lands. |
| `/etc` read | `/etc` is a system root to `channel-policy.js` and approval-class. The design example grant is not a default. |
| Hand start, darwin | No parent. Unchanged. |

## Threats

- A profile edit widens every tenant silently. Refused by the preset model: only an explicit, versioned, audited write changes a column.
- `status_units` used to restart. The restart path checks `units` only; negative test.
- A patched sash calls `unit.restart` on a status-only unit. Grant omits `unit.restart` when `units` is empty, and the agent re-check denies; negative test on both.
- An old agent treating `status_units` as `units`. It does not know the field and denies; test with the target absent.
- A malformed `status_units` (bad shape, `*`, too many, agent self-unit) widening the policy. The write rules refuse each one (`parseUnit`). `parseServicePolicy` also returns null for a bad shape, `*`, too many, or a self-unit in `status_units`, so a row written around the rules denies the whole policy. `units` keeps its existing parser, which checks shape and count only.
- A profile that writes `/var/log` on a host where the pty user can read nothing there. The file helper runs as the pty user; the profile does not raise privilege.

Negative tests, control plane: `status_units` with `*`, a bad unit name, over the cap, the agent unit, a helper unit; grant from `status_units` only has no `unit.restart`; unknown profile name refused; profile with `--restart` on `observe` refused; a profile write without `if_version` refused (the existing route check); the audit row carries the profile label and the full columns.

Negative tests, agent: restart of a unit only in `StatusUnits` denied; status of a unit in neither list denied; nil `StatusUnits` behaves as today.

## Order

1. Agent: `StatusUnits`, status and restart checks, tests.
2. Control plane: `parseServicePolicy` and `assertPolicyShape` accept `status_units`; `buildSashTerminalGrant` splits the entitlements and the targets; tests.
3. `profiles.js`, the CLI verb, the route field; tests.
4. Update `2026-11-06-sash-design.md` Grant section and `systemapi.io/docs/sah-suite.md` in the same change as step 2.
