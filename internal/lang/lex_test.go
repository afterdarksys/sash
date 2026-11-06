package lang

import (
	"strings"
	"testing"
)

func kinds(toks []token) []kind {
	out := make([]kind, len(toks))
	for i, t := range toks {
		out[i] = t.kind
	}
	return out
}

func TestLexOrAndRow(t *testing.T) {
	toks, err := lex("a || b && c |> d | e")
	if err != nil {
		t.Fatal(err)
	}
	want := []kind{kWord, kOr, kWord, kAnd, kWord, kRow, kWord, kPipe, kWord, kEOF}
	got := kinds(toks)
	if len(got) != len(want) {
		t.Fatalf("kinds %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("at %d got %v want %v", i, got, want)
		}
	}
}

func TestLexBareAmpRejected(t *testing.T) {
	_, err := lex("a &")
	if err == nil || !strings.Contains(err.Error(), "&") {
		t.Fatalf("err %v", err)
	}
}

func TestLexCommaIsSeparator(t *testing.T) {
	toks, err := lex("fn f(a, b)")
	if err != nil {
		t.Fatal(err)
	}
	var words []string
	for _, tok := range toks {
		if tok.kind == kWord {
			words = append(words, tok.word.Parts[0].Lit)
		}
	}
	if strings.Join(words, ",") != "fn,f,a,b" {
		t.Fatalf("words %v", words)
	}
}

func TestLexCompareIsWord(t *testing.T) {
	toks, err := lex(`state == "failed"`)
	if err != nil {
		t.Fatal(err)
	}
	if toks[1].kind != kWord || toks[1].word.Parts[0].Lit != "==" {
		t.Fatalf("token %+v", toks[1])
	}
	if toks[0].kind == kGT || toks[2].kind == kGT {
		t.Fatal("compare parsed as a redirect")
	}
}

func TestLexStarIsLiteral(t *testing.T) {
	toks, err := lex("echo *")
	if err != nil {
		t.Fatal(err)
	}
	if toks[1].kind != kWord || toks[1].word.Parts[0].Lit != "*" {
		t.Fatalf("star %+v", toks[1])
	}
}
