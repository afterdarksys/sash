package lang

// The service verbs the init performs
// (docs/plans/2026-11-21-sash-init-contract.md, "New verbs").
//
// Threats: a service script that starts a program the grant does not list,
// runs a shell string, signals another service's process or an arbitrary
// pid, gives a capability or mount point the grant does not list, writes
// outside file_write, probes a host beyond loopback, or hands a secret to
// anything but a child's environment. sash never forks, execs, signals or
// mounts: each verb is one call to the init, checked here against the grant
// first, and again by the init. A process is named, never a pid: the init
// keeps a name-to-process table per service. PROGRAM is one absolute path
// with no shell metacharacters and no PATH lookup. The spawn options are a
// [ ] data value, never parsed as source; a secret may be only an env
// value, which the frame marks {"secret": ...} so the init never logs it,
// and which the plan line, the trace and a recording never show.

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"github.com/afterdarksys/sash/internal/parent"
)

// svcKeys are the first words of the service verbs and their keys. service
// status is the one two-word form.
var svcKeys = map[string]string{
	"spawn":  "svc.spawn",
	"signal": "svc.signal",
	"wait":   "svc.wait",
	"proc":   "svc.proc",
	"probe":  "svc.probe",
	"output": "svc.output",
	"mkdir":  "file.write",
	"chown":  "file.write",
	"chmod":  "file.write",
	"touch":  "file.write",
	"ln":     "file.write",
	"rm":     "file.write",
	"mount":  "fs.mount",
	"umount": "fs.mount",
}

// svcFileOps are the file verbs that ride on file.write. Their argv[0]
// is the operation; an agent that knows only "> path" denies them.
var svcFileOps = map[string]bool{"mkdir": true, "chown": true, "chmod": true, "touch": true, "ln": true, "rm": true}

// bootKeys are the keys only a sash/2 boot grant carries: no systemapi
// policy flag grants them.
var bootKeys = map[string]bool{
	"svc.spawn": true, "svc.signal": true, "svc.wait": true, "svc.proc": true,
	"svc.status": true, "svc.probe": true, "svc.output": true, "fs.mount": true,
}

// procName is a name given to spawn: the init's handle for the process.
// It starts with a letter, so it never reads as a pid.
var procName = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

// envRefused are environment names a spawn may not set: the dynamic loader,
// libc's lookup and allocator knobs, and interpreter search paths and
// startup hooks, each of which runs code the grant never named.
var envRefused = map[string]bool{
	"GCONV_PATH": true, "LOCPATH": true, "NLSPATH": true, "HOSTALIASES": true, "RES_OPTIONS": true,
	"GLIBC_TUNABLES": true, "TZDIR": true, "BASH_ENV": true, "ENV": true, "IFS": true, "SHELLOPTS": true,
	"PYTHONPATH": true, "PYTHONHOME": true, "PYTHONSTARTUP": true, "PERLLIB": true, "PERL5LIB": true, "PERL5OPT": true,
	"RUBYLIB": true, "RUBYOPT": true, "NODE_OPTIONS": true, "NODE_PATH": true, "JAVA_TOOL_OPTIONS": true,
	"_JAVA_OPTIONS": true, "JDK_JAVA_OPTIONS": true, "LUA_PATH": true, "LUA_CPATH": true,
}

var envRefusedPrefix = []string{"LD_", "DYLD_", "MALLOC_"}

// envNameOK is "" for an env name spawn may set, else env_refused.
func envNameOK(k string) string {
	if !envName.MatchString(k) {
		return "usage spawn opts env"
	}
	for _, p := range envRefusedPrefix {
		if strings.HasPrefix(k, p) {
			return "env_refused: " + k
		}
	}
	if envRefused[k] {
		return "env_refused: " + k
	}
	return ""
}

var (
	fileMode  = regexp.MustCompile(`^[01]?[0-7]{3}$`)
	umaskRe   = regexp.MustCompile(`^0?[0-7]{3}$`)
	envName   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	fsType    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
	mountOpts = regexp.MustCompile(`^[A-Za-z0-9_=,.:/+-]{1,256}$`)
	mountSrc  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
)

// spawnLimits are the rlimit names spawn opts may set.
var spawnLimits = map[string]bool{"nofile": true, "nproc": true, "core": true, "memlock": true, "stack": true, "as": true, "cpu": true, "fsize": true}

