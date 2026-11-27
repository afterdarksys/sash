// Package codes is the catalog of what sash prints when it refuses or
// fails: one table, compiled in, printed by sash codes --json.
//
// Threats: catalog text read by an agent as an instruction, and a catalog
// that drifts from the binary. Every fix sentence that names a change
// has a person, the operator, or an admin as its subject or decider;
// none says to turn a mode off, widen a grant, use another credential or
// channel, or names an override, and a test holds each to a banned-phrase
// list and to a reviewed fixture. A second test scans the fixed messages
// of internal/lang, internal/grant, internal/session, internal/proto,
// internal/connect, internal/line and cmd/sash and fails when one has no entry here or its exit
// differs. The connect area also holds the codes a control plane or a host
// may send to sash connect: the client prints this build's meaning for a
// code and never the sender's own text (Connect). The catalog
// names what the binary prints; it grants nothing and decides nothing,
// and it does not protect against a reader who treats any text as an
// order.
package codes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// Code is one catalog entry. Code is a token; where sash prints a
// sentence, Meaning quotes it.
type Code struct {
	Code    string `json:"code"`
	Exit    int    `json:"exit"`
	Area    string `json:"area"`
	Meaning string `json:"meaning"`
	Fix     string `json:"fix"`
	// prints are the first words sash prints for this entry.
	prints []string
}

// Areas are the catalog's areas. tls and fetch have no entry until those
// features exist.
var Areas = []string{"parse", "grant", "verb", "file", "atomic", "secret", "term", "replay", "connect", "tls", "fetch", "session"}

// FixMax is the longest fix sentence, in bytes.
const FixMax = 512

const (
	fixScript  = "A person corrects the script; nothing on that line ran."
	fixNothing = "Nothing to change: this reports what happened."
)

