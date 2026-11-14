package lang

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/parent"
)

// secretTplFiles: a spec that a script binds as a secret, a template that
// renders it, and an existing out file.
func secretTplFiles() map[string]string {
	return map[string]string{
		"/srv/ops/sec.yaml":     "pw: hunter2-long\n",
		"/srv/ops/plain.yaml":   "pw: public\n",
		"/srv/ops/t.tpl":        "password={{ $pw }}\n",
		"/srv/ops/out/app.conf": "password=old\n",
	}
}

const bindSecret = "declare -s s = (cat /srv/ops/sec.yaml)\n"

func writesTo(p *tplParent, path string) []parent.Verb {
	var out []parent.Verb
	for _, c := range p.calls {
		if c.Key == "file.write" && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

// A render that read a secret is written with mode 0600; one that did not
// carries no mode. Neither prints the secret.
func TestTemplateFileSecretMode(t *testing.T) {
	p := &tplParent{files: secretTplFiles()}
	out := Run(bindSecret+"template_file /srv/ops/sec.yaml /srv/ops/t.tpl /srv/ops/out/app.conf", tplGrant(), p, Options{})
	w := writesTo(p, "/srv/ops/out/app.conf")
	if out.Status != 0 || len(w) != 1 || w[0].Mode != "0600" || string(w[0].Body) != "password=hunter2-long\n" {
		t.Fatalf("secret render: %+v %+v", out, w)
	}
	if strings.Contains(out.Stdout+out.Stderr, "hunter2") {
		t.Fatalf("printed the secret: %+v", out)
	}
	// The template holding a bound secret's text counts too.
	p = &tplParent{files: secretTplFiles()}
	p.files["/srv/ops/t2.tpl"] = "x={{ $pw }}\npw: hunter2-long\n"
	out = Run(bindSecret+"template_file /srv/ops/plain.yaml /srv/ops/t2.tpl /srv/ops/out/app.conf", tplGrant(), p, Options{})
	if w := writesTo(p, "/srv/ops/out/app.conf"); out.Status != 0 || len(w) != 1 || w[0].Mode != "0600" {
		t.Fatalf("secret tpl: %+v %+v", out, w)
	}
	// No secret: no mode, so an older agent still takes the frame.
	p = &tplParent{files: secretTplFiles()}
	out = Run(bindSecret+"template_file /srv/ops/plain.yaml /srv/ops/t.tpl /srv/ops/out/app.conf\necho hi > /srv/ops/out/x", tplGrant(), p, Options{})
	if w := writesTo(p, "/srv/ops/out/app.conf"); out.Status != 0 || len(w) != 1 || w[0].Mode != "" || string(w[0].Body) != "password=public\n" {
		t.Fatalf("plain render: %+v %+v", out, w)
	}
	if w := writesTo(p, "/srv/ops/out/x"); len(w) != 1 || w[0].Mode != "" {
		t.Fatalf("> after: %+v", w)
	}
	// The flag does not outlive its write: a > after a secret render has
	// no mode.
	p = &tplParent{files: secretTplFiles()}
	out = Run(bindSecret+"template_file /srv/ops/sec.yaml /srv/ops/t.tpl /srv/ops/out/app.conf\necho hi > /srv/ops/out/x", tplGrant(), p, Options{})
	if w := writesTo(p, "/srv/ops/out/x"); out.Status != 0 || len(w) != 1 || w[0].Mode != "" {
		t.Fatalf("> after a secret render: %+v %+v", out, w)
	}
}

// Everything else about a secret stays: not through >, echo, a path, or
// the value a template_file leaves behind.
func TestTemplateFileSecretStillRefused(t *testing.T) {
	for name, src := range map[string]string{
		"redirect":       bindSecret + "echo $s > /srv/ops/out/x",
		"spec path":      bindSecret + "template_file /srv/ops/$s.yaml /srv/ops/t.tpl /srv/ops/out/x",
		"out path":       bindSecret + "template_file /srv/ops/sec.yaml /srv/ops/t.tpl /srv/ops/out/x$s",
		"render value >": bindSecret + "let r = (template_file /srv/ops/sec.yaml /srv/ops/t.tpl /srv/ops/out/app.conf)\necho $r > /srv/ops/out/x",
		"verb argument":  bindSecret + "unit restart $s",
	} {
		p := &tplParent{files: secretTplFiles()}
		out := Run(src, tplGrant(), p, Options{})
		if out.Status != exitType || !(strings.Contains(out.Stderr, "a secret cannot leave sash") || out.Stderr == "type value\n") || len(writesTo(p, "/srv/ops/out/x")) != 0 || strings.Contains(out.Stdout+out.Stderr, "hunter2") {
			t.Errorf("%s: %+v %+v", name, out, p.calls)
		}
	}
	for name, src := range map[string]string{
		"echo":         bindSecret + "echo $s",
		"render value": bindSecret + "let r = (template_file /srv/ops/sec.yaml /srv/ops/t.tpl /srv/ops/out/app.conf)\necho $r",
		"log":          bindSecret + "log info $s",
	} {
		p := &tplParent{files: secretTplFiles()}
		out := Run(src, tplGrant(), p, Options{})
		if strings.Contains(out.Stdout+out.Stderr, "hunter2") {
			t.Errorf("%s printed the secret: %+v", name, out)
		}
	}
}

// --check and plan mode never list a secret, from the render or the file.
func TestTemplateFileSecretDiff(t *testing.T) {
	p := &tplParent{files: secretTplFiles()}
	out := Run(bindSecret+"template_file /srv/ops/sec.yaml /srv/ops/t.tpl /srv/ops/out/app.conf --check", tplGrant(), p, Options{})
	if out.Status != 1 || out.Stdout != secretDiff || strings.Contains(out.Stdout+out.Stderr, "hunter2") || len(writesTo(p, "/srv/ops/out/app.conf")) != 0 {
		t.Fatalf("--check: %+v", out)
	}
	// The current file is the secret: its lines are not listed either.
	p = &tplParent{files: secretTplFiles()}
	p.files["/srv/ops/out/app.conf"] = "password=hunter2-long\n"
	out = Run("declare -s s = (cat /srv/ops/out/app.conf)\ntemplate_file /srv/ops/plain.yaml /srv/ops/t.tpl /srv/ops/out/app.conf --check", tplGrant(), p, Options{})
	if out.Status != 1 || out.Stdout != secretDiff {
		t.Fatalf("--check secret file: %+v", out)
	}
	// Same content: no diff and exit 0, secret or not.
	p = &tplParent{files: secretTplFiles()}
	p.files["/srv/ops/out/app.conf"] = "password=hunter2-long\n"
	out = Run(bindSecret+"template_file /srv/ops/sec.yaml /srv/ops/t.tpl /srv/ops/out/app.conf --check", tplGrant(), p, Options{})
	if out.Status != 0 || out.Stdout != "" {
		t.Fatalf("--check same: %+v", out)
	}
	p = &tplParent{files: secretTplFiles()}
	st := NewState()
	st.LockPlan()
	out = Run(bindSecret+"template_file /srv/ops/sec.yaml /srv/ops/t.tpl /srv/ops/out/app.conf", tplGrant(), p, Options{State: st})
	if out.Status != 0 || len(writesTo(p, "/srv/ops/out/app.conf")) != 0 || strings.Contains(out.Stdout+out.Stderr, "hunter2") ||
		!strings.Contains(out.Stderr, secretDiff) || !strings.Contains(out.Stderr, "plan: would template_file /srv/ops/out/app.conf (22 bytes) (mode 0600)") {
		t.Fatalf("plan: %+v", out)
	}
}

// An error after a secret read does not quote it, and ends the line.
func TestTemplateFileSecretFault(t *testing.T) {
	p := &tplParent{files: secretTplFiles()}
	p.files["/srv/ops/sec.yaml"] = "pw: [hunter2-long\n"
	out := Run("declare -s s = (cat /srv/ops/sec.yaml)\ntry { template_file /srv/ops/sec.yaml /srv/ops/t.tpl /srv/ops/out/app.conf } catch e { echo caught }\necho after", tplGrant(), p, Options{})
	if out.Status != exitType || strings.Contains(out.Stdout+out.Stderr, "hunter2") || strings.Contains(out.Stdout, "caught") || strings.Contains(out.Stdout, "after") ||
		!strings.Contains(out.Stderr, errSecretFault.Error()) {
		t.Fatalf("%+v", out)
	}
}

type modeParent struct {
	tplParent
	err error
}

func (p *modeParent) Do(v parent.Verb) (parent.Result, error) {
	if v.Key == "file.write" {
		p.calls = append(p.calls, v)
		return parent.Result{}, p.err
	}
	return p.tplParent.Do(v)
}

// An agent too old for the mode is a clear exit 2, never a retry without
// it; bad_mode and owner_changed are clear errors try catches with key
// file.write.
func TestTemplateFileModeErrors(t *testing.T) {
	p := &modeParent{tplParent: tplParent{files: secretTplFiles()}, err: parent.ErrModeUnsupported}
	out := Run(bindSecret+"template_file /srv/ops/sec.yaml /srv/ops/t.tpl /srv/ops/out/app.conf\necho after", tplGrant(), p, Options{})
	w := writesTo(&p.tplParent, "/srv/ops/out/app.conf")
	if out.Status != exitType || !strings.Contains(out.Stderr, "agent too old for file.write mode") || strings.Contains(out.Stdout, "after") || len(w) != 1 || w[0].Mode != "0600" {
		t.Fatalf("old agent: %+v %+v", out, w)
	}
	for _, code := range []string{"owner_changed", "bad_mode"} {
		p := &modeParent{tplParent: tplParent{files: secretTplFiles()}, err: errors.New(code)}
		out := Run(bindSecret+"template_file /srv/ops/sec.yaml /srv/ops/t.tpl /srv/ops/out/app.conf", tplGrant(), p, Options{})
		if out.Status != exitType || !strings.Contains(out.Stderr, "refused file.write: "+code+" (") {
			t.Errorf("%s: %+v", code, out)
		}
		out = Run(bindSecret+"try { template_file /srv/ops/sec.yaml /srv/ops/t.tpl /srv/ops/out/app.conf } catch e { echo $e.key $e.status }\necho after", tplGrant(), p, Options{})
		if out.Status != 0 || out.Stdout != "file.write 2\nafter\n" {
			t.Errorf("%s caught: %+v", code, out)
		}
	}
}

// A recording keeps the mode in the request and replay matches on it; the
// body of a moded write is redacted, never hashed.
func TestTemplateFileModeReplay(t *testing.T) {
	v := parent.Verb{Key: "file.write", Argv: []string{">", "/srv/ops/out/app.conf"}, Path: "/srv/ops/out/app.conf", Body: []byte("password=x\n"), Mode: "0600"}
	for _, tainted := range []bool{false, true} {
		rec := reqOf(v, tainted, nil)
		if rec.Mode != "0600" || !reqMatches(rec, v) {
			t.Fatalf("tainted %v: %+v", tainted, rec)
		}
		plain := v
		plain.Mode = ""
		if reqMatches(rec, plain) {
			t.Fatalf("tainted %v: matched without the mode", tainted)
		}
		if reqMatches(reqOf(plain, tainted, nil), v) {
			t.Fatalf("tainted %v: matched with a mode not recorded", tainted)
		}
	}
	p := &tplParent{files: secretTplFiles()}
	var buf strings.Builder
	tape, err := NewRecorder(&buf, "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	out := Run(bindSecret+"template_file /srv/ops/sec.yaml /srv/ops/t.tpl /srv/ops/out/app.conf", tplGrant(), p, Options{Tape: tape})
	if err := tape.Finish(); err != nil || out.Status != 0 {
		t.Fatalf("%v %+v", err, out)
	}
	if !strings.Contains(buf.String(), `"mode":"0600"`) || strings.Contains(buf.String(), "hunter2") || strings.Contains(buf.String(), sha([]byte("password=hunter2-long\n"))) {
		t.Fatalf("recording: %s", buf.String())
	}
}

// Lint: a secret render is information (secret-file), not a sink; a
// secret in a template_file path is still secret-to-sink.
func TestLintSecretFile(t *testing.T) {
	rows := lintRows(t, Lint(bindSecret+"template_file /srv/ops/sec.yaml /srv/ops/t.tpl /srv/ops/out/app.conf"))
	if !hasRule(rows, "secret-file", "info") || hasRule(rows, "secret-to-sink", "") {
		t.Fatalf("%+v", rows)
	}
	if rows := lintRows(t, Lint(bindSecret+"template_file /srv/ops/plain.yaml /srv/ops/t.tpl /srv/ops/out/app.conf")); hasRule(rows, "secret-file", "") {
		t.Fatalf("plain: %+v", rows)
	}
	if rows := lintRows(t, Lint(bindSecret+"template_file $s /srv/ops/t.tpl /srv/ops/out/app.conf")); !hasRule(rows, "secret-to-sink", "error") {
		t.Fatalf("path: %+v", rows)
	}
}

// why says a secret render is written with mode 0600.
func TestWhyTemplateFileMode(t *testing.T) {
	out := Why("template_file /srv/ops/sec.yaml /srv/ops/t.tpl /srv/ops/out/app.conf", tplGrant(), Options{})
	if out.Status != 0 || !strings.Contains(out.Stdout, "written with mode 0600") {
		t.Fatalf("%+v", out)
	}
}

const bindTok = "declare -s s = (cat /srv/ops/tok)\n"

func builtFiles() map[string]string {
	f := secretTplFiles()
	f["/srv/ops/tok"] = "hunter2-long"
	f["/srv/ops/split.yaml"] = "a: hunter2\nb: -long\n"
	f["/srv/ops/split.tpl"] = "pw={{ $a }}{{ $b }}\n"
	f["/srv/ops/esc.yaml"] = "pw: \"hunter2\\x2dlong\"\n"
	f["/srv/ops/b64.yaml"] = "pw: aHVudGVyMi1sb25n\n"
	return f
}

// A secret's text built during the render (split across spec values, or
// spelled with an escape the spec decodes) is written with mode 0600, and
// the spec and template origins become secret.
func TestTemplateFileSecretBuilt(t *testing.T) {
	for name, c := range map[string]struct{ spec, tpl, body string }{
		"split":  {"/srv/ops/split.yaml", "/srv/ops/split.tpl", "pw=hunter2-long\n"},
		"escape": {"/srv/ops/esc.yaml", "/srv/ops/t.tpl", "password=hunter2-long\n"},
	} {
		p := &tplParent{files: builtFiles()}
		out := Run(bindTok+"template_file "+c.spec+" "+c.tpl+" /srv/ops/out/app.conf", tplGrant(), p, Options{})
		w := writesTo(p, "/srv/ops/out/app.conf")
		if out.Status != 0 || len(w) != 1 || w[0].Mode != "0600" || string(w[0].Body) != c.body || strings.Contains(out.Stdout+out.Stderr, "hunter2") {
			t.Errorf("%s: %+v %+v", name, out, w)
		}
		p = &tplParent{files: builtFiles()}
		out = Run(bindTok+"template_file "+c.spec+" "+c.tpl+" /srv/ops/out/app.conf --check", tplGrant(), p, Options{})
		if out.Status != 1 || out.Stdout != secretDiff {
			t.Errorf("%s --check: %+v", name, out)
		}
	}
	// The spec's origin is now secret: its hash prints ***.
	p := &tplParent{files: builtFiles()}
	st := NewState()
	if out := Run(bindTok+"template_file /srv/ops/split.yaml /srv/ops/split.tpl /srv/ops/out/app.conf", tplGrant(), p, Options{State: st}); out.Status != 0 {
		t.Fatalf("%+v", out)
	}
	hidden := 0
	for id, o := range st.origins {
		if o.target == "/srv/ops/split.yaml" || o.target == "/srv/ops/split.tpl" {
			if !st.hideHash(uint32(id)) {
				t.Errorf("origin %s not secret", o.target)
			}
			hidden++
		}
	}
	if hidden != 2 {
		t.Fatalf("origins %+v", st.origins)
	}
}

// Known limit, as for recordings: an encoding of a secret (here its
// base64) is not its text, so that render keeps the default mode.
func TestTemplateFileSecretEncodingLimit(t *testing.T) {
	p := &tplParent{files: builtFiles()}
	out := Run(bindTok+"template_file /srv/ops/b64.yaml /srv/ops/t.tpl /srv/ops/out/app.conf", tplGrant(), p, Options{})
	if w := writesTo(p, "/srv/ops/out/app.conf"); out.Status != 0 || len(w) != 1 || w[0].Mode != "" || string(w[0].Body) != "password=aHVudGVyMi1sb25n\n" {
		t.Fatalf("%+v %+v", out, w)
	}
}

// Lint warns when a secret is read from a file an earlier template_file
// wrote: the run could not know, so it went out at the default mode.
func TestLintSecretAfterWrite(t *testing.T) {
	src := "template_file /srv/ops/plain.yaml /srv/ops/t.tpl /srv/ops/out/app.conf\ndeclare -s s = (cat /srv/ops/out/app.conf)"
	if rows := lintRows(t, Lint(src)); !hasRule(rows, "secret-after-write", "warning") {
		t.Fatalf("%+v", rows)
	}
	for name, src := range map[string]string{
		"read first": "declare -s s = (cat /srv/ops/out/app.conf)\ntemplate_file /srv/ops/plain.yaml /srv/ops/t.tpl /srv/ops/out/app.conf",
		"not secret": "template_file /srv/ops/plain.yaml /srv/ops/t.tpl /srv/ops/out/app.conf\nlet s = (cat /srv/ops/out/app.conf)",
		"--check":    "template_file /srv/ops/plain.yaml /srv/ops/t.tpl /srv/ops/out/app.conf --check\ndeclare -s s = (cat /srv/ops/out/app.conf)",
	} {
		if rows := lintRows(t, Lint(src)); hasRule(rows, "secret-after-write", "") {
			t.Errorf("%s: %+v", name, rows)
		}
	}
}
