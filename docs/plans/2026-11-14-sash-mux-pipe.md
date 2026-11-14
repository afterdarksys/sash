# Multiplexed verb pipe (sash ⇄ systemapi agent)

Status: approved by Ryan 2026-11-14; every "Recommended" below is the decision (hello at first `par`, M = 4, 5-minute agent verb deadline, agent-wide cap of 32 file helpers, audit rows in the HMAC-chained session recording). Unblocks v5 §8 `par N { }`. Depends on jobs-contract §2 (`file.write` mode), built on systemapi.io `feat/sash-file-write-mode` (unmerged).

Today `proto.Client.Do` (`internal/proto/proto.go`) holds one mutex across write and read; `proto.Serve` and the agent's `sashServe` (`sash_frame.go`) read one frame, run the handler inline, and write one result. Frames are a big-endian uint32 length plus one strict JSON object, capped at 8 MiB on both sides (`proto.MaxFrame`, `sashMaxFrame`). Nothing carries an id: a response matches its request only because there is never more than one outstanding.

## 1. Wire

**Hello.** Opt-in per connection: sash sends `{"mux":{"v":1,"max":16,"need":["mode"]}}`.
- An old agent decodes with `DisallowUnknownFields` (`sash_frame.go:sashReadFrame`), answers `{"error":"frame body"}`, and keeps serving. sash treats exactly that reply as "not supported": the client stays serial and `par` runs at N=1, branches in order. Never an error.
- A new agent answers `{"mux":{"v":1,"max":M,"have":["mode"]}}`, M its in-flight limit. Mux support requires `file.write` mode support.
- Any other reply (wrong shape, unknown version, M < 1 or > 16) marks the client broken (§4).
- An old sash never sends hello; a new agent then serves exactly today's protocol.
- Hello is valid only while nothing is outstanding; a hello after mux is on is a protocol error.

Decision: send hello at `session.Open` or at the first `par`. Recommended: first `par`, so a session that never uses `par` stays byte-identical to today against any agent, and the pipe is idle when hello goes out.

**Request ids.** Allocated by sash: JSON integers 1..2³²−1, strictly increasing per connection, never reused; exhaustion breaks the client. Id 0 is reserved for connection-level frames. The agent tracks only the last id seen; a request whose id is not greater is a protocol error (`{"id":0,"error":"mux_protocol"}`, socket closed). Every request, response and cancel frame carries `id` once mux is on.

**Correlation.** sash keeps id → one-shot channel. A response for an id not outstanding (unknown, already answered, or 0 with a result) breaks the client. It is never routed to "the oldest waiter".

**Ordering.** Within a branch requests stay in order (one outstanding per branch). Across branches there is no completion order. The agent starts requests in arrival order, subject to §3 locks. Scripts must not assume cross-branch order.

**In-flight limit.** Worst case per request is about 32 MiB (8 MiB frame, decoded body ≤ `fileReadMax`, the defensive copy in `sashDoFileWrite`, 8 MiB result); file verbs each spawn a helper (`file_spawn_linux.go:startFileHelperProc`).

Decision: M = 4 or M = 16. Recommended: 4 (about 128 MiB and 4 helpers per session; most verbs wait on systemd, the package manager or disk, so 4 captures most of the gain; `par 16` still works, extra branches wait in sash).

**Backpressure.** sash holds a semaphore of min(N, M); a waiting branch blocks before allocating an id. The agent reads continuously (it must see `cancel`); a request beyond M is a protocol violation answered `{"id":n,"error":"mux_over_limit"}` without running, and sash marks itself broken. The agent buffers at most one unread frame beyond its in-flight requests.

**Frame cap.** Each frame is written whole under a per-connection write mutex on each side (today `proto.Write` and `sashWriteFrame` each issue two writes, header then body, which would interleave). The 8 MiB cap per frame is unchanged; `sashWriteResult`'s `result_too_large` still bounds the body with its id. One large result can delay others while it is written: accepted.

## 2. Cancellation

`{"cancel":n}`.
- **Queued** (waiting on a lock or slot): dropped, audited `cancelled_before_start`, answered `{"id":n,"error":"cancelled"}`. Nothing ran.
- **Started:** runs to completion and its response is always sent, including a helper between `write_open` and `write_commit`, `unit.restart`, `package.install`, `sysctl.set`. Matches v5's "an in-flight verb finishes". The agent never kills a write helper part way (aborting is the helper's own path), and reads also finish, for one rule.
- **Already answered:** ignored (cancel and response crossed). **Never issued:** protocol error.