var table = []Code{
	// parse: the line or the script itself.
	{"lex", 2, "parse", "the text could not be split into words: an unclosed quote, a bare !, a bad $ reference, or & (there are no background jobs)", fixScript, []string{"lex:"}},
	{"parse", 2, "parse", "the script does not parse; the message names what was expected", fixScript, []string{"parse:"}},
	{"usage", 2, "parse", "a form was given the wrong arguments; the message names the form", fixScript, []string{"usage"}},
	{"type", 2, "parse", "a value or a pipe has the wrong shape: bytes into |>, a whole row as a word, a failed [ ] expression", fixScript, []string{"type"}},
	{"command_from_value", 2, "parse", "a command word, or a word that selects what a form does, came from a value", "A person writes the command word as a literal; a value never names a command.", []string{"command_from_value"}},
	{"unknown", 127, "parse", "the name is not in the closed table and is not a fn; no host binary is run", "A person uses a name from the closed table (sash --skill verbs lists it).", []string{"unknown"}},
	{"unset", 2, "parse", "a name, a map key, or a wasm module that was never bound", fixScript, []string{"unset"}},
	{"readonly", 2, "parse", "a declare -r name was bound again", fixScript, []string{"readonly"}},
	{"fn_depth", 2, "parse", "\"fn depth\": a call deeper than eight", fixScript, []string{"fn depth"}},
	{"loop_max", 2, "parse", "\"loop max\": a loop passed the session's loop cap; the line ended", "A person decides whether the loop is right; the cap's ceiling is the grant's.", []string{"loop max"}},
	{"verb_max", 2, "parse", "\"verb max\": the run passed the session's verb cap; the line ended", "A person decides whether the run is right; the cap's ceiling is the grant's.", []string{"verb max"}},
	{"check_max", 2, "parse", "\"check max\": more check rows than a session keeps", fixScript, []string{"check max"}},
	{"set_ceiling", 2, "parse", "\"set ... above the ceiling\": set may lower loopmax or verbmax, never raise one past its ceiling", "A person keeps the cap at or below its ceiling; the ceiling is the grant's.", []string{"set "}},
	{"timeout", 2, "parse", "a timeout block's deadline passed, before a statement, a verb, or while a person was asked", "A person decides whether the work or the deadline is wrong; a verb already in flight finished.", []string{"timeout"}},
	{"hand_start", 0, "session", "\"sash: no session grant: verbs are denied, filters run\": sash was started by hand, without a session, at a terminal; printed once", fixNothing, []string{"sash: no session grant"}},
	{"line_failed", 1, "session", "\"sash line failed\": the prompt could not put the terminal in raw mode", "A person checks the terminal sash was started on.", []string{"sash line failed"}},
	{"skill_failed", 1, "file", "\"skill: ...\": sash --skill could not read its embedded files or write them to stdout", "A person checks where stdout goes; nothing else ran.", []string{"skill:"}},
	{"codes_unreadable", 1, "file", "\"codes: catalog unreadable\": sash version --json or sash codes --json could not encode the catalog", "The operator decides which build is installed.", []string{"codes: catalog unreadable"}},
	{"hangup", 129, "session", "the session's terminal hung up (SIGHUP); the run stopped at its next statement or verb", fixNothing, []string{"hangup"}},
	{"prefix_unset", 2, "file", "a relative path or glob with no session prefix", "A person writes the absolute path; a script starts with no prefix.", []string{"prefix_unset"}},
	{"json", 2, "parse", "JSON text that could not be read or written", fixScript, []string{"json"}},
	{"yaml", 2, "parse", "YAML text that could not be read or written", fixScript, []string{"yaml"}},
	{"toml", 2, "parse", "TOML text that could not be read or written", fixScript, []string{"toml"}},
	{"csv", 2, "parse", "CSV or TSV text that could not be read or written", fixScript, []string{"csv"}},
	{"data_too_large", 2, "parse", "\"<format> too large\": data past the size a value may have", fixScript, []string{"%s too large"}},
	{"template", 2, "file", "a template that could not be rendered; the message names the line", "A person corrects the template or the values; nothing was written.", []string{"template", "template_file"}},
	{"spec", 2, "file", "a template_file spec that does not match its schema", "A person corrects the spec or the schema; nothing was written.", []string{"spec"}},
	{"use", 2, "file", "a use file that could not be read, is too large, or holds something other than fn and declare -r", "A person corrects the library file; nothing from it was loaded.", []string{"use:"}},
	{"with_prefix", 2, "file", "\"with prefix: cannot stat\": the prefix of a with block could not be checked", "A person checks the path; the block did not run.", []string{"with prefix:"}},
	{"par", 2, "verb", "a par block failed: a branch failed, the count is wrong, or it was interrupted (130); the message says which", "A person reads the branch's own output above this line.", []string{"par:", "par cancelled"}},
	{"late", 0, "verb", "\"late: ...\": the outcome of a verb that answered after its par had stopped waiting", fixNothing, []string{"late:"}},
	{"record", 2, "file", "a session recording could not start or was not written; the message says why", "A person decides whether to record again; the session's verbs were not stopped.", []string{"record:", "not written:"}},
	{"wasm_unsupported", 2, "parse", "this build runs no wasm modules", "The operator decides which build is installed.", []string{"wasm_unsupported"}},
	{"wasm_hash", 2, "parse", "a wasm module's bytes do not match the sha256 the script names", "A person checks which module the script means; it was not loaded.", []string{"wasm_hash"}},
	{"wasm_refused", 2, "parse", "a wasm module was refused or stopped: it imports something, passes a limit, is too large, does not compile, trapped, breaks the call ABI, is busy, or was handed a secret", "A person checks the module; it has no access to the host either way.", []string{"wasm_abi", "wasm_busy", "wasm_compile", "wasm_import", "wasm_limits", "wasm_secret", "wasm_too_large", "wasm_trap", "wasm:"}},

	// grant: what the session may do.
	{"denied", 126, "grant", "the grant lacks the key, or the path is outside the grant's roots; that statement ran nothing, earlier ones did", "A tenant admin decides what this session's grant holds.", []string{"denied"}},
	{"grant_missing", 2, "grant", "the session was started without a grant document", "The operator checks how the session was started; no verb ran.", []string{"grant_missing"}},
	{"grant_bad_shape", 2, "grant", "the grant document is not a sash/1 grant", "The operator checks the control plane and the agent; no verb ran.", []string{"grant_bad_shape"}},
	{"grant_bad_sig", 2, "grant", "the grant's signature does not verify against the fleet key", "The operator checks the control plane and the agent; no verb ran.", []string{"grant_bad_sig"}},
	{"grant_expired", 2, "grant", "the grant's expiry has passed", "A person decides whether the work is still needed; no verb ran.", []string{"grant_expired"}},
	{"grant_star", 2, "grant", "the grant holds \"*\", which sash never accepts as an entitlement", "The operator checks the control plane; no verb ran.", []string{"grant_star"}},
	{"grant_root_slash", 2, "grant", "the grant names \"/\" as a file root, which sash never accepts", "The operator checks the control plane; no verb ran.", []string{"grant_root_slash"}},

	// verb: a call to the host's agent.
	{"declined", 126, "verb", "a person answered no at a confirm question; the verb did not run", fixNothing, []string{"declined"}},
	{"confirm_no_terminal", 2, "verb", "\"confirm needs a terminal\": confirm mode is on and there is no terminal to ask, so the mutating verb did not run", "A person decides whether that step is needed; it did not run.", []string{"confirm needs a terminal"}},
	{"confirm_locked", 2, "verb", "\"confirm is locked\": confirm mode cannot be cleared from a script or a line", fixNothing, []string{"confirm is locked"}},
	{"parent", 1, "verb", "\"parent <key>\": the call to the agent failed or the agent could not perform it", "A person reads the host's state before running the step again; the operator checks the agent.", []string{"parent"}},
	{"refused", 2, "file", "\"refused file.write: <code>\": the agent refused a file write; the code and its meaning follow", "A person reads the code; the file is as the agent reports it.", []string{"refused"}},
	{"fs_not_written", 1, "file", "\"fs write-atomic: not written\": an earlier stage of the pipeline, or its < read, failed, so nothing was sent and the file is as it was; the status is the failing stage's, not always 1: 1 for a failed read or a stage that ended 1, 2, 126 or 127 for a stage that was refused", "A person reads the failing stage's output above this line.", []string{"fs write-atomic: not written"}},
	{"mode_unsupported", 2, "file", "\"agent too old for file.write mode\": the write needs a file mode this agent cannot set, and it is never sent without one", "The operator decides when the host's agent is updated.", []string{"agent too old for file.write mode"}},
	{"outcome_unknown", 1, "verb", "a verb passed the agent's deadline, or its par stopped waiting: it may have run", "A person checks the host's state before running it again.", []string{"outcome_unknown", "outcome unknown"}},
	{"cancelled", 1, "verb", "the agent dropped the verb before it started: nothing ran", fixNothing, []string{"cancelled"}},

	// session: the pipes to the agent.
	{"frame", 2, "session", "a frame on the verb pipe is malformed or too large", "The operator checks the agent; the session cannot go on.", []string{"frame"}},
	{"verb_pipe_broken", 1, "session", "\"verb pipe broken\": the pipe to the agent closed or failed; no further verb is sent", "A person opens a new session; the operator checks the agent.", []string{"verb pipe broken"}},
	{"mux_refused", 1, "session", "the agent's answer to the concurrency hello is malformed, it reported mux_over_limit or mux_protocol, or a response named a request that was not outstanding", "The operator checks the agent; a person opens a new session.", []string{"malformed hello", "mux_over_limit", "mux_protocol", "agent:", "concurrency", "request ids exhausted", "response for an id not outstanding"}},

	// atomic.
	{"atomic_irreversible", 2, "atomic", "the block holds something atomic cannot hold: a verb it cannot undo, use, par, a nested atomic, or a question to the person", "A person moves that step outside the block.", []string{"atomic_irreversible"}},
	{"atomic_unavailable", 2, "atomic", "\"atomic unavailable\": the agent is too old for atomic, or the grant has no file write target; the block did not run", "The operator decides when the agent is updated; a tenant admin decides the grant's write targets.", []string{"atomic unavailable"}},
	{"atomic_rolled_back", 2, "atomic", "\"atomic: rolled back\": the block failed and the agent restored its file writes; the run does not end 0", "A person reads the failure above this line.", []string{"atomic:"}},

	// secret.
	{"secret_cannot_leave", 2, "secret", "\"a secret cannot leave sash\": a secret was a verb argument, a path, a > body, or a fn argument", "A person keeps the secret inside sash.", []string{"a secret cannot leave sash"}},
	{"secret_cannot_decide", 2, "secret", "\"a secret cannot decide a branch\": a secret was an if, while, or for condition", fixScript, []string{"a secret cannot decide a branch"}},
	{"secret_fault", 2, "secret", "\"a secret ended the line\": an evaluation that read a secret failed, and the line ended so the failure tells nothing", fixScript, []string{"a secret ended the line"}},
	{"secret_in_word", 2, "secret", "\"a secret in a word: build it inside [ ]\": a secret was pasted into a word", fixScript, []string{"a secret in a word"}},

	// term: asking the person.
	{"terminal_not_restored", 2, "term", "\"sash: terminal not restored\": the terminal's mode could not be put back after a question, or when a signal ended sash; the question has no answer", "A person checks the terminal before typing more.", []string{"sash: terminal not restored"}},
	{"term_no_terminal", 2, "term", "\"term needs a terminal\": the script asks a person and there is no terminal to ask; it stopped there", "A person runs the script at a terminal.", []string{"term needs a terminal"}},
	{"term_eof", 1, "term", "\"term prompt: end of input\": Ctrl-D on an empty line, or the input ended; the value is empty", fixNothing, []string{"term prompt: end of input", "end of input"}},
	{"term_empty", 1, "term", "\"term prompt: empty answer\": the person pressed Enter on an empty line; the value is empty, as at the end of input", fixNothing, []string{"term prompt: empty answer"}},
	{"term_interrupted", 130, "term", "\"term: interrupted\": Ctrl-C at a question; the script stopped", fixNothing, []string{"term: interrupted", "interrupted"}},
	{"term_answer_long", 2, "term", "\"term prompt: answer over 4096 bytes\": the rest of the line was discarded and there is no value", "A person gives a shorter answer.", []string{"term prompt: answer over 4096 bytes", "answer over 4096 bytes"}},
	{"term_answer_not_text", 2, "term", "\"term prompt: answer is not text\": the answer is not UTF-8 and there is no value", "A person types the answer as text.", []string{"term prompt: answer is not text", "answer is not text"}},
	{"term_inside_par", 2, "term", "\"term inside par\": a question inside a par branch", "A person asks before the par.", []string{"term inside par"}},
	{"term_secret_misplaced", 2, "term", "\"term prompt -s: only as declare -s name = (term prompt -s \"question\")\": -s anywhere else", "A person binds the secret with declare -s.", []string{"term prompt -s"}},

	// replay and record.
	{"record_exists", 2, "replay", "\"record_exists\": the --record path already exists, as a file, a link, or anything else; it is never overwritten and nothing ran", "A person names a new path for the recording.", []string{"record_exists"}},
	{"fixture", 2, "replay", "\"fixture: ...\" or \"fixture too large\": the --record or --replay fixture is over 8 MiB, is not strict JSON of the fixture's shape, holds \"*\", an empty entitlement or a root that is not clean and absolute, or has no result for a call", "A person corrects the fixture.", []string{"fixture:", "fixture too large"}},
	{"record_failed", 2, "replay", "the recording could not be built or written", "A person records again.", []string{"record_failed"}},
	{"record_too_large", 2, "replay", "the recording passed its size or event cap", "A person records a shorter run.", []string{"record_too_large"}},
	{"replay_bad_format", 2, "replay", "the recording is not a sash recording, or a line of it is malformed", "A person checks which file was given.", []string{"replay_bad_format"}},
	{"replay_bad_chain", 2, "replay", "the recording's hash chain does not hold: a line was changed, removed, or added", "A person records again; this file is not what was recorded.", []string{"replay_bad_chain"}},
	{"replay_bad_segment", 2, "replay", "a segment of a segmented recording is missing or out of order", "A person gives the first segment, with the others beside it.", []string{"replay_bad_segment"}},
	{"replay_diverged", 2, "replay", "the script did something the recording does not hold at that point", "A person replays the script that was recorded, unchanged.", []string{"replay_diverged"}},
	{"replay_expired", 2, "replay", "the recording is past its expiry", "A person records again.", []string{"replay_expired"}},
	{"replay_module", 2, "replay", "a wasm module's hash differs from the one in the recording", "A person replays with the module that was recorded.", []string{"replay_module"}},
	{"replay_other_script", 2, "replay", "the recording was made from another script", "A person replays the script that was recorded.", []string{"replay_other_script"}},
	{"replay_secret", 2, "replay", "the replay reached a term prompt -s; a recording never holds that answer", "Nothing to change: a recording that asked for a secret cannot be replayed.", []string{"replay_secret"}},
	{"replay_too_large", 2, "replay", "the recording is larger than a replay reads", "A person checks which file was given.", []string{"replay_too_large"}},
	{"replay_truncated", 2, "replay", "the recording stopped at its size cap, and the run went past that point", "A person records a shorter run.", []string{"replay_truncated"}},
}

