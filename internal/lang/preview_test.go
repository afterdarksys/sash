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

// faceAt is the face drawn at byte i: later spans win, as in Paint.
func faceAt(spans []Span, i int) Face {
	f := FacePlain
	for _, sp := range spans {
		if i >= sp.Start && i < sp.End {
			f = sp.Face
		}
	}
	return f
}

// wordFace is the face of every byte of word in src, or -1 when the
// bytes disagree.
func wordFace(spans []Span, src, word string) Face {
	at := strings.Index(src, word)
	f := faceAt(spans, at)
	for i := at; i < at+len(word); i++ {
		if faceAt(spans, i) != f {
			return -1
		}
	}
	return f
}

func TestPreviewWriteOutsideRoot(t *testing.T) {
	g := grant.Grant{Entitlements: []string{"file.write"}, FileWrite: []string{"/tmp"}}
	src := "echo hi > /etc/passwd"
	got := Preview(src, g, Options{})
	if !got.Denied || !got.Block || got.Smile != "denied file.write" {
		t.Fatalf("denied %v block %v smile %q", got.Denied, got.Block, got.Smile)
	}
	if f := wordFace(got.Spans, src, "/etc/passwd"); f != FaceOutOfRoot {
		t.Fatalf("target face %d spans %+v", f, got.Spans)
	}
}

// v5: an out-of-root path marks only the word refused; the rest of the
// line keeps its face. A missing entitlement still dims its spans.
func TestPreviewOutOfRootMarksOneWord(t *testing.T) {
	g := grant.Grant{
		Entitlements: []string{"file.read", "file.write", "config.read"},
		FileRead:     []string{"/var/log"},
		FileWrite:    []string{"/tmp"},
	}
	cases := []struct {
		src, word, smile string
		opt              Options
	}{
		{"cat /var/log/a /etc/shadow", "/etc/shadow", "denied file.read", Options{}},
		{"ls /var/log; cat /etc/x", "/etc/x", "denied file.read", Options{}},
		{"egrep a < /etc/shadow", "/etc/shadow", "denied file.read", Options{}},
		{"echo hi > /etc/passwd", "/etc/passwd", "denied file.write", Options{}},
		{"cd /etc", "/etc", "denied file.read", Options{}},
		{"template /etc/t", "/etc/t", "denied file.read", Options{}},
		{"config read /etc/ssh/sshd_config", "/etc/ssh/sshd_config", "denied file.read", Options{}},
		{"template_file /var/log/v.yaml /var/log/t /etc/out", "/etc/out", "denied file.write", Options{}},
		{"template_file /var/log/v.yaml /etc/t /tmp/out", "/etc/t", "denied file.read", Options{}},
		{"ls", "ls", "denied file.read", Options{Prefix: "/etc"}},
	}
	for _, c := range cases {
		got := Preview(c.src, g, c.opt)
		if !got.Denied || !got.Block || got.Smile != c.smile {
			t.Errorf("%q: denied %v block %v smile %q", c.src, got.Denied, got.Block, got.Smile)
			continue
		}
		if f := wordFace(got.Spans, c.src, c.word); f != FaceOutOfRoot {
			t.Errorf("%q: %q face %d spans %+v", c.src, c.word, f, got.Spans)
		}
		for i := range c.src {
			at := strings.Index(c.src, c.word)
			if (i < at || i >= at+len(c.word)) && (faceAt(got.Spans, i) == FaceOutOfRoot || faceAt(got.Spans, i) == FaceDenied) {
				t.Errorf("%q: byte %d (%q) marked %d", c.src, i, c.src[i], faceAt(got.Spans, i))
			}
		}
		out := Run(c.src, g, &countParent{}, c.opt)
		if out.Status != exitDenied || out.Stderr != c.smile+"\n" {
			t.Errorf("%q: run %+v", c.src, out)
		}
	}
	got := Preview("cat /var/log/a", grant.Grant{}, Options{})
	if !got.Block || wordFace(got.Spans, "cat /var/log/a", "cat /var/log/a") != FaceDenied {
		t.Fatalf("no entitlement %+v", got)
	}
}

// v5: a command name from a value is FaceFromValue on that word and the
// line is Block, as the run refuses it. A subcommand from a value is
// FaceDynamic, and the run decides.
func TestPreviewFromValueAndDynamic(t *testing.T) {
	g := grant.Grant{Entitlements: []string{"ps", "unit.status"}}
	for src, word := range map[string]string{
		"$cmd x":                    "$cmd",
		"echo a; $c":                "$c",
		"ps | $f":                   "$f",
		"[ \"ps\" ] a":              "[ \"ps\" ]",
		"fn f() { $g }":             "$g",
		"if [ true ] { \"$x\" }":    "\"$x\"",
		"try { $y } catch { echo }": "$y",
	} {
		got := Preview(src, g, Options{})
		if !got.Block || got.Denied || got.Smile != "command_from_value" {
			t.Errorf("%q: block %v denied %v smile %q", src, got.Block, got.Denied, got.Smile)
		}
		if f := wordFace(got.Spans, src, word); f != FaceFromValue {
			t.Errorf("%q: %q face %d spans %+v", src, word, f, got.Spans)
		}
	}
	for src, word := range map[string]string{
		"unit $op nginx":         "$op",
		"package $p vim":         "$p",
		"sysctl $s k":            "$s",
		"config $how /etc/hosts": "$how",
	} {
		got := Preview(src, g, Options{})
		if got.Block || got.Denied {
			t.Errorf("%q: block %v denied %v smile %q", src, got.Block, got.Denied, got.Smile)
		}
		if f := wordFace(got.Spans, src, word); f != FaceDynamic {
			t.Errorf("%q: %q face %d spans %+v", src, word, f, got.Spans)
		}
		name := strings.Fields(src)[0]
		if f := wordFace(got.Spans, src, name); f == FaceFilter {
			t.Errorf("%q: verb painted as a filter", src)
		}
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
