package lang

// The boot verbs (docs/plans/2026-11-24-sash-boot-verbs-contract.md):
// module load, device wait, mount ensure, network apply, service start and
// service stop, performed by the init under a sash/3 boot grant.
//
// Threats: a boot script that names a path, an alias, a module parameter,
// a mount target, an address or an interface; a name its sash/3 grant does
// not list; a sash/1 or sash/2 grant that lists a boot key; a boot verb
// outside the start entry, inside await, check or par (so boot order is
// never the order written), or inside atomic (none of them can be
// restored); more than 64 boot verbs per run; a secret as a name. Every
// form takes exactly one name, never a flag, and sash sends only Key and
// Argv: no Path, Body or Mode. sash never opens /dev, /sys, /lib/modules,
// a netlink socket or a mount fd: each verb is one call to the init, which
// checks it all again against its own copy of the grant and root's host
// declarations, and performs it with kernel interfaces. Does NOT decide
// what a name means; does NOT count per boot (the init does, sash counts
// per run); does NOT protect against a signer who lists too much.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

// bootForm is one boot verb: its two words, key, name grammar, what the
// grant's list is called in a denial, and its usage.
type bootForm struct {
	verb, sub, key string
	name           *regexp.Regexp
	denied         string
	usage          string
	allows         func(grant.Grant, string) bool
}

var bootForms = []bootForm{
	{"module", "load", "boot.module", grant.ModuleName, "denied module", "usage module load NAME", grant.Grant.AllowsModule},
	{"device", "wait", "boot.device", grant.BootName, "denied device", "usage device wait NAME timeout D", grant.Grant.AllowsDevice},
	{"mount", "ensure", "boot.mount", grant.BootName, "denied host mount", "usage mount ensure NAME", grant.Grant.AllowsHostMount},
	{"network", "apply", "boot.net", grant.BootName, "denied network profile", "usage network apply NAME", grant.Grant.AllowsNetProfile},
	{"service", "start", "boot.start", grant.UnitName, "denied service start", "usage service start NAME", grant.Grant.AllowsUnitStart},
	{"service", "stop", "boot.stop", grant.UnitName, "denied service stop", "usage service stop NAME", grant.Grant.AllowsUnitStop},
}

// bootWords are the first words that only ever begin a boot verb.
var bootWords = map[string]bool{"module": true, "device": true, "network": true}

// bootField maps every key only a boot grant carries to the signed field
// that names its targets (D5): the boot verbs to their sash/3 list, the
// service verbs to the sash/2 field. bootKeys is its key set.
var bootField = map[string]string{
	"boot.module": "modules", "boot.device": "devices", "boot.mount": "host_mounts",
	"boot.net": "net_profiles", "boot.start": "units_start", "boot.stop": "units_stop",
	"svc.spawn": "exec", "fs.mount": "mounts",
	"svc.signal": "entitlements", "svc.wait": "entitlements", "svc.proc": "entitlements",
	"svc.status": "entitlements", "svc.probe": "entitlements", "svc.output": "entitlements",
}

// bootKeys are the keys no systemapi policy flag grants: a boot grant
// carries them, sash/2 or sash/3.
var bootKeys = func() map[string]bool {
	out := map[string]bool{}
	for k := range bootField {
		out[k] = true
	}
	return out
}()

// bootOpsKey is a key only a sash/3 boot grant carries.
func bootOpsKey(key string) bool { return strings.HasPrefix(key, "boot.") }

const (
	// bootMax is how many boot verbs one run may call. set cannot raise
	// it: it is not a session option.
	bootMax = 64

	bootWaitMin = time.Second
	bootWaitMax = 60 * time.Second

	msgBootEntry   = "boot_entry: a boot verb runs only in the start entry"
	msgBootInAwait = "boot_in_await: a boot verb never runs in await or check"
	msgBootInPar   = "boot_in_par: a boot verb runs in the order written, never in par"
	msgBootLimit   = "boot_call_limit: at most 64 boot verbs per boot"
	msgBootWait    = "usage device wait: timeout 1s to 60s"
)

// isSvcWord is a first word of a service or boot verb: a secret there is
// secret_refused, not usage.
func isSvcWord(name string) bool {
	_, svc := svcKeys[name]
	return svc || name == "service" || bootWords[name]
}

