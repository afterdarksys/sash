package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

type tplParent struct {
	files  map[string]string
	writes map[string]string
	calls  []parent.Verb
}

func (p *tplParent) Do(v parent.Verb) (parent.Result, error) {
	p.calls = append(p.calls, v)
	switch {
	case v.Key == "file.read" && v.Argv[0] == "cat":
		if s, ok := p.files[v.Path]; ok {
			return parent.Result{Kind: parent.KindBytes, Bytes: []byte(s)}, nil
		}
		return parent.Result{Kind: parent.KindBytes, Exit: 1}, nil
	case v.Key == "file.write":
		if p.writes == nil {
			p.writes = map[string]string{}
		}
		p.writes[v.Path] = string(v.Body)
		return parent.Result{Kind: parent.KindBytes}, nil
	}
	return parent.Result{Kind: parent.KindBytes}, nil
}

func tplGrant() grant.Grant {
	return grant.Grant{V: "sash/1", Entitlements: []string{"file.read", "file.write"}, FileRead: []string{"/srv/ops"}, FileWrite: []string{"/srv/ops/out"}}
}

const nginxTpl = `server {
  listen {{ $port }};
{{- for h in $hosts }}
  server_name {{ $h }};
{{- end }}
{{- if $mode == "prod" }}
  access_log off;
{{- else }}
  access_log on;
{{- end }}
}
`

func TestTemplateFileRendersThroughTheWriteDoor(t *testing.T) {
	p := &tplParent{files: map[string]string{
		"/srv/ops/web.yaml":  "vars:\n  port: 8080\n  hosts: [a.example, b.example]\nschema:\n  port: {type: number, required: true}\n  hosts: {type: list}\n  mode: {type: string, enum: [prod, staging], default: prod}\n",
		"/srv/ops/nginx.tpl": nginxTpl,
	}}
	out := Run("template_file /srv/ops/web.yaml /srv/ops/nginx.tpl /srv/ops/out/web.conf", tplGrant(), p, Options{})
	want := "server {\n  listen 8080;\n  server_name a.example;\n  server_name b.example;\n  access_log off;\n}\n"
	if out.Status != 0 || p.writes["/srv/ops/out/web.conf"] != want {
		t.Fatalf("%+v\nwrote %q", out, p.writes["/srv/ops/out/web.conf"])
	}
	// --check: same file is status 0 and silent, a changed one lists lines.
	p.files["/srv/ops/out/web.conf"] = want
	p.writes = nil
	if out := Run("template_file /srv/ops/web.yaml /srv/ops/nginx.tpl /srv/ops/out/web.conf --check", tplGrant(), p, Options{}); out.Status != 0 || out.Stdout != "" || p.writes != nil {
		t.Fatalf("check same: %+v", out)
	}
	p.files["/srv/ops/out/web.conf"] = strings.Replace(want, "8080", "80", 1)
	out = Run("template_file /srv/ops/web.yaml /srv/ops/nginx.tpl /srv/ops/out/web.conf --check", tplGrant(), p, Options{})
	if out.Status != 1 || out.Stdout != "+   listen 8080;\n-   listen 80;\n" || p.writes != nil {
		t.Fatalf("check diff: %+v", out)
	}
}