const (
	msgSpawnShell     = "spawn_shell"
	msgSecretRefused  = "secret_refused: a secret may leave sash only as a spawn env value"
	msgDeniedExec     = "denied exec"
	msgDeniedCap      = "denied cap"
	msgDeniedMount    = "denied fs.mount"
	msgDeniedProbe    = "denied probe: not loopback"
	msgDeniedProbePrv = "denied probe: not a private address"
)

// spawnProgram is "" for a program spawn may start: one absolute, clean
// path with no whitespace or shell metacharacter. Anything else is a shell
// string or a PATH lookup, and is spawn_shell.
func spawnProgram(p string) string {
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, " \t\n\r;|&$<>()`'\"*?[]{}\\~#!") {
		return msgSpawnShell
	}
	if _, err := makePath("", p); err != nil || p == "/" {
		return msgSpawnShell
	}
	return ""
}

// probeTCP is "" when HOST:PORT may be probed: an IP literal or localhost
// (no name lookup) and a port 1-65535, on loopback, or with allow_private
// on a private address too. A public address is never probed.
func probeTCP(target string, loopbackOnly bool) string {
	host, port, ok := strings.Cut(target, ":")
	if strings.HasPrefix(target, "[") {
		end := strings.Index(target, "]:")
		if end < 0 {
			return "usage probe tcp HOST:PORT"
		}
		host, port, ok = target[1:end], target[end+2:], true
	} else if strings.Count(target, ":") != 1 {
		return "usage probe tcp HOST:PORT"
	}
	n, err := strconv.Atoi(port)
	if !ok || err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return "usage probe tcp HOST:PORT"
	}
	if host == "localhost" {
		return ""
	}
	a, err := netip.ParseAddr(host)
	if err != nil || a.Zone() != "" {
		return "usage probe tcp HOST:PORT"
	}
	a = a.Unmap()
	if a.IsLoopback() {
		return ""
	}
	if loopbackOnly {
		return msgDeniedProbe
	}
	if a.IsPrivate() || a.IsLinkLocalUnicast() {
		return ""
	}
	return msgDeniedProbePrv
}

// classifySvc is classify for the service verbs: their shape, before any
// grant check. ok is false for a name that is not one.
func classifySvc(argv []string) (classified, *problem, bool) {
	name := argv[0]
	key, ok := svcKeys[name]
	if name == "service" {
		if len(argv) != 3 || argv[1] != "status" || !grantServiceName(argv[2]) {
			return classified{}, &problem{exitType, "usage service status NAME"}, true
		}
		return classified{key: "svc.status", in: inNone, out: inRows}, nil, true
	}
	if !ok {
		return classified{}, nil, false
	}
	usage := func() (classified, *problem, bool) {
		return classified{}, &problem{exitType, "usage " + svcUsage[name]}, true
	}
	out := inBytes
	switch name {
	case "spawn":
		n := len(argv)
		if n >= 2 && argv[n-1] == exprMark {
			if n < 5 || argv[n-2] != "opts" {
				return usage()
			}
			n -= 2
		}
		if n < 3 || !procName.MatchString(argv[1]) {
			return usage()
		}
		if msg := spawnProgram(argv[2]); msg != "" {
			return classified{}, &problem{exitType, msg}, true
		}
		// opts is only ever the marker before a [ ] map: a word after it
		// would reach the program as text.
		for _, a := range argv[3:n] {
			if a == exprMark || a == "opts" || strings.ContainsRune(a, 0) {
				return usage()
			}
		}
		out = inRows
	case "signal":
		if len(argv) != 3 || !procName.MatchString(argv[1]) || !spawnSignals[argv[2]] {
			return usage()
		}
	case "wait":
		if len(argv) != 4 || !procName.MatchString(argv[1]) || argv[2] != "timeout" {
			return usage()
		}
		if _, err := serviceDuration(argv[3]); err != nil {
			return usage()
		}
		out = inRows
	case "proc":
		if len(argv) != 2 || !procName.MatchString(argv[1]) {
			return usage()
		}
		out = inRows
	case "output":
		if len(argv) < 2 || !procName.MatchString(argv[1]) {
			return usage()
		}
		rest := argv[2:]
		if len(rest) > 0 && rest[0] == "stderr" {
			rest = rest[1:]
		}
		if len(rest) == 2 && rest[0] == "tail" {
			if n, err := strconv.Atoi(rest[1]); err != nil || n < 1 || n > 10000 {
				return usage()
			}
			rest = nil
		}
		if len(rest) != 0 {
			return usage()
		}
		out = inRows
	case "probe":
		if len(argv) != 3 {
			return usage()
		}
		switch argv[1] {
		case "tcp":
			if msg := probeTCP(argv[2], false); strings.HasPrefix(msg, "usage") {
				return usage()
			}
		case "unix", "file":
		case "notify":
			if !procName.MatchString(argv[2]) {
				return usage()
			}
		default:
			return usage()
		}
	case "mkdir":
		if len(argv) < 2 || !svcOptions(argv[2:], map[string]func(string) bool{"mode": fileMode.MatchString, "owner": ownerOK}) {
			return usage()
		}
	case "chown":
		if len(argv) != 3 || !ownerOK(argv[2]) {
			return usage()
		}
	case "chmod":
		if len(argv) != 3 || !fileMode.MatchString(argv[2]) {
			return usage()
		}
	case "touch", "rm", "umount":
		if len(argv) != 2 {
			return usage()
		}
	case "ln":
		if len(argv) != 3 {
			return usage()
		}
	case "mount":
		if len(argv) < 3 || !svcOptions(argv[3:], map[string]func(string) bool{"type": fsType.MatchString, "opts": mountOpts.MatchString}) {
			return usage()
		}
		if src := argv[1]; !mountSrc.MatchString(src) {
			if _, err := makePath("", src); err != nil {
				return usage()
			}
		}
	}
	// No path or name starts with -: rm and the rest take no flags.
	for _, a := range argv[1:] {
		if strings.HasPrefix(a, "-") && name != "spawn" {
			return usage()
		}
	}
	return classified{key: key, in: inNone, out: out}, nil, true
}

