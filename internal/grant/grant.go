// Package grant is the in-memory sash/1 entitlement set.
// Commit 1 callers may pass a Grant they already trust.
// Verify is the signed check used when a parent delivers the document.
package grant

import (
	"path"
	"strings"
)

// Grant is one sash/1 document without a signature.
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
		// The root "/" (Verify accepts it: clean and absolute) covers
		// every absolute path; r+"/" would be "//" and match only "/".
		if c == r || strings.HasPrefix(c, strings.TrimSuffix(r, "/")+"/") {
			return true
		}
	}
	return false
}