func TestTemplateFileRefuses(t *testing.T) {
	files := map[string]string{
		"/srv/ops/t.tpl":       "{{ $port }}",
		"/srv/ops/nospec.json": `{"vars": {"port": "x"}, "schema": {"port": {"type": "number"}}}`,
		"/srv/ops/req.json":    `{"vars": {}, "schema": {"port": {"required": true}}}`,
		"/srv/ops/extra.json":  `{"vars": {"port": 1, "other": 2}, "schema": {"port": {}}}`,
		"/srv/ops/enum.json":   `{"vars": {"m": "dev"}, "schema": {"m": {"enum": ["prod"]}}}`,
		"/srv/ops/pat.json":    `{"vars": {"n": "Bad Name"}, "schema": {"n": {"pattern": "^[a-z]+$"}}}`,
		"/srv/ops/rule.json":   `{"vars": {}, "schema": {"n": {"shell": "x"}}}`,
		"/srv/ops/plain.json":  `{"other": 1}`,
	}
	for src, msg := range map[string]string{
		"template_file /srv/ops/nospec.json /srv/ops/t.tpl /srv/ops/out/x":     "port must be number",
		"template_file /srv/ops/req.json /srv/ops/t.tpl /srv/ops/out/x":        "port is required",
		"template_file /srv/ops/extra.json /srv/ops/t.tpl /srv/ops/out/x":      "other is not in the schema",
		"template_file /srv/ops/enum.json /srv/ops/t.tpl /srv/ops/out/x":       "not one of its enum",
		"template_file /srv/ops/pat.json /srv/ops/t.tpl /srv/ops/out/x":        "does not match its pattern",
		"template_file /srv/ops/rule.json /srv/ops/t.tpl /srv/ops/out/x":       "unknown rule shell",
		"template_file /srv/ops/plain.json /srv/ops/t.tpl /srv/ops/out/x":      "template line 1: unset port",
		"template_file /srv/ops/s.txt /srv/ops/t.tpl /srv/ops/out/x":           "spec must end",
		"template_file /srv/ops/plain.json /srv/ops/t.tpl":                     "usage template_file",
		"template_file /srv/ops/plain.json /srv/ops/t.tpl /srv/ops/out/x | wc": "type template_file",
	} {
		p := &tplParent{files: files}
		out := Run(src, tplGrant(), p, Options{})
		if out.Status != exitType || !strings.Contains(out.Stderr, msg) || p.writes != nil {
			t.Errorf("%s: %+v want %q wrote %v", src, out, msg, p.writes)
		}
	}
	// The write door: outside the write roots, or without file.write.
	p := &tplParent{files: map[string]string{"/srv/ops/v.json": `{"port": 1}`, "/srv/ops/t.tpl": "{{ $port }}"}}
	for src, g := range map[string]grant.Grant{
		"template_file /srv/ops/v.json /srv/ops/t.tpl /srv/ops/elsewhere": tplGrant(),
		"template_file /srv/ops/v.json /srv/ops/t.tpl /srv/ops/out/x":     {V: "sash/1", Entitlements: []string{"file.read"}, FileRead: []string{"/srv/ops"}},
		"template_file /etc/v.json /srv/ops/t.tpl /srv/ops/out/x":         tplGrant(),
	} {
		p.calls = nil
		out := Run(src, g, p, Options{})
		if out.Status != exitDenied || len(p.calls) != 0 || p.writes != nil {
			t.Errorf("%s: %+v calls %v", src, out, p.calls)
		}
	}
}