var svcUsage = map[string]string{
	"spawn":  "spawn NAME PROGRAM args... [opts [ {map} ]]",
	"signal": "signal NAME SIG",
	"wait":   "wait NAME timeout D",
	"proc":   "proc NAME",
	"output": "output NAME [stderr] [tail N]",
	"probe":  "probe tcp HOST:PORT | unix PATH | file PATH | notify NAME",
	"mkdir":  "mkdir PATH [mode M] [owner U:G]",
	"chown":  "chown PATH U:G",
	"chmod":  "chmod PATH M",
	"touch":  "touch PATH",
	"ln":     "ln PATH TARGET",
	"rm":     "rm PATH",
	"mount":  "mount SRC DIR [type T] [opts O]",
	"umount": "umount DIR",
}

func grantServiceName(s string) bool { return procName.MatchString(s) }

// ownerOK is U:G, each a user or group name.
func ownerOK(s string) bool {
	u, g, ok := strings.Cut(s, ":")
	return ok && unixName.MatchString(u) && unixName.MatchString(g)
}

// svcOptions is pairs of option and value, each option once.
func svcOptions(rest []string, opts map[string]func(string) bool) bool {
	if len(rest)%2 != 0 {
		return false
	}
	seen := map[string]bool{}
	for i := 0; i < len(rest); i += 2 {
		ok, known := opts[rest[i]]
		if !known || seen[rest[i]] || !ok(rest[i+1]) {
			return false
		}
		seen[rest[i]] = true
	}
	return true
}

// svcPathArgs are the argv indexes of a service verb that are paths.
func svcPathArgs(argv []string) []int {
	switch argv[0] {
	case "mkdir", "chown", "chmod", "touch", "rm", "umount":
		return []int{1}
	case "ln":
		return []int{1, 2}
	case "mount":
		if strings.HasPrefix(argv[1], "/") {
			return []int{1, 2}
		}
		return []int{2}
	case "probe":
		if argv[1] == "unix" || argv[1] == "file" {
			return []int{2}
		}
	case "spawn":
		return []int{2}
	}
	return nil
}

