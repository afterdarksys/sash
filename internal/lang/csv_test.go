package lang

import (
	"strings"
	"testing"
)

func TestCSV(t *testing.T) {
	st := NewState()
	st.vars["c"] = value{kind: valStr, s: "host,cpu,note\nweb1,90,\"busy, very\"\ndb1,10,\n"}
	st.vars["t"] = value{kind: valStr, s: "a\tb\n1\t2\n"}
	st.vars["ragged"] = value{kind: valStr, s: "ragged,x\n1\n"}
	for src, want := range map[string]string{
		`echo $c | from csv |> filter [ $it.cpu > 50 ] |> select host note`: "web1  busy, very",
		`echo $c | from csv |> sum cpu`:                                     "100",
		`echo $c | from csv --noheader |> first 1 |> select c1 c2`:          "host  cpu",
		`echo $t | from tsv |> select b`:                                    "2",
		`echo $c | from csv |> to csv`:                                      "host,cpu,note\nweb1,90,\"busy, very\"\ndb1,10,",
		`echo $c | from csv |> to tsv`:                                      "host\tcpu\tnote\nweb1\t90\tbusy, very\ndb1\t10\t",
		`echo '[{"a": "=1+1", "b": -5, "c": "-x"}]' | from json | to csv`:   "a,b,c\n'=1+1,-5,'-x",
		`echo $ragged | from csv |> select x`:                               "",
	} {
		out := Run(src, exprGrant(), &rowsParent{}, Options{State: st})
		if out.Status != 0 || strings.TrimSuffix(out.Stdout, "\n") != want {
			t.Errorf("%s: status %d out %q err %q want %q", src, out.Status, out.Stdout, out.Stderr, want)
		}
	}
}

func TestCSVRefuses(t *testing.T) {
	st := NewState()
	st.vars["many"] = value{kind: valStr, s: "a\n" + strings.Repeat("1\n", 3000000)}
	st.vars["dup"] = value{kind: valStr, s: "a,a\n1,2\n"}
	st.vars["wide"] = value{kind: valStr, s: "a\n1,2\n"}
	st.vars["bad"] = value{kind: valStr, s: "a\n\"x\n"}
	for src, msg := range map[string]string{
		`echo $dup | from csv`:            "duplicate column",
		`echo $wide | from csv`:           "wider than the header",
		`echo $bad | from csv`:            "csv syntax",
		`echo $many | from csv`:           "csv too large",
		`echo x | from json --noheader`:   "usage from",
		`echo [ 5 ] | from json | to csv`: "csv needs rows",
	} {
		out := Run(src, exprGrant(), &rowsParent{}, Options{State: st})
		if out.Status != exitType || !strings.Contains(out.Stderr, msg) {
			t.Errorf("%s: %+v want %q", src, out.Stderr, msg)
		}
	}
}
