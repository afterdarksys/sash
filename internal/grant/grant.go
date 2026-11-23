// Package grant is the in-memory sash/1 and sash/2 entitlement set.
// Commit 1 callers may pass a Grant they already trust.
// Verify is the signed check used when a parent delivers the document.
package grant

import (
	"path"
	"strings"
)

// Grant is one sash/1 or sash/2 document without a signature. The fields
// after AllowPrivate are sash/2 only (a boot grant, purpose service); a
// sash/1 grant leaves them empty and its signed bytes do not carry them.
type Grant struct {
	V            string
	Actor        string
	Host         string
	Purpose      string
	Exp          int64
	LeaseID      string
	Entitlements []string
	FileRead     []string
	FileWrite    []string
	AllowPrivate bool

	// Service is the one service a sash/2 grant authorizes; it must equal
	// the service block's name. Exec are the absolute programs spawn may
	// start, Caps the capabilities it may give, Mounts the mount points
	// mount and umount may touch. Serial is the signed revocation serial
	// (at least 1); the init refuses a grant below its floor.
	Service string
	Exec    []string
	Caps    []string
	Mounts  []string
	// MountSources are the sources mount may use: a path (or a path
	// beneath it) or a filesystem name such as tmpfs. User and Group are
	// the only identity spawn may run as.
	MountSources []string
	User         string
	Group        string
	// EnvNames are the environment names spawn may set (policy.go).
	EnvNames []string
	Serial   uint64
}

// Boot reports a sash/2 boot grant: the only kind that carries the
// service verbs.
func (g Grant) Boot() bool { return g.V == "sash/2" && g.Purpose == PurposeService }

// AllowsMountSource reports whether src is a listed name, or a clean path
// equal to or beneath a listed path.
func (g Grant) AllowsMountSource(src string) bool {
	if !strings.HasPrefix(src, "/") {
		return exact(g.MountSources, src)
	}
	if path.Clean(src) != src {
		return false
	}
	return under(g.MountSources, src)
}

// AllowsExec reports whether p is exactly one of the exec programs.
func (g Grant) AllowsExec(p string) bool { return exact(g.Exec, p) }

// AllowsCap reports whether capability c is listed.
func (g Grant) AllowsCap(c string) bool { return exact(g.Caps, c) }

// AllowsMount reports whether dir is exactly one of the mount points.
func (g Grant) AllowsMount(dir string) bool { return exact(g.Mounts, dir) }

func exact(list []string, s string) bool {
	if s == "" {
		return false
	}
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

// Has reports whether key is listed.
// A grant containing "*" authorizes nothing, including the listed names beside it.
func (g Grant) Has(key string) bool {
	if key == "" || key == "*" {
		return false
	}
	star := false
	found := false
	for _, e := range g.Entitlements {
		if e == "*" {
			star = true
		}
		if e == key {
			found = true
		}
	}
	return found && !star
}

// AllowsRead reports whether absolute path p is p itself or a descendant of a file_read root.
func (g Grant) AllowsRead(p string) bool { return under(g.FileRead, p) }

// AllowsWrite reports whether absolute path p is p itself or a descendant of a file_write root.
func (g Grant) AllowsWrite(p string) bool { return under(g.FileWrite, p) }

func under(roots []string, p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	c := path.Clean(p)
	for _, r := range roots {
		if !strings.HasPrefix(r, "/") {
			continue
		}
		r = path.Clean(r)
		if c == r || strings.HasPrefix(c, r+"/") {
			return true
		}
	}
	return false
}