// bindSvc checks a service verb's targets against the grant: PROGRAM in
// exec, a path in file_write (file_read for a probe), a mount point in
// mounts, a tcp probe on loopback. Each path follows the v7 rules: in a
// script it is literal text or a path value, and it is made absolute and
// clean (relative to the prefix) before the check, so the init gets the
// path that was checked.
func (m *machine) bindSvc(st *expStage) (int, *problem) {
	argv := st.argv
	for _, i := range svcPathArgs(argv) {
		if !m.prompt && i < len(st.argData) && st.argData[i] {
			return i, &problem{exitType, "type path"}
		}
		if argv[0] == "spawn" {
			continue
		}
		full, err := makePath(m.prefix(), argv[i])
		if err != nil {
			return i, &problem{exitType, err.Error()}
		}
		argv[i] = string(full)
		if st.path == "" {
			st.path = string(full)
		}
	}
	switch argv[0] {
	case "spawn":
		if !m.g.AllowsExec(argv[2]) {
			return 2, &problem{exitDenied, msgDeniedExec}
		}
	case "mkdir", "chown", "chmod", "touch", "rm":
		if !m.g.AllowsWrite(argv[1]) {
			return 1, &problem{exitDenied, "denied file.write"}
		}
	case "ln":
		for _, i := range []int{1, 2} {
			if !m.g.AllowsWrite(argv[i]) {
				return i, &problem{exitDenied, "denied file.write"}
			}
		}
	case "mount", "umount":
		dir := 1
		if argv[0] == "mount" {
			dir = 2
		}
		if !m.g.AllowsMount(argv[dir]) {
			return dir, &problem{exitDenied, msgDeniedMount}
		}
		if argv[0] == "mount" {
			if i, prob := m.mountChecks(argv); prob != nil {
				return i, prob
			}
		}
	case "probe":
		switch argv[1] {
		case "tcp":
			if msg := probeTCP(argv[2], !m.g.AllowPrivate); msg != "" {
				return 2, &problem{exitDenied, msg}
			}
		case "unix", "file":
			if !m.g.AllowsRead(argv[2]) {
				return 2, &problem{exitDenied, "denied file.read"}
			}
		}
	}
	return -1, nil
}

// svcRefusals are the init's refusals of a service verb, which sash prints
// with the reason and exits 2 on: the init does not restart a service for
// them. timeout is wait's.
var svcRefusals = map[string]string{
	"unknown_name": "no process of this service has that name",
	"name_in_use":  "a process of this service already has that name",
	"timeout":      "the process did not end in time",
	"denied":       "the init refused it",
	"ceiling":      "past the init's ceiling for caps or limits",
}

// runSpawn evaluates the options and makes the one svc.spawn call.
func (m *machine) runSpawn(st expStage) (value, int) {
	body, secret, prob := m.spawnOpts(st.x)
	if prob != nil {
		m.err.WriteString(prob.msg + "\n")
		return value{}, prob.status
	}
	argv := st.argv
	if n := len(argv); n >= 2 && argv[n-1] == exprMark {
		argv = argv[:n-2]
	}
	v := parent.Verb{Key: "svc.spawn", Argv: append([]string(nil), argv...), Body: body}
	saw := m.sawSecret
	m.sawSecret = m.sawSecret || secret
	res, err := m.do(v)
	m.sawSecret = saw
	if err != nil {
		return value{}, m.doFailed("svc.spawn", err)
	}
	return fromResult(res), res.Exit
}

