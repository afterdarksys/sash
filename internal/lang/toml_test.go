package lang

import (
	"strings"
	"testing"
)

func TestFromTOML(t *testing.T) {
	doc := "title = \"fleet\"\nzeta = 1\n\n[server]\nport = 8080\nhosts = [\"web1\", \"db1\"]\nstarted = 2026-11-10T04:00:00Z\n\n[[pool]]\nname = \"a\"\ncpu = 90\n\n[[pool]]\nname = \"b\"\ncpu = 10\n"
	out := yamlRun(t, doc, "from toml | to json")
	for _, want := range []string{`"title": "fleet"`, `"zeta": 1`, `"port": 8080`, `"started": "2026-11-10T04:00:00Z"`, `"name": "b"`} {
		if !strings.Contains(out.Stdout, want) {
			t.Errorf("missing %s in %s (err %q)", want, out.Stdout, out.Stderr)
		}
	}
	if strings.Index(out.Stdout, "title") > strings.Index(out.Stdout, "zeta") || strings.Index(out.Stdout, "zeta") > strings.Index(out.Stdout, "server") {
		t.Errorf("document order lost:\n%s", out.Stdout)
	}
	st := NewState()
	st.vars["doc"] = value{kind: valStr, s: doc}
	got := Run("let c = (echo $doc | from toml)\necho $c.server.hosts[1] $c.pool[0].cpu\nfor p in [ $c.pool ] { echo $p.name }", exprGrant(), &rowsParent{}, Options{State: st})
	if got.Status != 0 || got.Stdout != "db1 90\na\nb\n" {
		t.Fatalf("%+v", got)
	}
}

func TestToTOML(t *testing.T) {
	out := yamlRun(t, `{"b": 1, "a": {"x": "y"}, "t": [{"n": 1}, {"n": 2}]}`, "from json | to toml")
	if out.Status != 0 || !strings.Contains(out.Stdout, "b = 1") || !strings.Contains(out.Stdout, "[a]\n  x = \"y\"") || !strings.Contains(out.Stdout, "[[t]]") {
		t.Fatalf("status %d out %q err %q", out.Status, out.Stdout, out.Stderr)
	}
	for doc, msg := range map[string]string{
		`{"a": null}`: "toml has no null",
		`[1, 2]`:      "toml needs a map",
	} {
		if out := yamlRun(t, doc, "from json | to toml"); out.Status != exitType || !strings.Contains(out.Stderr, msg) {
			t.Errorf("%s: %+v", doc, out)
		}
	}
}

func TestFromTOMLRefuses(t *testing.T) {
	for doc, msg := range map[string]string{
		"a = [" + strings.Repeat("[", 70) + strings.Repeat("]", 70) + "]\n": "toml too deep",
		"a = \n":         "toml syntax",
		"a = 1\na = 2\n": "toml syntax",
		"a = inf\n":      "toml float",
	} {
		out := yamlRun(t, doc, "from toml")
		if out.Status != exitType || !strings.Contains(out.Stderr, msg) || out.Stdout != "" {
			t.Errorf("%.40q: %+v want %q", doc, out, msg)
		}
	}
	if !tomlTooDeep([]byte(strings.Repeat("[", 8<<20))) {
		t.Fatal("depth guard")
	}
	if tomlTooDeep([]byte(`a = "` + strings.Repeat("[", 100) + `"`)) {
		t.Fatal("brackets in a string counted")
	}
}
