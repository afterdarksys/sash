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

func TestLexHereString(t *testing.T) {
	toks, err := lex("cat <<< hello")
	if err != nil {
		t.Fatal(err)
	}
	if toks[1].kind != kHere {
		t.Fatalf("here %v", kinds(toks))
	}
	cmp, err := lex("a <= b")
	if err != nil {
		t.Fatal(err)
	}
	if cmp[1].kind != kWord || cmp[1].word.Parts[0].Lit != "<=" {
		t.Fatalf("compare %+v", cmp[1])
	}
	two, err := lex("cat << hello")
	if err != nil {
		t.Fatal(err)
	}
	if two[1].kind != kLT || two[2].kind != kLT {
		t.Fatalf("two %v", kinds(two))
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

// Bash descriptor redirections are parse errors, never a silent misparse
// (before v7, `echo a 2> x` wrote "a 2" to x).
func TestLexRefusesBashRedirections(t *testing.T) {
	for src, want := range map[string]string{
		"echo a 2> /tmp/x": "2> is not sash redirection",
		"echo a 2>&1":      "2>&1 is not sash redirection",
		"echo a 1>/tmp/x":  "1> is not sash redirection",
		"echo a &> /tmp/x": "&> is not sash redirection",
		"echo a >& /tmp/x": ">& is not sash redirection",
		"echo a >> /tmp/x": "use template_file or atomic for appends",
		"cat 0< /tmp/x":    "0< is not sash redirection",
		"echo a <& 3":      "<& is not sash redirection",
	} {
		if _, err := lex(src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", src, err, want)
		}
	}
	for _, src := range []string{"echo 2 > /tmp/x", "echo a > /tmp/x", "echo [ 2 > 1 ]", "x >= 2", "cat <<< hi", "ps |> where pid > 1"} {
		if _, err := lex(src); err != nil {
			t.Errorf("%q: %v", src, err)
		}
	}
}
