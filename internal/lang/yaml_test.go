package lang

import (
	"strings"
	"testing"
)

func yamlRun(t *testing.T, doc, pipe string) Outcome {
	t.Helper()
	st := NewState()
	st.vars["doc"] = value{kind: valStr, s: doc}
	return Run("echo $doc | "+pipe, exprGrant(), &rowsParent{}, Options{State: st})
}

func TestFromYAML(t *testing.T) {
	doc := "server:\n  port: 8080\n  hosts: [web1, db1]\n  tls: true\nratio: 0.5\nname: \"007\"\nnone: ~\n"
	out := yamlRun(t, doc, "from yaml | to json")
	want := "{\n  \"server\": {\n    \"port\": 8080,\n    \"hosts\": [\n      \"web1\",\n      \"db1\"\n    ],\n    \"tls\": true\n  },\n  \"ratio\": 0.5,\n  \"name\": \"007\",\n  \"none\": null\n}\n"
	if out.Status != 0 || out.Stdout != want {
		t.Fatalf("status %d out %q err %q", out.Status, out.Stdout, out.Stderr)
	}
	out = yamlRun(t, "- name: a\n  cpu: 90\n- name: b\n  cpu: 10\n", "from yaml |> where cpu >= 50 |> select name")
	if out.Status != 0 || out.Stdout != "a\n" {
		t.Fatalf("rows: %+v", out)
	}
	out = yamlRun(t, "a: 1\n---\nb: 2\n", "from yaml | to json")
	if out.Status != 0 || !strings.HasPrefix(out.Stdout, "[") {
		t.Fatalf("multi-document is a list: %+v", out)
	}
	out = yamlRun(t, "base: &b {x: 1}\nuse: *b\n", "from yaml | to json")
	if out.Status != 0 || strings.Count(out.Stdout, "\"x\": 1") != 2 {
		t.Fatalf("alias: %+v", out)
	}
}

func TestToYAMLRoundTrip(t *testing.T) {
	out := yamlRun(t, `{"b": 1, "a": [true, null, "x: y"], "n": "123"}`, "from json | to yaml")
	want := "b: 1\na:\n  - true\n  - null\n  - 'x: y'\nn: \"123\"\n"
	if out.Status != 0 || out.Stdout != want {
		t.Fatalf("status %d out %q err %q", out.Status, out.Stdout, out.Stderr)
	}
	out = Run("ps |> to yaml", exprGrant(), &rowsParent{}, Options{})
	if out.Status != 0 || !strings.Contains(out.Stdout, "- user: ryan\n  pid: \"1234\"") {
		t.Fatalf("rows: %+v", out)
	}
}

func TestFromYAMLRefuses(t *testing.T) {
	laughs := "a: &a [\"lol\",\"lol\",\"lol\",\"lol\",\"lol\",\"lol\",\"lol\",\"lol\",\"lol\"]\n"
	prev := "a"
	for _, n := range []string{"b", "c", "d", "e", "f", "g", "h", "i"} {
		laughs += n + ": &" + n + " [" + strings.Repeat("*"+prev+",", 9) + "*" + prev + "]\n"
		prev = n
	}
	for doc, msg := range map[string]string{
		laughs:                           "yaml too large",
		"a: !custom x\n":                 "yaml tag !custom",
		"a: !!binary aGk=\n":             "yaml tag !!binary",
		"b: &b {x: 1}\nc:\n  <<: *b\n":   "merge keys",
		"a: .inf\n":                      "yaml float",
		"a: [1\n":                        "yaml syntax",
		"? [a, b]\n: c\n":                "yaml key",
		strings.Repeat("- ", 80) + "x\n": "yaml too deep",
	} {
		out := yamlRun(t, doc, "from yaml")
		if out.Status != exitType || !strings.Contains(out.Stderr, msg) || out.Stdout != "" {
			t.Errorf("%.40q: %+v want %q", doc, out, msg)
		}
	}
}
