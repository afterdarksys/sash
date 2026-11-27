// Package codes is the catalog of what sash prints when it refuses or
// fails: one table, compiled in, printed by sash codes --json.
//
// Threats: catalog text read by an agent as an instruction, and a catalog
// that drifts from the binary. Every fix sentence that names a change
// has a person, the operator, or an admin as its subject or decider;
// none says to turn a mode off, widen a grant, use another credential or
// channel, or names an override, and a test holds each to a banned-phrase
// list and to a reviewed fixture. A second test scans the fixed messages
// of internal/lang, internal/grant, internal/session and internal/proto
// and fails when one has no entry here or its exit differs. The catalog
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

// Areas are the catalog's areas. connect, tls, and fetch have no entry
// until those features exist.
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
	{"type", 2, "parse", "a value or a pipe has the wrong shape: bytes into |>, an unset name, a failed [ ] expression", fixScript, []string{"type"}},
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
	{"grant_expired", 2, "grant", "the grant's expiry has passed", "A person opens a new session.", []string{"grant_expired"}},
	{"grant_star", 2, "grant", "the grant holds \"*\", which sash never accepts as an entitlement", "The operator checks the control plane; no verb ran.", []string{"grant_star"}},
	{"grant_root_slash", 2, "grant", "the grant names \"/\" as a file root, which sash never accepts", "The operator checks the control plane; no verb ran.", []string{"grant_root_slash"}},

	// verb: a call to the host's agent.
	{"declined", 126, "verb", "a person answered no at a confirm question; the verb did not run", fixNothing, []string{"declined"}},
	{"confirm_no_terminal", 2, "verb", "\"confirm needs a terminal\": confirm mode is on and there is no terminal to ask, so the mutating verb did not run", "A person runs the mutating step at a terminal.", []string{"confirm needs a terminal"}},
	{"confirm_locked", 2, "verb", "\"confirm is locked\": confirm mode cannot be cleared from a script or a line", fixNothing, []string{"confirm is locked"}},
	{"parent", 1, "verb", "\"parent <key>\": the call to the agent failed or the agent could not perform it", "A person reads the host's state before running the step again; the operator checks the agent.", []string{"parent"}},
	{"refused", 2, "file", "\"refused file.write: <code>\": the agent refused a file write; the code and its meaning follow", "A person reads the code; the file is as the agent reports it.", []string{"refused"}},
	{"fs_not_written", 1, "file", "\"fs write-atomic: not written\": an earlier stage of the pipeline failed, so nothing was sent and the file is as it was; the status is the failing stage's", "A person reads the failing stage's output above this line.", []string{"fs write-atomic: not written"}},
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
	{"secret_cannot_leave", 2, "secret", "\"a secret cannot leave sash\": a secret was a verb argument, a path, a > body, or a fn argument", "A person keeps the secret inside sash; the binding is what makes it secret.", []string{"a secret cannot leave sash"}},
	{"secret_cannot_decide", 2, "secret", "\"a secret cannot decide a branch\": a secret was an if, while, or for condition", fixScript, []string{"a secret cannot decide a branch"}},
	{"secret_fault", 2, "secret", "\"a secret ended the line\": an evaluation that read a secret failed, and the line ended so the failure tells nothing", fixScript, []string{"a secret ended the line"}},
	{"secret_in_word", 2, "secret", "\"a secret in a word: build it inside [ ]\": a secret was pasted into a word", fixScript, []string{"a secret in a word"}},

	// term: asking the person.
	{"term_no_terminal", 2, "term", "\"term needs a terminal\": the script asks a person and there is no terminal to ask; it stopped there", "A person runs the script at a terminal.", []string{"term needs a terminal"}},
	{"term_eof", 1, "term", "\"term prompt: end of input\": Ctrl-D on an empty line, or the input ended; the value is empty", fixNothing, []string{"term prompt: end of input", "end of input"}},
	{"term_interrupted", 130, "term", "\"term: interrupted\": Ctrl-C at a question; the script stopped", fixNothing, []string{"term: interrupted", "interrupted"}},
	{"term_answer_long", 2, "term", "\"term prompt: answer over 4096 bytes\": the rest of the line was discarded and there is no value", "A person gives a shorter answer.", []string{"term prompt: answer over 4096 bytes", "answer over 4096 bytes"}},
	{"term_answer_not_text", 2, "term", "\"term prompt: answer is not text\": the answer is not UTF-8 and there is no value", "A person types the answer as text.", []string{"term prompt: answer is not text", "answer is not text"}},
	{"term_inside_par", 2, "term", "\"term inside par\": a question inside a par branch", "A person asks before the par.", []string{"term inside par"}},
	{"term_secret_misplaced", 2, "term", "\"term prompt -s: only as declare -s name = (term prompt -s \"question\")\": -s anywhere else", "A person binds the secret with declare -s.", []string{"term prompt -s"}},

	// replay and record.
	{"record_failed", 2, "replay", "the recording could not be built or written", "A person records again.", []string{"record_failed"}},
	{"record_too_large", 2, "replay", "the recording passed its size or event cap", "A person records a shorter run.", []string{"record_too_large"}},
	{"replay_bad_format", 2, "replay", "the recording is not a sash recording, or a line of it is malformed", "A person checks which file was given.", []string{"replay_bad_format"}},
	{"replay_bad_chain", 2, "replay", "the recording's hash chain does not hold: a line was changed, removed, or added", "A person records again; this file is not what was recorded.", []string{"replay_bad_chain"}},
	{"replay_bad_segment", 2, "replay", "a segment of a segmented recording is missing or out of order", "A person gives the first segment, with the others beside it.", []string{"replay_bad_segment"}},
	{"replay_diverged", 2, "replay", "the script did something the recording does not hold at that point", "A person replays the script that was recorded, unchanged.", []string{"replay_diverged"}},
	{"replay_expired", 2, "replay", "the recording is past its expiry", "A person records again.", []string{"replay_expired"}},
	{"replay_module", 2, "replay", "a wasm module's hash differs from the one in the recording", "A person replays with the module that was recorded.", []string{"replay_module"}},
	{"replay_other_script", 2, "replay", "the recording was made from another script", "A person replays the script that was recorded.", []string{"replay_other_script"}},
	{"replay_secret", 2, "replay", "the replay reached a term prompt -s; a recording never holds that answer", "A person runs the script at a terminal instead of replaying it.", []string{"replay_secret"}},
	{"replay_too_large", 2, "replay", "the recording is larger than a replay reads", "A person checks which file was given.", []string{"replay_too_large"}},
	{"replay_truncated", 2, "replay", "the recording stopped at its size cap, and the run went past that point", "A person records a shorter run.", []string{"replay_truncated"}},
}

func init() {
	sort.Slice(table, func(i, j int) bool { return table[i].Code < table[j].Code })
}

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