// The connect area: what sash connect prints itself, then the codes a
// control plane or a host may send it (refusal, ended). For a sent code the
// client prints this table's meaning and never the sender's text.
var connectTable = []Code{
	{"connect_usage", 2, "connect", "\"usage: sash connect [--root] <host>\": connect takes one host and at most --root, and no other flag, script, or argument", "A person types the command as the usage line shows.", []string{"usage: sash connect"}},
	{"connect_no_terminal", 2, "connect", "\"connect needs a terminal\": stdin, stdout, and stderr must all be terminals; nothing was sent", "A person runs it at a terminal.", []string{"connect needs a terminal"}},
	{"connect_invalid_url", 2, "connect", "\"connect: invalid SYSTEMAPI_URL\": the control plane's address is missing, is not https, or holds a user, a query, or a path", "A person sets SYSTEMAPI_URL to the https address of the control plane.", []string{"connect: invalid SYSTEMAPI_URL"}},
	{"connect_invalid_host", 2, "connect", "\"connect: invalid host name\": a host is 1 to 253 letters, digits, dots, underscores, and hyphens; nothing was sent", "A person types the host's name or id.", []string{"connect: invalid host name"}},
	{"token_file_rejected", 2, "connect", "\"connect: token_file_rejected\": the file SYSTEMAPI_TOKEN_FILE names is not a regular file of this user with no group or other permission, is over 4096 bytes, or does not hold one token; nothing was sent", "A person logs in again with the systemapi CLI, which writes the file.", []string{"connect: token_file_rejected"}},
	{"connect_no_login", 2, "connect", "\"connect: no login; a person logs in with the systemapi CLI\": SYSTEMAPI_TOKEN_FILE is not set to an absolute path, or no file is there; sash does not log in", "A person logs in with the systemapi CLI.", []string{"connect: no login; a person logs in with the systemapi CLI"}},
	{"request_failed", 1, "connect", "\"connect: request_failed\": a call to the control plane failed: the network, TLS, a timeout, a redirect (never followed), or an answer that could not be read", "A person checks the address and the network; the operator checks the control plane.", []string{"connect: request_failed"}},
	{"connect_refused", 126, "connect", "\"connect refused: <code>\": the control plane or the host refused the session; the code and this build's meaning for it follow, and a code this build does not list prints as itself or as unlisted", "A person reads the code; no session was opened.", []string{"connect refused:"}},
	{"connect_not_sah3", 1, "connect", "\"connect: the server does not speak sah/3\": the upgrade was not answered 101, or the first frames were not the terminal protocol's", "The operator checks the control plane's version; a person checks the address.", []string{"connect: the server does not speak sah/3"}},
	{"connect_closed", 1, "connect", "\"connect: closed\": the person typed ~. after Enter and the client closed the session", fixNothing, []string{"connect: closed"}},
	{"connect_interrupted", 130, "connect", "\"connect: interrupted\": Ctrl-C before the session started, or a SIGINT sent to the client during the session (where Ctrl-C is the host's input); the connection was closed and the terminal restored", fixNothing, []string{"connect: interrupted"}},
	{"session_ended", 1, "connect", "\"session ended: <reason>\": the session ended without an exit status from the host's shell; the reason and this build's meaning for it follow", "A person reads the reason.", []string{"session ended:"}},
	{"connection_lost", 1, "connect", "\"session ended: connection lost\": the connection closed with no report of how the session ended; the host stops the session's terminal", "A person reads the host's state before repeating a step.", []string{"session ended: connection lost"}},
	{"protocol", 1, "connect", "\"session ended: protocol\": the server sent a message over 1 MiB, a frame the WebSocket rules forbid (reserved bits, a reserved opcode, a bad control frame), or text that is not UTF-8, and the client closed the session", "The operator checks the control plane.", []string{"session ended: protocol"}},

	// Sent in a pty_status error (the control plane's PTY_ERRORS).
	refusal("ticket_invalid", "the ticket was missing, spent, expired, or issued for another host or purpose", "A person runs the command again; a ticket is used once."),
	refusal("scope_denied", "this login may not open that session; a tenant admin decides who may", "A tenant admin decides who may open a session on this host."),
	refusal("agent_offline", "the host's agent is not connected to the control plane", "The operator checks the host and its agent."),
	refusal("lease_required", "the control plane cannot sign the session's lease", "The operator checks the control plane's signer."),
	refusal("pty_user_unset", "the host has no account configured for terminal sessions", "The operator decides which account the host's sessions use."),
	refusal("account_unmapped", "this login maps to no account on the host", "A tenant admin decides which account this login uses on the host."),
	refusal("recording_unavailable", "the host cannot record the session, so it was not opened", "The operator checks the host's recording."),
	refusal("root_unavailable", "a root session is not available on this host: the host's agent must record it", "The operator checks the host's agent and its recording."),
	refusal("pty_start_failed", "the host could not start the session's terminal, or this login has too many sessions open", "A person decides whether to ask again; the operator checks the host."),
	refusal("internal", "the control plane or the host failed in a way it does not name", "The operator checks the control plane's log."),
	refusal("grant_purpose", "the host's agent refused the session's grant: it was signed for another purpose", "The operator checks the control plane and the agent."),
	refusal("grant_binding", "the host's agent refused the session's grant: it was signed for another host or session", "The operator checks the control plane and the agent."),
	refusal("sash_missing", "the host has no sash to run the session, and no other shell is started in its place", "The operator decides when sash is installed on the host."),

	// Sent as an HTTP refusal of the resolve or the ticket call.
	refusal("invalid_name", "the control plane does not take that host name", "A person types the host's name or id."),
	refusal("not_found", "no host this login can see has that name or id", "A person checks the name; a tenant admin decides which hosts a login sees."),
	refusal("ambiguous", "more than one host has that name", "A person names the host by its id."),
	refusal("bad_purpose", "the control plane does not know the session purpose this client asked for", "The operator checks the control plane's version."),
	refusal("rate_limited", "too many sessions were asked for on this host in the last minute", "A person waits a minute before asking again."),
	refusal("tickets_unavailable", "the control plane cannot issue tickets now", "The operator checks the control plane."),
	refusal("approval_required", "a root session on this host needs an approval; an approver decides", "A person asks for the approval; an approver decides."),
	refusal("forbidden", "this login may not open that session; a tenant admin decides who may", "A tenant admin decides who may open a session on this host."),
	refusal("token_expired", "your login expired; a person logs in again with the systemapi CLI", "A person logs in again with the systemapi CLI."),
	refusal("fresh_token_expired", "a fresh login lasts 5 minutes; a person makes a new fresh login with the systemapi CLI just before opening a root session", "A person makes a new fresh login with the systemapi CLI."),
	refusal("fresh_expired", "a fresh login lasts 5 minutes; a person makes a new fresh login with the systemapi CLI just before opening a root session", "A person makes a new fresh login with the systemapi CLI."),

	// Sent as the reason of a pty_exit that carries no exit status.
	ended("exited", "the host's shell ended and reported no exit status", fixNothing),
	ended("max_duration", "the session reached the longest time a session may last", "A person opens a new session."),
	ended("agent_disconnected", "the host's agent lost its connection to the control plane", "A person opens a new session once the host is back; the operator checks the agent."),
	ended("stopped", "an admin, a policy change, or a re-check of this login stopped the session", "A tenant admin decides whether the session may be opened again."),
}

