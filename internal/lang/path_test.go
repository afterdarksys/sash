package lang

import (
	"strings"
	"testing"
)

func TestMakePath(t *testing.T) {
	for _, c := range []struct{ prefix, in, want, err string }{
		{"", "/etc/nginx", "/etc/nginx", ""},
		{"", "/", "/", ""},
		{"/srv", "x", "/srv/x", ""},
		{"/srv", "./x/y", "/srv/x/y", ""},
		{"", "x", "", "prefix_unset"},
		{"", "/etc/../root", "", "path: not clean"},
		{"", "/etc/", "", "path: not clean"},
		{"", "//etc", "", "path: not clean"},
		{"", "/etc/./x", "", "path: not clean"},
		{"/srv", "../etc", "", "path: not clean"},
		{"/srv", "..", "", "path: not clean"},
		{"/srv", ".", "", "path: not clean"},
		{"/srv", "a/../../etc", "", "path: not clean"},
		{"", "", "", "path: empty"},
		{"", "/etc/a\x00b", "", "path: control character"},
		{"", "/etc/a\nb", "", "path: control character"},
		{"", "/" + strings.Repeat("a", pathMax), "", "path: too long"},
	} {
		got, err := makePath(c.prefix, c.in)
		if c.err != "" {
			if err == nil || err.Error() != c.err {
				t.Errorf("makePath(%q, %q) = %q, %v; want %s", c.prefix, c.in, got, err, c.err)
			}
			continue
		}
		if err != nil || string(got) != c.want {
			t.Errorf("makePath(%q, %q) = %q, %v; want %q", c.prefix, c.in, got, err, c.want)
		}
	}
}

func runPath(t *testing.T, src string) Outcome {
	t.Helper()
	return Run(src, allow(), &fake{}, Options{})
}

func TestPathValues(t *testing.T) {
	for _, c := range []struct{ src, out string }{
		{"let p = path /etc/nginx/nginx.conf\necho $p $p.name $p.ext $p.dir", "/etc/nginx/nginx.conf nginx.conf .conf /etc/nginx\n"},
		{"let p = path /etc/nginx\necho [ $p.parts ] [ type($p) ]", "[\"etc\",\"nginx\"] path\n"},
		{"let p = [ path(\"/etc\") / \"nginx\" / \"conf.d\" ]\necho $p", "/etc/nginx/conf.d\n"},
		{"let p = [ path(\"/\") / \"etc\" ]\necho $p [ path(\"/\").parts ]", "/etc []\n"},
		{"declare -P p = /etc\necho [ $p == path(\"/etc\") ] [ $p == \"/etc\" ] [ str($p) == \"/etc\" ]", "true false true\n"},
		{"let p = path /etc/nginx2\necho [ starts($p, path(\"/etc/nginx\")) ] [ starts($p, path(\"/etc\")) ] [ starts($p, path(\"/\")) ]", "false true true\n"},
		{"let p = path /etc\necho [ {\"p\": $p} ]", "{\"p\":\"/etc\"}\n"},
		{"cd /etc\nlet p = path nginx\necho $p", "/etc/nginx\n"},
	} {
		out := runPath(t, c.src)
		if out.Status != 0 || out.Stdout != c.out {
			t.Errorf("%q: %+v, want %q", c.src, out, c.out)
		}
	}
}

func TestPathRefusals(t *testing.T) {
	for _, c := range []struct{ src, err string }{
		{"let p = path /etc/../root", "path: not clean"},
		{"let p = path nginx", "prefix_unset"},
		{"let p = path /etc/*", "a glob is not a path"},
		{"declare -P p = /etc/x*", "a glob is not a path"},
		{"declare -rP p = /etc/h*", "a glob is not a path"},
		{"let p = path /etc\nlet q = [ $p / \"..\" ]", "path: join component"},
		{"let p = path /etc\nlet q = [ $p / \"a/b\" ]", "path: join component"},
		{"let p = path /etc\nlet q = [ $p / \"\" ]", "path: join component"},
		{"let p = path /etc\nlet q = [ $p / 3 ]", "path: join component"},
		{"let q = [ \"/etc\" / \"x\" ]", "type expr"},
		{"declare -P p = /etc\nlet p = /root", "type p path"},
		{"let p = path /etc\necho [ starts($p, \"/etc\") ]", "type starts(path, string)"},
		{"let p = path /etc\necho [ $p.size ]", "type expr path field size"},
		{"let p = [ path(3) ]", "type path(number)"},
	} {
		out := runPath(t, c.src)
		if out.Status != exitType || !strings.Contains(out.Stderr, c.err) {
			t.Errorf("%q: %+v, want exit 2 %q", c.src, out, c.err)
		}
	}
}

// In a script, text from host data is not a file path; a path made from
// it (path(...) or a join) is, and the grant still decides.
func TestScriptFileVerbRefusesHostText(t *testing.T) {
	for _, src := range []string{
		"for r in unit status { cat $r.name }",
		"for r in unit status { cat /etc/$r.name }",
		"for r in unit status { ls $r.name }",
		"for r in unit status { echo x > $r.name }",
		"let rows = (unit status)\ncat $rows[0].name",
	} {
		f := &fake{}
		out := Run(src, allow(), f, Options{})
		if out.Status != exitType || !strings.Contains(out.Stderr, "type path") {
			t.Errorf("%q: %+v, want exit 2 type path", src, out)
		}
		for _, c := range f.calls {
			if c.Key == "file.read" || c.Key == "file.write" {
				t.Errorf("%q: called %s", src, c.Key)
			}
		}
	}
	for _, src := range []string{
		"let etc = path /etc\nfor r in unit status { cat [ $etc / $r.name ] }",
		"for r in unit status { cat [ path(\"/etc/\" + $r.name) ] }",
		"cat /etc/hosts",
		"let n = hosts\ncat /etc/$n",
		"let p = path /etc/hosts\ncat $p",
	} {
		out := Run(src, allow(), &fake{}, Options{})
		if out.Status != 0 {
			t.Errorf("%q: %+v", src, out)
		}
	}
	// A path built from host text is still checked: no climbing, and the
	// grant's roots still decide.
	out := Run("for r in ps { cat [ path($r.cmd) ] }", allow(), &fake{}, Options{})
	if out.Status != exitDenied {
		t.Errorf("outside the roots: %+v", out)
	}
	// At the prompt, words keep working.
	out = Run("for r in unit status { cat /etc/$r.name }", allow(), &fake{}, Options{State: NewState()})
	if out.Status != 0 {
		t.Errorf("prompt: %+v", out)
	}
}

func TestSecretPathNeverLeaves(t *testing.T) {
	out := Run("declare -s s = /etc/hosts\nlet p = [ path($s) ]\ncat $p", allow(), &fake{}, Options{})
	if out.Status != exitType || !strings.Contains(out.Stderr, "secret") {
		t.Errorf("%+v", out)
	}
	out = Run("declare -s s = /etc/hosts\nlet p = path $s", allow(), &fake{}, Options{})
	if out.Status != exitType || !strings.Contains(out.Stderr, "secret") {
		t.Errorf("%+v", out)
	}
}
