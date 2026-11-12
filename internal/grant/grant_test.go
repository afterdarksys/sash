package grant

import "testing"

func TestHasRejectsStar(t *testing.T) {
	g := Grant{Entitlements: []string{"ps", "*"}}
	if g.Has("ps") {
		t.Fatal("star grant authorized ps")
	}
	if g.Has("*") {
		t.Fatal("star grant authorized *")
	}
}

func TestHasListedKey(t *testing.T) {
	g := Grant{Entitlements: []string{"ps", "unit.status"}}
	if !g.Has("ps") || !g.Has("unit.status") {
		t.Fatal("listed key denied")
	}
	if g.Has("unit.restart") {
		t.Fatal("unlisted key allowed")
	}
}

func TestPathRoots(t *testing.T) {
	g := Grant{FileRead: []string{"/var/log"}, FileWrite: []string{"/var/tmp/ops"}}
	if !g.AllowsRead("/var/log") || !g.AllowsRead("/var/log/syslog") {
		t.Fatal("read root rejected a path inside it")
	}
	if g.AllowsRead("/var/logbook") || g.AllowsRead("/etc/passwd") || g.AllowsRead("syslog") {
		t.Fatal("read root accepted a path outside it")
	}
	if g.AllowsRead("/var/log/../../etc/passwd") {
		t.Fatal("read root accepted a cleaned escape")
	}
	if !g.AllowsWrite("/var/tmp/ops/out") {
		t.Fatal("write root rejected a path inside it")
	}
	if g.AllowsWrite("/var/tmp/other") {
		t.Fatal("write root accepted a sibling")
	}
}