// spawnOpts is the options map as the JSON the init reads, with the
// service block's user and group as defaults. A secret is allowed only as
// a value of a literal env map: each value there is evaluated on its own,
// so the secret is known to be that value and nothing else. Anywhere else
// it is secret_refused.
func (m *machine) spawnOpts(x Expr) ([]byte, bool, *problem) {
	fail := func(msg string) ([]byte, bool, *problem) { return nil, false, &problem{exitType, msg} }
	opts := map[string]any{}
	given := map[string]any{}
	secret := false
	eval := func(x Expr, allowSecret bool) (any, *problem) {
		d, err := m.evalX(x)
		if err != nil {
			return nil, &problem{exitType, err.Error()}
		}
		if inner, was := unsecret(d); was {
			if !allowSecret {
				return nil, &problem{exitType, msgSecretRefused}
			}
			s, ok := inner.(string)
			if !ok {
				return nil, &problem{exitType, "usage spawn opts: an env value is a string"}
			}
			secret = true
			return map[string]any{"secret": s}, nil
		}
		if containsSecret(d) {
			return nil, &problem{exitType, msgSecretRefused}
		}
		return d, nil
	}
	switch mx := x.(type) {
	case nil:
	case exprMap:
		for i, k := range mx.keys {
			if k == "env" {
				if em, ok := mx.vals[i].(exprMap); ok {
					env := map[string]any{}
					for j, ek := range em.keys {
						d, prob := eval(em.vals[j], true)
						if prob != nil {
							return nil, false, prob
						}
						env[ek] = d
					}
					given[k] = env
					continue
				}
			}
			d, prob := eval(mx.vals[i], false)
			if prob != nil {
				return nil, false, prob
			}
			given[k] = d
		}
	default:
		d, prob := eval(x, false)
		if prob != nil {
			return nil, false, prob
		}
		o, ok := d.(*omap)
		if !ok {
			return fail("usage spawn opts: a map")
		}
		for _, k := range o.keys {
			given[k] = o.m[k]
		}
	}
	for k, d := range given {
		v, prob := m.spawnOpt(k, d)
		if prob != nil {
			return nil, false, prob
		}
		opts[k] = v
	}
	// Omitted means the block's identity, which is the grant's.
	if _, ok := opts["user"]; !ok && m.g.User != "" {
		opts["user"] = m.g.User
	}
	if _, ok := opts["group"]; !ok && m.g.Group != "" {
		opts["group"] = m.g.Group
	}
	b, err := json.Marshal(opts)
	if err != nil {
		return fail("usage spawn opts")
	}
	return b, secret, nil
}

// spawnOpt checks one option and returns it as JSON data.
func (m *machine) spawnOpt(k string, d any) (any, *problem) {
	bad := func() (any, *problem) { return nil, &problem{exitType, "usage spawn opts " + needsCell(k)} }
	str := func() (string, bool) {
		switch x := d.(type) {
		case string:
			return x, true
		case pathV:
			return string(x), true
		}
		return "", false
	}
	switch k {
	case "user", "group":
		s, ok := str()
		if !ok || !unixName.MatchString(s) {
			return bad()
		}
		// Only the grant's identity, which the service block must match:
		// root only when the grant itself says root.
		want := m.g.User
		if k == "group" {
			want = m.g.Group
		}
		if s != want {
			return nil, &problem{exitDenied, "denied " + k}
		}
		return s, nil
	case "cwd":
		s, ok := str()
		if !ok {
			return bad()
		}
		p, err := makePath(m.prefix(), s)
		if err != nil {
			return bad()
		}
		return string(p), nil
	case "umask":
		s, ok := str()
		if !ok || !umaskRe.MatchString(s) {
			return bad()
		}
		return s, nil
	case "env":
		var env map[string]any
		switch x := d.(type) {
		case map[string]any:
			env = x
		case *omap:
			env = map[string]any{}
			for _, ek := range x.keys {
				env[ek] = x.m[ek]
			}
		default:
			return bad()
		}
		for ek, ev := range env {
			if msg := envNameOK(ek); msg != "" {
				return nil, &problem{exitType, msg}
			}
			switch x := ev.(type) {
			case string:
				if strings.ContainsRune(x, 0) {
					return bad()
				}
			case pathV:
				env[ek] = string(x)
			case map[string]any:
				// {"secret": s}, from spawnOpts only.
			default:
				return bad()
			}
		}
		return env, nil
	case "limits":
		o, ok := d.(*omap)
		if !ok {
			return bad()
		}
		out := map[string]int64{}
		for _, lk := range o.keys {
			n, isInt := o.m[lk].(int64)
			if !spawnLimits[lk] || !isInt || n < 0 {
				return bad()
			}
			out[lk] = n
		}
		return out, nil
	case "caps":
		list, ok := d.([]any)
		if !ok {
			return bad()
		}
		out := []string{}
		for _, c := range list {
			s, isStr := c.(string)
			if !isStr {
				return bad()
			}
			if !m.g.AllowsCap(s) {
				return nil, &problem{exitDenied, msgDeniedCap}
			}
			out = append(out, s)
		}
		return out, nil
	case "stdout", "stderr":
		s, ok := str()
		if !ok {
			return bad()
		}
		switch {
		case s == "journal" || s == "null" || (k == "stderr" && s == "stdout"):
			return s, nil
		case strings.HasPrefix(s, "file:"):
			p, err := makePath("", strings.TrimPrefix(s, "file:"))
			if err != nil {
				return bad()
			}
			if !m.g.AllowsWrite(string(p)) {
				return nil, &problem{exitDenied, "denied file.write"}
			}
			return "file:" + string(p), nil
		}
		return bad()
	}
	return bad()
}

