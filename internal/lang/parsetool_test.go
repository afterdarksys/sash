package lang

import (
	"strings"
	"testing"
)

func TestParseAndSub(t *testing.T) {
	st := NewState()
	st.vars["pw"] = value{kind: valStr, s: "root:x:0:0:root:/root:/bin/bash\nryan:x:501:20:Ryan:/Users/ryan:/bin/zsh\nbroken line\n"}
	st.vars["log"] = value{kind: valStr, s: "GET /a 200 12ms\nPOST /b 500 99ms\n"}
	for src, want := range map[string]string{
		`echo $pw | parse "{user}:{_}:{uid}:{gid}:{rest}" |> select user uid`:                                  "root  0\nryan  501",
		`echo $pw | parse "{user}:{_}:{uid}:{gid}:{rest}" |> filter [ $it.uid > 0 ] |> select rest`:            "Ryan:/Users/ryan:/bin/zsh",
		`echo $log | parse re "(?P<verb>\w+) (?P<path>\S+) (?P<code>\d+)" |> where code == 500 |> select path`: "/b",
		`echo $log | parse "{verb} {path} {code} {ms}" |> sub ms 'ms$' with "" |> sum ms`:                      "111",
		`echo $log | parse "{verb} {path} {_}" |> sub path '^/(.*)' with 'x-$1' |> select path`:                "x-a\nx-b",
		`echo "a.b*c" | parse "{x}.b*{y}"`: "a  c",
	} {
		out := Run(src, exprGrant(), &rowsParent{}, Options{State: st})
		if out.Status != 0 || strings.TrimSuffix(out.Stdout, "\n") != want {
			t.Errorf("%s: status %d out %q err %q want %q", src, out.Status, out.Stdout, out.Stderr, want)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	for src, msg := range map[string]string{
		`echo x | parse "{a}:{a}"`:                            "field",
		`echo x | parse "{a b}"`:                              "field",
		`echo x | parse "{a"`:                                 "unclosed",
		`echo x | parse "nofields"`:                           "no fields",
		`echo x | parse re "(\w+)"`:                           "needs (?P<name>",
		`echo x | parse re "("`:                               "regex",
		`echo x | parse`:                                      "usage parse",
		`ps |> sub user "(" with x`:                           "regex",
		`ps |> sub user "x" to y`:                             "usage sub",
		`echo x | sub a "b" with c`:                           "type |",
		`echo x | parse "` + strings.Repeat("{a}", 400) + `"`: "too long",
	} {
		out := Run(src, exprGrant(), &rowsParent{}, Options{})
		if out.Status != exitType || !strings.Contains(out.Stderr, msg) {
			t.Errorf("%s: %+v want %q", src, out, msg)
		}
	}
}