// bootFormOf is the boot form argv begins with, or nil. A mount stage
// whose second word is ensure is always mount ensure.
func bootFormOf(argv []string) *bootForm {
	if len(argv) == 0 {
		return nil
	}
	for i := range bootForms {
		f := &bootForms[i]
		if argv[0] != f.verb {
			continue
		}
		if bootWords[f.verb] || (len(argv) > 1 && argv[1] == f.sub) {
			return f
		}
	}
	return nil
}

// classifyBoot is classify for the boot verbs: the shape only. ok is false
// for a stage that is not one.
func classifyBoot(argv []string) (classified, *problem, bool) {
	f := bootFormOf(argv)
	if f == nil {
		return classified{}, nil, false
	}
	usage := func(msg string) (classified, *problem, bool) {
		return classified{}, &problem{exitType, msg}, true
	}
	if len(argv) < 2 || argv[1] != f.sub {
		return usage(f.usage)
	}
	if f.key == "boot.device" {
		if len(argv) != 5 || !f.name.MatchString(argv[2]) || argv[3] != "timeout" {
			return usage(f.usage)
		}
		if _, ok := bootWaitSeconds(argv[4]); !ok {
			return usage(msgBootWait)
		}
	} else if len(argv) != 3 || !f.name.MatchString(argv[2]) {
		return usage(f.usage)
	}
	return classified{key: f.key, in: inNone, out: inRows}, nil, true
}

// bootWaitSeconds reads device wait's timeout: a Go duration of whole
// seconds, 1s to 60s. A fraction is refused, never rounded.
func bootWaitSeconds(s string) (int, bool) {
	d, err := time.ParseDuration(s)
	if err != nil || d < bootWaitMin || d > bootWaitMax || d%time.Second != 0 {
		return 0, false
	}
	return int(d / time.Second), true
}

// bindBoot checks a boot verb before anything runs: the entry is start,
// it is not in await, check or par, and the grant's list names it (the
// per-run budget is runBoot's, where the call is counted). The key and the grant (sash/3 purpose boot)
// were checked in prepare. device wait's timeout goes to the init as whole
// seconds.
func (m *machine) bindBoot(st *expStage) (int, *problem) {
	f := bootFormOf(st.argv)
	if f == nil {
		return -1, &problem{exitType, "usage boot"}
	}
	if m.entry != "start" {
		return -1, &problem{exitType, msgBootEntry}
	}
	if m.polling {
		// Never a not-ready to poll again: the run stops.
		m.ctl = ctlHalt
		return -1, &problem{exitType, msgBootInAwait}
	}
	if m.pb != nil {
		return -1, &problem{exitType, msgBootInPar}
	}
	if !f.allows(m.g, st.argv[2]) {
		return 2, &problem{exitDenied, f.denied}
	}
	if f.key == "boot.device" {
		secs, _ := bootWaitSeconds(st.argv[4])
		st.argv[4] = strconv.Itoa(secs)
	}
	return -1, nil
}

// runBoot makes the one call: Key and Argv only.
func (m *machine) runBoot(st expStage) (value, int) {
	if m.st.bootCalls >= bootMax {
		m.err.WriteString(msgBootLimit + "\n")
		return value{}, exitType
	}
	m.st.bootCalls++
	res, err := m.do(parent.Verb{Key: st.key, Argv: append([]string(nil), st.argv...)})
	if err != nil {
		return value{}, m.doFailed(st.key, err)
	}
	return fromResult(res), res.Exit
}

// bootPlan is a boot verb's plan line, or "".
func bootPlan(v parent.Verb) string {
	if !bootOpsKey(v.Key) || len(v.Argv) < 3 {
		return ""
	}
	name := needsCell(v.Argv[2])
	switch v.Key {
	case "boot.module":
		return "load module " + name
	case "boot.device":
		if len(v.Argv) == 5 {
			return "wait for device " + name + " (up to " + needsCell(v.Argv[4]) + "s)"
		}
	case "boot.mount":
		return "ensure mount " + name
	case "boot.net":
		return "apply network " + name
	case "boot.start":
		return "start service " + name
	case "boot.stop":
		return "stop service " + name
	}
	return ""
}

// planStops is a key plan mode prints and does not call: every mutation,
// and device wait, whose outcome depends on the module loads plan skipped.
func planStops(key string) bool { return mutating[key] || key == "boot.device" }