// svcPlan is the plan line of a service verb: what would run, never an
// option's value. "" for any other verb.
func svcPlan(v parent.Verb) string {
	if len(v.Argv) == 0 {
		return ""
	}
	isSvc := strings.HasPrefix(v.Key, "svc.") || v.Key == "fs.mount" || (v.Key == "file.write" && svcFileOps[v.Argv[0]])
	if !isSvc {
		return ""
	}
	words := make([]string, len(v.Argv))
	for i, a := range v.Argv {
		words[i] = needsCell(a)
		if a == "" || strings.ContainsAny(words[i], " ") {
			words[i] = "'" + words[i] + "'"
		}
	}
	line := strings.Join(words, " ")
	if v.Key == "svc.spawn" {
		var o struct {
			User  string `json:"user"`
			Group string `json:"group"`
		}
		_ = json.Unmarshal(v.Body, &o)
		switch {
		case o.User != "" && o.Group != "":
			line += " as " + o.User + ":" + o.Group
		case o.User != "":
			line += " as " + o.User
		}
	}
	return line
}

// svcDoFailed is doFailed for an init refusal of a service verb.
func (m *machine) svcDoFailed(key string, err error) (int, bool) {
	why, ok := svcRefusals[err.Error()]
	if !ok || !(strings.HasPrefix(key, "svc.") || key == "fs.mount" || key == "file.write") {
		return 0, false
	}
	if err.Error() == "timeout" {
		m.err.WriteString("timeout\n")
		return exitType, true
	}
	m.err.WriteString(fmt.Sprintf("refused %s: %s (%s)\n", key, err.Error(), why))
	return exitType, true
}

// svcStage is needs for a service verb: the key and its literal target. A
// spawn lists its program, each literal capability as "cap NAME", and a
// file: stream as a file.write; a probe lists "KIND TARGET". Options that
// are not a literal map are dynamic: they may carry a capability or a
// stream needs cannot see.
func (n *needsRun) svcStage(name string, st Stage, b needBase) {
	args := st.Argv[1:]
	at := st.Argv[0].Pos
	switch name {
	case "service":
		if len(args) == 2 && plain(args[0]) == "status" {
			n.value("svc.status", args[1], false)
		}
	case "signal", "wait", "proc", "output":
		if len(args) >= 1 {
			n.value(svcKeys[name], args[0], false)
		}
	case "spawn":
		if len(args) < 2 {
			return
		}
		n.value("svc.spawn", args[1], false)
		last := len(args) - 1
		if last >= 3 && plain(args[last-1]) == "opts" {
			if x := exprOf(args[last]); x != nil {
				n.spawnOptsNeeds(x, args[last].Pos)
			}
		}
	case "probe":
		if len(args) != 2 {
			return
		}
		kind, target := plain(args[0]), plain(args[1])
		switch {
		case kind == "" || target == "":
			n.add("svc.probe", "$", noteDynamic, at, "")
		case kind == "unix" || kind == "file":
			if full, ok := b.resolve(target); ok {
				n.add("svc.probe", kind+" "+full, "", at, "")
			} else {
				n.add("svc.probe", kind+" "+target, noteRelative, at, "")
			}
		default:
			n.add("svc.probe", kind+" "+target, "", at, "")
		}
	case "mkdir", "chown", "chmod", "touch", "rm":
		if len(args) >= 1 {
			n.path("file.write", args[0], needFile, b)
		}
	case "ln":
		for _, w := range args {
			n.path("file.write", w, needFile, b)
		}
	case "mount":
		if len(args) >= 2 {
			n.value("fs.mount", args[1], false)
			if src := plain(args[0]); src == "" {
				n.add("fs.mount", "source $", noteDynamic, at, "")
			} else {
				n.add("fs.mount", "source "+src, "", at, "")
			}
		}
	case "umount":
		if len(args) == 1 {
			n.value("fs.mount", args[0], false)
		}
	}
}

