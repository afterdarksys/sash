# sash language v4: config files, plans, checks, and jobs

Status: scope confirmed by Ryan 2026-11-10: build 1–6 in sash; 7 is an agent contract, drafted in `systemapi.io/docs/sash-jobs-contract.md`, not built. Template syntax confirmed: `{{ }}` with sash expressions.

Every v1–v3 rule holds: verbs only as pipeline stages behind the grant check and the agent re-check, values never parsed as sash source, everything in process bounded by the 8 MiB value cap and depth 64.

## 1. Templates

### `template_file spec tpl out [--check]`

Reads `spec` and `tpl` through `file.read`, validates the spec, renders, and writes `out` through `file.write`. Every path passes the same root checks as `cat` and `>`. With `--check` it writes nothing and prints a line diff of `out` against the render, so it needs only `file.read`.

The spec is YAML, JSON, or TOML, by extension (`.yaml`, `.yml`, `.json`, `.toml`). It is either a plain map of variables, or a map with `vars:` and `schema:`:

```yaml
vars:
  port: 8080
  hosts: [web1, web2]
schema:
  port:  { type: number, required: true }
  hosts: { type: list, required: true }
  mode:  { type: string, enum: [prod, staging], default: prod }
  name:  { type: string, pattern: '^[a-z][a-z0-9-]*$' }
```

Types: `string`, `number`, `bool`, `list`, `map`. A missing required var, a wrong type, a value outside `enum`, or a string that does not match `pattern` (RE2) is exit 2 before anything renders. A var not in the schema is an error when a schema is present. Defaults fill missing vars.

`/etc` stays out of reach: a session writes only under its `file_write` roots. Writing a system config is `config.write`, which is still blocked on its own contract (`TODO_CODEX_OR_CLAUDE.md`).

### `... | template tpl`

Renders `tpl` with the pipeline's value as the vars and prints the result. No schema; for a check, use `template_file --check`.

### Template syntax

| Form | Meaning |
| --- | --- |
| `{{ expr }}` | A sash expression, the same language as `[ ]`. Lists and maps render as JSON. |
| `{{ if expr }} … {{ else if expr }} … {{ else }} … {{ end }}` | |
| `{{ for x in expr }} … {{ end }}`, `{{ for k, v in expr }}` | As `for` in sash. |
| `{{# comment }}` | Dropped. |
| `{{-` and `-}}` | Trim the whitespace, newlines included, before or after the tag. |

Vars are `$name` for each top-level key, and `$vars` for the whole map. An unset name is exit 2 with the template line number and no template text. A template cannot run a verb, read a file, or include another template. Output caps at 8 MiB; loops in a template count against `loopmax`; nesting caps at 64.

## 2. Plan mode

`set -o plan`, or `sash --plan script`. The mutating verbs (`unit.restart`, `package.install`, `sysctl.set`, `file.write`) do not reach the agent; each prints `would <command>` and succeeds. Reads run, so the script's logic sees real state. `template_file` in plan mode shows its diff.

`sash --plan` locks plan on: `set +o plan` is exit 2 for that run, so a script cannot turn its own dry run off. Plan mode does not change the grant; a denied mutation is still denied.

## 3. `retry` and `timeout`

- `retry N [delay D] { … }`: runs the block again when it fails, up to N times (N ≤ 100, D ≤ 1h). A failure is what `try` catches. A jump or a cap passes through.
- `timeout D { … }`: D ≤ 24h. The deadline is checked before every statement and before every agent call. An agent call already in flight finishes. Past the deadline it is exit 2 `timeout`, which `try` and `retry` can catch.

Durations are Go durations: `500ms`, `2s`, `5m`, `1h`.

## 4. Checks

`check "name" [ expr ]` records a row (`check`, `result`) and prints `ok name` or `FAIL name`. A failed check is status 1 and does not stop the script unless `set -e` is on. `checks` lists the rows recorded so far, for `|> to json`. Names cap at 256 bytes; one run keeps at most 10000 checks.

## 5. Secrets (redaction only in v4)

`declare -s name = value` marks a name secret. A secret is never printed: `echo`, `trace`, `declare -p`, `why`, output, and errors show `***`. Anything computed from a secret is secret too: an expression that reads one, a word that contains one. The prompt does not keep a line with `declare -s` in history. A secret decides nothing: a condition that reads one (`if`, `while`, `do`, `filter`, `check`, a template `if`) is exit 2, since a branch per guess would spell the value out. An error in an evaluation that read a secret ends the line, and `try` and `retry` cannot catch it, so errors cannot probe it.

