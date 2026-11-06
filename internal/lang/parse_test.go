package lang

import "testing"

func TestParseElseAfterNewline(t *testing.T) {
	prog, err := parse("if echo x {\n echo yes\n}\nelse {\n echo no\n}\n")
	if err != nil {
		t.Fatal(err)
	}
	iff, ok := prog.Stmts[0].(*If)
	if !ok || iff.Else == nil {
		t.Fatalf("else dropped: %#v", prog.Stmts[0])
	}
}

func TestParseFnParamsIgnoreCommas(t *testing.T) {
	prog, err := parse("fn f(a, b) {\n echo $a\n}\n")
	if err != nil {
		t.Fatal(err)
	}
	fn, ok := prog.Stmts[0].(*Fn)
	if !ok {
		t.Fatalf("%#v", prog.Stmts[0])
	}
	if len(fn.Params) != 2 || fn.Params[0] != "a" || fn.Params[1] != "b" {
		t.Fatalf("params %v", fn.Params)
	}
}

func TestParseOrChain(t *testing.T) {
	prog, err := parse("echo a || echo b\n")
	if err != nil {
		t.Fatal(err)
	}
	ch, ok := prog.Stmts[0].(*Chain)
	if !ok || len(ch.Ops) != 1 || ch.Ops[0] != "||" {
		t.Fatalf("%#v", prog.Stmts[0])
	}
}
