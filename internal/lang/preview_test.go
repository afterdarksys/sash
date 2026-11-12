package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

type countParent struct {
	n int
}

func (c *countParent) Do(parent.Verb) (parent.Result, error) {
	c.n++
	return parent.Result{Kind: parent.KindBytes}, nil
}

func TestPreviewDeniedAgreesWithRun(t *testing.T) {
	cases := []struct {
		src string
		key string
	}{
		{"unit restart nginx", "unit.restart"},
		{"package install vim", "package.install"},
		{"sysctl set net.ipv4.ip_forward 1", "sysctl.set"},
		{"journal", "journal"},
	}
	for _, tc := range cases {
		got := Preview(tc.src, grant.Grant{}, Options{})
		if !got.Denied || !got.Block || got.Smile != "denied "+tc.key {
			t.Fatalf("%s denied %v block %v smile %q", tc.src, got.Denied, got.Block, got.Smile)
		}
		c := &countParent{}
		out := Run(tc.src, grant.Grant{}, c, Options{})
		if out.Status != exitDenied || out.Stderr != got.Smile+"\n" || c.n != 0 {
			t.Fatalf("%s status %d stderr %q calls %d", tc.src, out.Status, out.Stderr, c.n)
		}
	}
}

func TestPreviewStarGrantDeniesPS(t *testing.T) {
	g := grant.Grant{Entitlements: []string{"*", "ps"}}
	got := Preview("ps", g, Options{})
	if !got.Denied || !got.Block || got.Smile != "denied ps" {
		t.Fatalf("denied %v block %v smile %q", got.Denied, got.Block, got.Smile)
	}
}

func TestPreviewWriteOutsideRoot(t *testing.T) {
	g := grant.Grant{Entitlements: []string{"file.write"}, FileWrite: []string{"/tmp"}}
	src := "echo hi > /etc/passwd"
	got := Preview(src, g, Options{})
	if !got.Denied || !got.Block || got.Smile != "denied file.write" {
		t.Fatalf("denied %v block %v smile %q", got.Denied, got.Block, got.Smile)
	}
	found := false
	for _, sp := range got.Spans {
		if sp.Key == "file.write" && sp.Face == FaceDenied && sp.Start >= 0 && sp.End <= len(src) && strings.Contains(src[sp.Start:sp.End], ">") {
			found = true
		}
	}
	if !found {
		t.Fatalf("spans %+v", got.Spans)
	}
}

func TestPreviewTypePipeSpan(t *testing.T) {
	src := "echo hi |> where a == b"
	got := Preview(src, grant.Grant{}, Options{})
	if got.Block || got.Smile != "type |>" {
		t.Fatalf("block %v smile %q denied %v", got.Block, got.Smile, got.Denied)
	}
	found := false
	for _, sp := range got.Spans {
		if sp.Start >= 0 && sp.End <= len(src) && src[sp.Start:sp.End] == "|>" && sp.Face == FaceTypePipe {
			found = true
		}
	}
	if !found {
		t.Fatalf("spans %+v", got.Spans)
	}
}

func TestRunBindViStaysUnknown(t *testing.T) {
	c := &countParent{}
	got := Run("bind vi", grant.Grant{}, c, Options{})
	if got.Status != exitUnknown || !strings.Contains(got.Stderr, "unknown bind") || c.n != 0 {
		t.Fatalf("status %d stderr %q calls %d", got.Status, got.Stderr, c.n)
	}
}

// The preview walks the same pipes as a run, a declare value included.
func TestPreviewDeclarePipeDenied(t *testing.T) {
	g := grant.Grant{Entitlements: []string{"*", "ps"}}
	got := Preview("declare x = (ps)", g, Options{})
	if !got.Denied || !got.Block || got.Smile != "denied ps" {
		t.Fatalf("denied %v block %v smile %q", got.Denied, got.Block, got.Smile)
	}
}