// bootRefusals are the init's refusal codes for a boot verb, printed as
// refused boot.KEY: CODE (why), exit 2.
var bootRefusals = map[string]string{
	"boot_over":             "the boot waves have ended; boot verbs run only during boot",
	"boot_entry":            "the session's entry is not start",
	"boot_call_limit":       "more than 64 boot verbs for this service in this boot",
	"boot_once":             "a boot verb already started or stopped this unit in this boot",
	"boot_self":             "a unit cannot start or stop itself",
	"boot_chain":            "the unit is itself a boot-ops unit",
	"boot_cycle":            "the unit needs or comes after this one",
	"boot_unit_auto":        "the unit is a boot unit; the boot waves start it",
	"boot_essential":        "the unit is essential; strict boot promises it",
	"module_disabled":       "module loading is disabled on this host",
	"module_sig_unenforced": "the kernel does not enforce module signatures",
	"module_tree":           "the module tree or modules.dep failed the trusted checks",
	"module_unknown":        "modules.dep has no module of that name",
	"module_failed":         "the kernel refused the module",
	"device_unsafe":         "/dev is not devtmpfs, or the node is a symlink or of the wrong kind",
	"device_mismatch":       "the device's identity is not the declared one",
	"device_absent":         "the device is not present",
	"device_changed":        "the device is not the one device wait checked",
	"mount_target":          "the mount target is not a root-owned directory",
	"mount_conflict":        "something else is mounted there",
	"mount_failed":          "the kernel refused the mount",
	"mount_overlap":         "the target is at, under or over a file_write root",
	"not_mounted":           "the mount is not one the init made",
	"net_unknown_link":      "no single link has the profile's address",
	"net_unguarded":         "a unit the profile requires is not ready",
	"net_failed":            "the kernel refused a network change",
	"sash_boot_ops":         "the unit is not opted in to boot verbs",
	"boot_ops_lingering":    "a process of the unit outlived start",
	"sash_grant_host":       "the grant is not for this host",
	"host_decl_invalid":     "the host declarations are invalid",
	"host_decl_perm":        "the host declarations file failed the file rules",
	"denied":                "the init refused it",
	"busy":                  "a load of that module is still in the kernel",
	"not_found":             "the init has no declaration or unit of that name",
	"timeout":               "the verb did not finish in time",
}

// bootDoFailed is doFailed for an init refusal of a boot verb.
func (m *machine) bootDoFailed(key string, err error) (int, bool) {
	if !bootOpsKey(key) {
		return 0, false
	}
	why, ok := bootRefusals[err.Error()]
	if !ok {
		return 0, false
	}
	m.err.WriteString(fmt.Sprintf("refused %s: %s (%s)\n", key, err.Error(), why))
	return exitType, true
}

// bootStage is needs for a boot verb: the key and its literal name.
func (n *needsRun) bootStage(st Stage) {
	argv := make([]string, len(st.Argv))
	for i, w := range st.Argv {
		argv[i] = plain(w)
	}
	f := bootFormOf(argv)
	if f == nil || len(st.Argv) < 3 {
		return
	}
	n.value(f.key, st.Argv[2], false)
}

// bootOutsideStart is boot-outside-start: a boot verb reachable from a
// service file's stop, reload or check, which the run refuses (boot_entry).
func (l *linter) bootOutsideStart() {
	if serviceOf(l.prog) == nil {
		return
	}
	for i, s := range l.prog.Stmts {
		fn, ok := s.(*Fn)
		if !ok || (fn.Name != "stop" && fn.Name != "reload" && fn.Name != "check") {
			continue
		}
		if what := l.bootReach(&fn.Body, map[*Fn]bool{fn: true}); what != "" {
			l.add(l.prog.Pos[i], "boot-outside-start", "fn "+fn.Name+" reaches "+what+"; boot verbs run only in fn start")
		}
	}
}

// bootReach names the first boot verb p reaches, through fns called by
// literal name, each once.
func (l *linter) bootReach(p *Program, seen map[*Fn]bool) string {
	what := ""
	needsEachStage(p, func(st Stage) {
		if what != "" || len(st.Argv) == 0 {
			return
		}
		if k := lintKey(st); bootOpsKey(k) {
			what = k
			return
		}
		if fn := l.fns[plain(st.Argv[0])]; fn != nil && !seen[fn] {
			seen[fn] = true
			if inner := l.bootReach(&fn.Body, seen); inner != "" {
				what = inner + " through " + needsCell(fn.Name)
			}
		}
	})
	return what
}