**Decision:** in v4 a secret cannot leave sash. `template_file` refuses to render one, a verb argument cannot carry one, and `>` refuses one. The agent's `file.write` has no file mode, so a rendered secret would land with the helper's default mode. Writing secrets needs the agent change in section 7: `file.write` with a mode, and a `secret get` verb.

**Amended (jobs contract §2, as built).** `file.write` now takes an optional `mode` (`0600`, `0640`, `0644`), so `template_file` may write a render that read a secret, always with mode `0600`. That is the only way a secret leaves sash: a verb argument, a path, `>`, `echo`, and `log` still refuse or redact one. A render is secret when the spec's or the template's response is: its hash is a secret origin's, or it holds a bound secret's text (the `origin` rule, v6 §2), for example `declare -s db = (cat /srv/app/db.yaml)` and then `template_file /srv/app/db.yaml app.tpl /srv/app/app.conf`. It is also secret when the rendered output, or any decoded spec value, holds a bound secret's text by the same rule (four runes or more anywhere, shorter only as a whole field): that catches a secret built during the render, split across spec values (`a: hunter2`, `b: -long`, `pw={{ $a }}{{ $b }}`) or spelled with an escape the spec decodes (`"hunter2\x2dlong"`). Then the spec and template origins are marked secret too. A field with a CR or LF is checked a second time with every CR and LF removed (from it and from the secret texts), so a secret split across output lines (`pw=hunter2` / `-long`) is caught; that pass can also join the ends of two unrelated lines into a secret's text, which over-redacts (marks the render secret), never leaks. **Known limits:** an encoding of a secret (its base64, hex, or any transform) is not its text and is written with the default mode, the same limit as recordings (v6 §4); and a secret bound *after* the write (`template_file ... /srv/app/app.conf`, then `declare -s s = (cat /srv/app/app.conf)`) cannot be known when the file is written, so it went out with the default mode. Lint warns about that order when it sees it (`secret-after-write`, best effort, by literal path). sash sends `mode` only on such a write, because an agent older than the mode refuses any frame that has it (`frame body`); that answer is exit 2 `agent too old for file.write mode`, never a retry without the mode. The agent's `bad_mode` and `owner_changed` are exit 2 `refused file.write: <code> (...)`, which `try` catches with key `file.write`. A secret render's `--check` and plan diffs print `files differ (a secret is in the render or the file; not listed)`, and so does a diff against a current file that is itself secret; plan mode prints `plan: would template_file <out> (<n> bytes) (mode 0600)`. An error after a secret read (spec parse, schema, render) ends the line as `template_file: a secret ended the line`, which `try` cannot catch, since the message could quote the secret. The value a secret render leaves (`let r = (template_file ...)`) is a secret. Recordings keep the request's `mode`, redact its body (no hash), and replay matches on the mode.

## 6. Functions and `log`

Expression functions: `sha256(s)` (hex), `base64(s)`, `unbase64(s)`, `hex(s)`, `uuid()` (crypto/rand, v4). Pure apart from `uuid`'s randomness.

`log level msg [ {fields} ]`: one line on stderr, `level` one of `debug`, `info`, `warn`, `error`, with an RFC 3339 time and the fields of an optional map in key order. (Amended: `key=value` cannot be a word, since `=` is a token.) Control bytes become spaces. A secret value logs as `***`.

## 7. Jobs and other agent work (contract, not built)

Drafted as `systemapi.io/docs/sash-jobs-contract.md`: daemonized sash scripts (`job start file every 5m | on boot, restart on-failure`, `job list`, `job stop`, `job logs`) run by the agent under their own signed job grant and lease, on the existing sysscript job pool. Also there: `file.write` with a mode, `secret get`, `http.get` and `tcp.probe` health verbs, and signed scripts for `use` and jobs.

## Threats

- Template injection: a template is parsed from its file once; vars are data and never parsed as template or sash source. A var holding `{{ … }}` renders literally.
- Template amplification: output, loops, and nesting are capped and charged.
- Schema bypass: validation runs on the merged vars before any render; a schema error writes nothing.
- A plan that mutates: mutating keys are cut at the one parent door (`machine.do`); `sash --plan` locks the option.
- A timeout leaving work half done: checked at statement and verb boundaries; documented that an in-flight verb completes.
- Secret leaks: redaction at every render path, taint through expressions and words, no history, and no exit from sash until the agent can write with a mode.
- `uuid` randomness: `crypto/rand`, never `math/rand`.

## Order

1. Templates: engine, `template`, `template_file`, schema, `--check` diff.
2. Plan mode.
3. `retry`, `timeout`.
4. `check`, `checks`.
5. Secrets.
6. Functions and `log`.
7. The jobs contract document in systemapi.io.