// sent are the codes a control plane or a host may send to sash connect:
// the connect entries made by refusal and ended, and the grant refusals a
// host's agent reports in a pty_status, which the grant area already names.
var sent = map[string]bool{"grant_missing": true, "grant_bad_shape": true, "grant_star": true, "grant_bad_sig": true, "grant_expired": true}

// refusal is a code sent to refuse a session: printed after "connect
// refused: ", exit 126.
func refusal(code, meaning, fix string) Code {
	sent[code] = true
	return Code{code, 126, "connect", meaning, fix, []string{"connect refused: " + code}}
}

// ended is a pty_exit reason: printed after "session ended: ", exit 1.
func ended(code, meaning, fix string) Code {
	sent[code] = true
	return Code{code, 1, "connect", meaning, fix, []string{"session ended: " + code}}
}

func init() {
	table = append(table, connectTable...)
	sort.Slice(table, func(i, j int) bool { return table[i].Code < table[j].Code })
}

// Connect is the entry for a code a control plane or a host sent to sash
// connect. Only a sent code is found: a code of another area, or one sash
// connect prints itself, is not, so a sender cannot borrow its sentence.
func Connect(code string) (Code, bool) {
	if !sent[code] {
		return Code{}, false
	}
	for _, c := range table {
		if c.Code == code {
			return c, true
		}
	}
	return Code{}, false
}

// Sent reports whether Connect holds code.
func Sent(code string) bool { return sent[code] }

// All is the catalog, sorted by code.
func All() []Code {
	return append([]Code(nil), table...)
}

// Find is the entry whose printed words begin msg: the longest match
// wins, and a match ends at the end of msg or before a space, a colon, or
// a line end.
func Find(msg string) (Code, bool) {
	best, bestLen := -1, -1
	for i, c := range table {
		for _, p := range c.prints {
			if !strings.HasPrefix(msg, p) {
				continue
			}
			rest := msg[len(p):]
			whole := rest == "" || strings.HasSuffix(p, ":") || strings.HasSuffix(p, " ") || strings.ContainsRune(" :\n", rune(rest[0]))
			if whole && len(p) > bestLen {
				best, bestLen = i, len(p)
			}
		}
	}
	if best < 0 {
		return Code{}, false
	}
	return table[best], true
}

// JSON is the catalog as sash codes --json prints its codes field.
func JSON() ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	// The text is read by people and agents, not by a browser: < and >
	// stay as written.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(All()); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(b.String(), "\n")), nil
}

// SHA256 is the hex sha256 of JSON: what sash version --json reports, so
// two builds can be told to carry the same catalog.
func SHA256() (string, error) {
	b, err := JSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
