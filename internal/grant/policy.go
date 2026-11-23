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

// MountForcedOpts are added to every mount: no setuid binaries, no device
// nodes.
var MountForcedOpts = []string{"nosuid", "nodev"}

// CheckMountOpts checks a mount's type ("" for a bind) and options. A bind
// (bind or rbind) takes no type; any other mount names an allowed type.
// It returns whether the mount is a bind.
func CheckMountOpts(typ string, opts []string) (bool, error) {
	bind := false
	for _, o := range opts {
		k, v, hasVal := strings.Cut(o, "=")
		switch {
		case !hasVal && mountFlags[o]:
			if o == "bind" || o == "rbind" {
				bind = true
			}
		case hasVal && (k == "size" || k == "nr_inodes") && mountSize.MatchString(v):
		case hasVal && k == "mode" && mountMode.MatchString(v):
		case hasVal && (k == "uid" || k == "gid") && mountNum.MatchString(v):
		default:
			return false, ErrMountOpt
		}
	}
	if bind && typ != "" || !bind && !MountTypes[typ] {
		return false, ErrMountOpt
	}
	return bind, nil
}