func (n *needsRun) spawnOptsNeeds(x Expr, at int) {
	mx, ok := x.(exprMap)
	if !ok {
		n.add("svc.spawn", "opts", noteDynamic, at, "")
		return
	}
	for i, k := range mx.keys {
		switch k {
		case "caps":
			list, ok := mx.vals[i].(exprList)
			if !ok {
				n.add("svc.spawn", "cap $", noteDynamic, at, "")
				continue
			}
			for _, it := range list.items {
				lit, ok := it.(exprLit)
				s, isStr := lit.d.(string)
				if !ok || !isStr {
					n.add("svc.spawn", "cap $", noteDynamic, at, "")
					continue
				}
				n.add("svc.spawn", "cap "+s, "", at, "")
			}
		case "stdout", "stderr":
			lit, ok := mx.vals[i].(exprLit)
			s, isStr := lit.d.(string)
			if !ok || !isStr {
				n.add("file.write", "$", noteDynamic, at, "")
				continue
			}
			if p, ok := strings.CutPrefix(s, "file:"); ok {
				n.literalPath("file.write", p, needFile, needBase{"": true}, at)
			}
		}
	}
}

// lintShells are programs that would run whatever command they are given:
// spawning one undoes "no shell", since the grant's exec then admits any
// command line.
var lintShells = map[string]bool{"sh": true, "bash": true, "dash": true, "zsh": true, "ash": true, "ksh": true, "busybox": true, "env": true}

// serviceRules is spawn-shell (a spawn, or the service block's run, starts
// a shell) and check-mutates (a service file's fn check reaches a
// mutation, which the run refuses: check is read-only).
func (l *linter) serviceRules() {
	shell := func(at int, prog string) {
		if i := strings.LastIndexByte(prog, '/'); i >= 0 && lintShells[prog[i+1:]] {
			l.add(at, "spawn-shell", "spawn starts "+needsCell(prog)+", which runs any command it is given")
		}
	}
	svc := serviceOf(l.prog)
	if svc != nil && len(svc.Run) > 0 {
		shell(l.prog.Pos[0], svc.Run[0])
	}
	lintEach(l.prog, true, func(s Stmt, at int, _ bool) {
		lintEachPipe(s, func(pl Pipeline, _ string) {
			for _, st := range pl.Stages {
				if len(st.Argv) >= 3 && plain(st.Argv[0]) == "spawn" {
					shell(st.Argv[0].Pos, plain(st.Argv[2]))
				}
			}
		})
	})
	if svc == nil {
		return
	}
	for i, s := range l.prog.Stmts {
		if fn, ok := s.(*Fn); ok && fn.Name == "check" {
			if what := l.mutates(&fn.Body, 0); what != "" {
				l.add(l.prog.Pos[i], "check-mutates", "fn check reaches "+what+"; check runs read-only, so the run refuses it")
			}
		}
	}
}

// mountRebinds are the mount options that bring an existing tree to DIR.
var mountRebinds = map[string]bool{"bind": true, "rbind": true, "remount": true}

// mountChecks is mount SRC DIR's source and overlap rules: SRC is one of
// the grant's mount_sources or beneath one; a bind, rbind or remount takes
// a path SRC, never a filesystem name; and DIR neither lies at or under a
// file_write root nor holds one (mount_overlap), so a mount cannot turn
// file_write onto another tree, or a write root into the mounted one.
func (m *machine) mountChecks(argv []string) (int, *problem) {
	src, dir := argv[1], argv[2]
	rebind := false
	for i := 3; i+1 < len(argv); i += 2 {
		vals := []string{argv[i+1]}
		if argv[i] == "opts" {
			vals = strings.Split(argv[i+1], ",")
		}
		for _, v := range vals {
			if mountRebinds[v] {
				rebind = true
			}
		}
	}
	if !m.g.AllowsMountSource(src) {
		return 1, &problem{exitDenied, "denied mount source"}
	}
	if rebind && !strings.HasPrefix(src, "/") {
		return 1, &problem{exitType, "usage mount: bind, rbind and remount take a path"}
	}
	for _, root := range m.g.FileWrite {
		if pathStarts(pathV(dir), pathV(root)) || pathStarts(pathV(root), pathV(dir)) {
			return 2, &problem{exitDenied, "mount_overlap"}
		}
	}
	return -1, nil
}
