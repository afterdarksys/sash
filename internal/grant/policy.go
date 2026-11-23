package grant

// The allowlists a boot grant's spawn environment and mounts are checked
// against. sash and the init test double share them, so the two checks
// cannot drift; the init applies the same rules.
//
// Threats: a spawn env name that makes the child load code (LD_PRELOAD,
// interpreter search paths, OPENSSL_CONF, GIT_SSH_COMMAND), and a mount
// whose type or options bring another tree in (overlay lowerdir, move,
// x-bind) or re-enable setuid binaries and device nodes. Both are
// allowlists: a name or option not listed is refused. The env names are
// listed in the signed grant; the denylist below is a second gate a
// careless signer cannot open.

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// ErrEnvRefused is a spawn env name the grant does not list, or one on
// the denylist.
var ErrEnvRefused = errors.New("env_refused")

// ErrMountOpt is a mount type or option not on the allowlist.
var ErrMountOpt = errors.New("mount_opt_refused")

// EnvName is the form of a spawn environment name.
var EnvName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// envDenied are names refused even when a grant lists them: the dynamic
// loader, libc's lookup and allocator knobs, shell startup, interpreter
// search paths and hooks, and tools that run a command from the
// environment. PATH is not here: it is allowed only when the grant lists
// it, like every other name.
var envDenied = map[string]bool{
	"GCONV_PATH": true, "LOCPATH": true, "NLSPATH": true, "HOSTALIASES": true, "RES_OPTIONS": true,
	"GLIBC_TUNABLES": true, "TZDIR": true, "BASH_ENV": true, "ENV": true, "IFS": true, "SHELLOPTS": true,
	"PYTHONPATH": true, "PYTHONHOME": true, "PYTHONSTARTUP": true, "PYTHONWARNINGS": true,
	"PERLLIB": true, "PERL5LIB": true, "PERL5OPT": true, "PERL5DB": true,
	"RUBYLIB": true, "RUBYOPT": true, "NODE_OPTIONS": true, "NODE_PATH": true,
	"JAVA_TOOL_OPTIONS": true, "_JAVA_OPTIONS": true, "JDK_JAVA_OPTIONS": true, "CLASSPATH": true,
	"LUA_PATH": true, "LUA_CPATH": true, "OPENSSL_CONF": true, "OPENSSL_ENGINES": true,
}

var envDeniedPrefix = []string{"LD_", "DYLD_", "MALLOC_", "GIT_"}

// EnvDenied reports a name on the denylist.
func EnvDenied(name string) bool {
	for _, p := range envDeniedPrefix {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return envDenied[name]
}

// AllowsEnv reports whether spawn may set name: it has the form, the
// grant lists it, and it is not on the denylist.
func (g Grant) AllowsEnv(name string) bool {
	return EnvName.MatchString(name) && exact(g.EnvNames, name) && !EnvDenied(name)
}

// MountTypes are the filesystem types mount may name. A bind is not a
// type: it is the option bind or rbind, with a path source.
var MountTypes = map[string]bool{"tmpfs": true, "proc": true, "sysfs": true, "devpts": true}

var (
	mountFlags = map[string]bool{"ro": true, "rw": true, "nosuid": true, "nodev": true, "noexec": true, "bind": true, "rbind": true, "remount": true}
	mountSize  = regexp.MustCompile(`^[0-9]{1,15}[kmg%]?$`)
	mountNum   = regexp.MustCompile(`^[0-9]{1,10}$`)
	mountMode  = regexp.MustCompile(`^[0-7]{3,4}$`)
)

// mountIDMax is the largest uid or gid: 2^32-1 is (uid_t)-1, "no id".
const mountIDMax = 1<<32 - 2

// mountValueOK bounds a size, nr_inodes, mode, uid or gid by its meaning:
// a count that parses without overflow (a percentage at most 100), a mode
// without setuid or setgid (at most 1777), an id below (uid_t)-1.
func mountValueOK(k, v string) bool {
	switch k {
	case "size", "nr_inodes":
		if !mountSize.MatchString(v) {
			return false
		}
		num, unit := v, byte(0)
		if c := v[len(v)-1]; c < '0' || c > '9' {
			num, unit = v[:len(v)-1], c
		}
		n, err := strconv.ParseUint(num, 10, 64)
		if err != nil || (unit == '%' && (k != "size" || n > 100)) {
			return false
		}
		shift := map[byte]uint{'k': 10, 'm': 20, 'g': 30}[unit]
		return n <= (1<<63-1)>>shift
	case "mode":
		n, err := strconv.ParseUint(v, 8, 32)
		return mountMode.MatchString(v) && err == nil && n <= 0o1777
	case "uid", "gid":
		n, err := strconv.ParseUint(v, 10, 64)
		return mountNum.MatchString(v) && err == nil && n <= mountIDMax
	}
	return false
}

// MountForcedOpts are added to every mount: no setuid binaries, no device
// nodes.
var MountForcedOpts = []string{"nosuid", "nodev"}

// CheckMountOpts checks a mount's source, type ("" for a bind) and
// options. A bind (bind or rbind) takes no type; any other mount names an
// allowed type, and its source is that type's name, so a grant whose
// mount_sources lists only tmpfs cannot mount proc. ro and rw together are
// refused. It returns whether the mount is a bind.
func CheckMountOpts(src, typ string, opts []string) (bool, error) {
	bind, ro, rw := false, false, false
	for _, o := range opts {
		k, v, hasVal := strings.Cut(o, "=")
		switch {
		case !hasVal && mountFlags[o]:
			bind = bind || o == "bind" || o == "rbind"
			ro, rw = ro || o == "ro", rw || o == "rw"
		case hasVal && mountValueOK(k, v):
		default:
			return false, ErrMountOpt
		}
	}
	if ro && rw || bind && typ != "" || !bind && (!MountTypes[typ] || src != typ) {
		return false, ErrMountOpt
	}
	return bind, nil
}