func TestTemplateSyntaxAndSafety(t *testing.T) {
	vars := func(json string) any {
		d, err := decodeJSON([]byte(json))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	for tpl, want := range map[string]string{
		"a {{ $x + 1 }} b":                                                  "a 3 b",
		"{{ upper($s) }}|{{ $vars.x }}":                                     "HI|2",
		"{{ for k, v in $m }}{{ $k }}={{ $v }};{{ end }}":                   "a=1;b=2;",
		"{{ for i, v in $l }}{{ $i }}{{ $v }} {{ end }}":                    "0x 1y ",
		"{{ if $x > 5 }}big{{ else if $x > 1 }}mid{{ else }}small{{ end }}": "mid",
		"{{# a comment }}x":                                                 "x",
		"line\n  {{- $x -}}  \nnext":                                        "line2next",
		"{{ $inj }}":                                                        "{{ $x }} [ 1 + 1 ] $(ps)",
		"{{ $l }} {{ $m }}":                                                 `["x","y"] {"a":1,"b":2}`,
	} {
		got, err := renderTemplate(tpl, vars(`{"x": 2, "s": "hi", "m": {"a": 1, "b": 2}, "l": ["x", "y"], "inj": "{{ $x }} [ 1 + 1 ] $(ps)"}`), 1000)
		if err != nil || got != want {
			t.Errorf("%q: got %q err %v want %q", tpl, got, err, want)
		}
	}
	for tpl, msg := range map[string]string{
		"{{ $nope }}":                      "line 1: unset nope",
		"a\n\n{{ if true }}x":              "line 3: if needs end",
		"{{ end }}":                        "end without if",
		"{{ for x $l }}{{ end }}":          "for needs in",
		"{{ for a, b, c in $l }}{{ end }}": "one or two names",
		"{{ ps }}":                         "unknown name ps",
		"{{ x":                             "unclosed {{",
		"{{ for i in range(1, 3000) }}.{{ end }}":                             "loop max",
		strings.Repeat("{{ if true }}", 70) + strings.Repeat("{{ end }}", 70): "too deep",
		"{{ repeat(\"x\", 5000000) }}{{ repeat(\"x\", 5000000) }}":            "too large",
	} {
		_, err := renderTemplate(tpl, vars(`{"l": [1]}`), 1000)
		if err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%.50q: err %v want %q", tpl, err, msg)
		}
	}
	if _, err := renderTemplate("x", vars(`[1]`), 10); err == nil {
		t.Fatal("vars must be a map")
	}
}

func TestTemplatePipeline(t *testing.T) {
	p := &tplParent{files: map[string]string{"/srv/ops/t.tpl": "{{ for r in $vars.rows }}{{ $r.n }}\n{{ end }}"}}
	out := Run(`echo '{"rows": [{"n": "a"}, {"n": "b"}]}' | from json | template /srv/ops/t.tpl`, tplGrant(), p, Options{})
	if out.Status != 0 || out.Stdout != "a\nb\n" {
		t.Fatalf("%+v", out)
	}
}

// Review: plan mode diffs only a readable out, and template_file needs
// file.read itself; check names lose control bytes.
func TestTemplateFileReviewFixes(t *testing.T) {
	g := grant.Grant{V: "sash/1", Entitlements: []string{"file.read", "file.write"}, FileRead: []string{"/srv/ops/in"}, FileWrite: []string{"/srv/out"}}
	p := &tplParent{files: map[string]string{"/srv/ops/in/v.json": `{"a": 1}`, "/srv/ops/in/t.tpl": "{{ $a }}", "/srv/out/shadow": "SECRET-CONTENT"}}
	st := NewState()
	st.LockPlan()
	out := Run("template_file /srv/ops/in/v.json /srv/ops/in/t.tpl /srv/out/shadow", g, p, Options{State: st})
	for _, c := range p.calls {
		if c.Path == "/srv/out/shadow" && c.Key == "file.read" {
			t.Fatalf("read a write-only file: %+v", out)
		}
	}
	if strings.Contains(out.Stdout+out.Stderr, "SECRET") || !strings.Contains(out.Stderr, "would write /srv/out/shadow") {
		t.Fatalf("%+v", out)
	}
	noRead := grant.Grant{V: "sash/1", Entitlements: []string{"file.write"}, FileRead: []string{"/srv/ops/in"}, FileWrite: []string{"/srv/out"}}
	p.calls = nil
	if out := Run("template_file /srv/ops/in/v.json /srv/ops/in/t.tpl /srv/out/x", noRead, p, Options{}); out.Status != exitDenied || len(p.calls) != 0 {
		t.Fatalf("needs file.read: %+v", out)
	}
	if out := Run("check \"a\x1b[2Jb\nok forged\" [ true ]", tplGrant(), p, Options{}); strings.ContainsAny(out.Stdout[:len(out.Stdout)-1], "\x1b\n") {
		t.Fatalf("control bytes: %q", out.Stdout)
	}
	if got := oneLine("a\u009b2Jb c"); got != "a 2Jb c" {
		t.Fatalf("C1: %q", got)
	}
}
