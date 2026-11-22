package line

import (
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

type unitRowsParent struct{ reads []string }

func (p *unitRowsParent) Do(v parent.Verb) (parent.Result, error) {
	switch v.Key {
	case "unit.status":
		return parent.Result{Kind: parent.KindRows, Cols: []string{"name"}, Rows: []parent.Row{{"name": "hosts"}}}, nil
	case "file.read":
		p.reads = append(p.reads, v.Path)
	}
	return parent.Result{Kind: parent.KindBytes, Bytes: []byte("x\n")}, nil
}

// A line typed at the editor's prompt keeps using host text as a word,
// while the same text as a script is refused (lang path.go).
func TestPromptKeepsHostTextWords(t *testing.T) {
	g := grant.Grant{V: "sash/1", Entitlements: []string{"unit.status", "file.read"}, FileRead: []string{"/etc"}}
	p := &unitRowsParent{}
	e := New(g, p)
	if out := e.run("for r in unit status { cat /etc/$r.name }"); out.Status != 0 || len(p.reads) != 1 {
		t.Fatalf("%+v %v", out, p.reads)
	}
}