**Deadline.** `sashUnitRestartDefault` and `sashPackageInstallDefault` (`sash_verb.go`) run with `context.Background()`. In mux mode each verb gets an agent-side deadline; past it the answer is `{"id":n,"error":"outcome_unknown"}`, audited (systemd or the package manager may still finish).

Decision: deadline 5 minutes, or the grant `exp`. Recommended: 5 minutes, so a hung unit can't hold a slot for a whole lease.

**What sash assumes.** A sent cancel means "maybe ran" until the response arrives; `par` joins only after every outstanding id is answered. A mutating verb that finished after its branch was cancelled is reported in the `par` failure message and in `par … as r` (`branch 3: unit.restart nginx finished after cancel (exit 0)`). On a broken pipe, or a second interrupt that stops the wait, sash prints `outcome unknown: <key> <target>` for every outstanding mutating verb. The agent audit always holds the real outcome.

## 3. Agent side

**Dispatch.** `sashServe` gains a reader loop and a worker per request, bounded by M. Each worker calls `sashDispatchAt` unchanged; lease, history, entitlement, pin, policy and helper checks run per request when it leaves the queue (`sashDispatchAt`, `sashLeaseLive`).

**Lease expiry mid-flight.** A request started before `exp` finishes (today's behaviour). A request still queued at expiry is checked when it starts and is `denied`.

**Per-session class limits** (within M): `file.read`/`file.write` 4 (helpers); `package.install` 1 (global package-manager lock); `ps` 1 (walks `/proc`); `history` already serialized by `sashHistMu`; others M.

**Per-target locks** (per session, FIFO): `unit.restart` on `unit:<name>`, `sysctl.set` on `sysctl:<key>`, `file.write` on the cleaned path after `sashLongestRoot`. Reads take no lock (`write_commit` replaces atomically). Cross-session races exist today and are out of scope.

Decision: an agent-wide file-helper cap (e.g. 32) or only the per-session cap. Recommended: agent-wide too, since sessions × 4 is otherwise unbounded; a request waiting for a helper is still queued and cancellable.

**Audit.** There is no per-verb audit today (`sash_verb.go` has none; the trails are the terminal recording `recorder.go` and the history `.audit` file, `sash_history.go:sashHistPaths`). New: each request writes a `start` row before its handler (id, key, target, body length; never a body or body hash, so short secrets aren't guessable, SECURITY-RULES rule 6) and exactly one end row (`end` with exit or error, `cancelled_before_start`, `outcome_unknown`, `session_closed`), appended under one mutex in event order. If an audit write fails, the verb does not start.

Decision: rows in the HMAC-chained session recording (`recorder.go`) or a new per-session verb log. Recommended: the recording, whose chain proves order and whose digest already reaches the control plane. Needs plumbing: `prepareSash` (`sash_session_linux.go`) has no recorder handle today.

**Session end.** On socket close, workers finish and write end rows; `sashServe` waits on a WaitGroup before returning, so no agent work outlives the session. The single-process reap rule holds: `par` adds goroutines inside one sash process, never processes or background jobs.

## 4. sash side

**Client.** `proto.Client` gains `writeMu`, an atomic `nextID`, `pending map[uint32]chan resp`, one reader goroutine started after a successful hello, and `broken error` set once. `Do(v)` keeps its signature; serial mode keeps today's code path exactly, including `ErrModeUnsupported`. An optional `parent.ContextParent{ DoContext(ctx, v) }` sends `cancel` when its context ends; fake, fixture and replay parents implement only `Do`.

**As built (step 3).** `internal/proto/mux.go`. `Client.Concurrency(n)` is the lazy hello (`par` will call it): it returns min(n, M), or 1 after the exact `{"error":"frame body"}`; a session that never calls it never sends hello. The hello reply is decoded strictly: exactly `{"mux":{"v":1,"max":M,"have":[...]}}` with 1 ≤ M ≤ 16, `"mode"` in `have`, no `need`, no other field, nothing after it; anything else (including a near spelling of the old-agent reply) breaks the client. Request ids are allocated and written under `writeMu`, so they reach the agent strictly increasing. The cancel frame is exactly `{"cancel":n}`. A call whose context ends sends it and still waits for the answer: `cancelled` is `parent.ErrCancelled`, `outcome_unknown` is `parent.ErrOutcomeUnknown` (the pipe stays up), and a real result that crossed the cancel is returned as the result. `mux_over_limit`, any id-0 frame, a response without an id, or one for an id not outstanding breaks the client (`proto.ErrBroken`). Tests run against an in-repo fake agent (`fakeagent_test.go`) and an old-agent fake.

**Broken client.** A framing error, unknown id, over-limit reply or malformed hello sets `broken`; every outstanding id gets `errBroken`, later calls fail at once, and every branch fails closed with status 2.

**`par`.** Each branch's machine holds the parent; a sibling's failure cancels the branch context, which sends `cancel` for that branch's outstanding id, and the branch's own boundary checks (`machine.do`, `pastDeadline`) stop it starting anything new. Effective N is min(N, M), or 1 when serial.

**Plan.** Unchanged: mutating verbs never reach the pipe; reads run concurrently. **Confirm.** Forces N = 1 and skips hello. **`verbmax`.** One shared `*atomic.Int64` under `par`, incremented with compare-and-swap so it never passes `verbMax`.

**Record and replay.** `Tape.add`, `Tape.see` and `seq` take a mutex; each event gains `branch` and a per-branch sequence; the hash chain covers the merged completion order; the secret set gathers from every branch before `Finish` redacts. Replay answers each branch from its own stream and requires every event used exactly once; it never compares merged order. Replay runs `par` at N = 1. (Fix v6 §4's "checks the merged order" to match.)

## Threats

- **Request-id confusion or spoofing.** Ids are sash-allocated, strictly increasing, never reused; a response for an id not outstanding breaks the client and is never routed elsewhere. The socket is a private `SOCK_CLOEXEC` socketpair (`prepareSash`); anyone able to write into it already controls the session, and the per-request grant check still bounds them.
- **Starvation.** FIFO agent locks, FIFO sash semaphore, and the agent deadline (`outcome_unknown`) bound a hung verb.
- **Exhausting the agent.** M = 4 per session, class limits, an agent-wide helper cap, at most one unread frame beyond in-flight, over-limit refused not queued, 8 MiB per frame.
- **Ordering assumptions in scripts.** Branches are unordered; output is buffered and emitted in branch order (v5); lint warns when two branches name the same mutating target; per-target locks serialize but don't order.
- **Races on one file or unit.** Serialized per path, unit and sysctl key; last writer wins; both audited.
- **Audit integrity.** One start and exactly one end row per id, in a chained log; a failed audit write means no verb; cancelled-but-finished and unknown outcomes are explicit rows.
- **Forced downgrade** (hello stripped or answered `frame body`): `par` at N = 1, slower and never less safe. No path ever retries a mutation.

## Order

1. Agent: jobs §2 (`file.write` mode) — built on `feat/sash-file-write-mode`, merge first.
2. Agent: write mutex, hello, ids, workers, locks, limits, cancel, deadline, audit rows, WaitGroup on close. Negative tests: repeated id; out-of-order id; id 0 on a request; over-limit; cancel for an id never issued; hello while a request is outstanding; frame over 8 MiB; lease expiry with requests queued; grant denied on a queued request; audit write failure means no verb; socket closed mid-write still writes an end row; two writes to one path serialized; hung restart gives `outcome_unknown`.
3. sash: concurrent client plus serial fallback against an old-agent fake replying `frame body`. Negative tests: unknown response id breaks the client; malformed hello; framing error fails every outstanding call; id exhaustion; cancel crossing a response; concurrent `Do` under `-race`.
4. sash: `par` on `ContextParent`, shared `verbmax`, per-branch tape streams. Tests: a sibling failure cancels the queued request; a finished mutation is reported after cancel; `verbmax` never passed under 16 branches; replay with a different completion order doesn't diverge; confirm forces N = 1.
5. Docs: close v5 §8's decision; fix v6 §4 "merged order".

**Size:** agent, human 2 weeks / CC 3–4 days; sash client, human 1 week / CC 1–2 days; `par` integration, human 3–4 days / CC 1 day.
