package lang

import (
	"strings"
	"testing"
)

func TestFromJSON(t *testing.T) {
	for src, want := range map[string]string{
		`let c = (echo '{"s": {"port": 8080, "tags": ["a", "b"]}, "n": 2.5, "z": null}' | from json); echo $c.s.port $c.s.tags[1] $c.n $c.z`: "8080 b 2.5 null",
		`echo '[{"name": "web1", "cpu": 90}, {"name": "db1", "cpu": 10}]' | from json |> where cpu >= 50 |> select name`:                     "web1",
		`echo '{"b": 1, "a": 2}' | from json`:                                    `{"b":1,"a":2}`,
		`echo '[{"name": "a"}, {"name": "b"}]' | from json | to json`:            "[\n  {\n    \"name\": \"a\"\n  },\n  {\n    \"name\": \"b\"\n  }\n]",
		`for h in echo '[{"n": "a"}, {"n": "b"}]' | from json { echo $h.n }`:     "a\nb",
		`for k, v in echo '{"x": 1, "y": 2}' | from json { echo $k $v }`:         "x 1\ny 2",
		`let big = (echo '12345678901234567890' | from json); echo [ $big > 1 ]`: "true",
		`echo [ {"k": [1, {"j": true}]} ] | from json | to json`:                 "{\n  \"k\": [\n    1,\n    {\n      \"j\": true\n    }\n  ]\n}",
		`ps |> to json`: "[\n  {\n    \"user\": \"ryan\",\n    \"pid\": \"1234\",\n    \"cmd\": \"sshd\"\n  },\n  {\n    \"user\": \"root\",\n    \"pid\": \"1\",\n    \"cmd\": \"init\"\n  }\n]",
		`ps | to json`:  `"ryan  1234  sshd\nroot  1  init\n"`,
	} {
		out := Run(src, exprGrant(), &rowsParent{}, Options{})
		if out.Status != 0 || strings.TrimSuffix(out.Stdout, "\n") != want {
			t.Errorf("%s: status %d out %q err %q want %q", src, out.Status, out.Stdout, out.Stderr, want)
		}
	}
}

func TestFromJSONRefuses(t *testing.T) {
	deep := strings.Repeat("[", 70) + strings.Repeat("]", 70)
	for src, msg := range map[string]string{
		`echo '{"a":' | from json`:             "json truncated",
		`echo '{"a": 1} x' | from json`:        "json trailing data",
		`echo '{1: 2}' | from json`:            "json",
		`echo '` + deep + `' | from json`:      "json too deep",
		`echo '1' | from json |> where a == 1`: "type |> number",
		`echo x | from xml`:                    "usage from",
		`echo x | to`:                          "usage to",
		`from json`:                            "",
	} {
		out := Run(src, exprGrant(), &rowsParent{}, Options{})
		if out.Status == 0 || !strings.Contains(out.Stderr, msg) || (msg != "" && out.Stdout != "") {
			t.Errorf("%s: %+v want %q", src, out, msg)
		}
	}
	big := "[" + strings.Repeat(`"xxxxxxxx",`, 900000) + "1]"
	if _, err := runFrom("json", value{kind: valStr, s: big}); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("size: %v", err)
	}
}

// A decoded string is data: JSON holding sash source stays a string.
func TestFromJSONStringsStayData(t *testing.T) {
	p := &listParent{}
	out := Run(`let v = (echo '{"cmd": "ps; unit restart nginx", "x": "[ 1 + 1 ]"}' | from json); echo $v.cmd $v.x`, injectGrant(), p, Options{})
	if out.Status != 0 || out.Stdout != "ps; unit restart nginx [ 1 + 1 ]\n" || len(p.calls) != 0 {
		t.Fatalf("%+v calls %d", out, len(p.calls))
	}
}
