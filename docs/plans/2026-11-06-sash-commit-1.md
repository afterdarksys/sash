# sash commit 1 Implementation Plan

> **For Codex:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Do not start these tasks until Ryan says to build.** This file is the plan. It is not permission to create the module, run `git init`, or commit.

**Goal:** A Go shell language that runs `ps -ef | egrep sshd` and `unit status |> where state == "failed"` against a fake parent, refuses a denied stage before any call, and exits 2 on a pipe type error before any call.

**Architecture:** `internal/grant` holds the in-memory `sash/1` set and answers `Has` and path roots. `internal/parent` is the verb port the agent will implement in commit 3. `internal/lang` lexes, parses, and executes one script. `cmd/sash` is a thin `-c` / file / stdin entry with an empty grant. Filters stay in process. Toolkit names call `Parent.Do`. There is no host `exec` and no `PATH`.

**Tech Stack:** Go 1.26.0. Module `github.com/afterdarksys/sash`. Directory `/Users/ryan/development/sash`. Standard library only.

**Behavior home:** `docs/plans/2026-11-06-sash-design.md`. If a snippet in this plan and that file disagree, stop and report the line. The design wins. Commit 1 has no signature code. Read `~/development/ads-fable-utils/SECURITY-RULES.md` before commit 2, not before this one.

**Leave out of this commit:** the grant pipe, ed25519, readline, globs, abbreviations, `!!`, `!$`, job control, a `rows` command, `config write`, Starlark, and any import of `github.com/afterdarksys/adssh`. A `*` word is a literal. The scratch tree at `/tmp/sash-plan` is not the product. Write the files below into `/Users/ryan/development/sash`.

**Stderr tokens this commit prints:** `denied <key>`, `unknown <name>`, `type |>`, `type |`, `type fn`, `type value`, `type rows`, `type for`, `prefix_unset`, `usage <name>`. Status 2 is a type or usage error. Status 126 is a denial. Status 127 is an unknown name. Status 1 from `egrep` with no match does not stop the rest of the script. Status 126, 127, or 2 does.

**Commit messages:** use the message in each task and nothing after it. No `Co-Authored-By` trailer and no "Generated with" line.

---

## Before task 1

The directory already holds the design doc and this plan. It is not a git repo yet.

1. `cd /Users/ryan/development/sash`
2. `git init`
3. `git add docs/plans/2026-11-06-sash-design.md docs/plans/2026-11-06-sash-commit-1.md`
4. `git commit -m "docs: record the sash design and the language plan"`
5. `git status --short` prints nothing. `git log -1 --format=%B` is that one line.

---

## Task 1: In-memory grant

**Files:**
- Create: `go.mod`
- Create: `internal/grant/grant_test.go`
- Create: `internal/grant/grant.go`

Why a star authorizes nothing: a `*` grant in adssh means every command. In sash that would be a privileged shell. `Has` returns false for `""`, for `"*"`, and for every name when the list contains `*`.

Why path checks clean first: `/var/log/../../etc/passwd` must miss `/var/log`. `/var/logbook` must miss `/var/log`. A relative path misses. Commit 1 trusts the caller for the signature. That check arrives in commit 2.

**Step 1: Write the module file**

Create `go.mod`:

```
module github.com/afterdarksys/sash

go 1.26.0
```

**Step 2: Write the failing test**

Create `internal/grant/grant_test.go`:

```go
package grant

import "testing"

func TestHasRejectsStar(t *testing.T) {
	g := Grant{Entitlements: []string{"ps", "*"}}
	if g.Has("ps") {
		t.Fatal("star grant authorized ps")
	}
	if g.Has("*") {
		t.Fatal("star grant authorized *")
	}
}

func TestHasListedKey(t *testing.T) {
	g := Grant{Entitlements: []string{"ps", "unit.status"}}
	if !g.Has("ps") || !g.Has("unit.status") {
		t.Fatal("listed key denied")
	}
	if g.Has("unit.restart") {
		t.Fatal("unlisted key allowed")
	}
}

func TestPathRoots(t *testing.T) {
	g := Grant{FileRead: []string{"/var/log"}, FileWrite: []string{"/var/tmp/ops"}}
	if !g.AllowsRead("/var/log") || !g.AllowsRead("/var/log/syslog") {
		t.Fatal("read root rejected a path inside it")
	}
	if g.AllowsRead("/var/logbook") || g.AllowsRead("/etc/passwd") || g.AllowsRead("syslog") {
		t.Fatal("read root accepted a path outside it")
	}
	if g.AllowsRead("/var/log/../../etc/passwd") {
		t.Fatal("read root accepted a cleaned escape")
	}
	if !g.AllowsWrite("/var/tmp/ops/out") {
		t.Fatal("write root rejected a path inside it")
	}
	if g.AllowsWrite("/var/tmp/other") {
		t.Fatal("write root accepted a sibling")
	}
}
```

**Step 3: Run the test and watch it fail**

Run: `go test ./internal/grant -count=1`

Expected: FAIL, `undefined: Grant`.

**Step 4: Write the implementation**

Create `internal/grant/grant.go`:

```go
// Package grant is the in-memory sash/1 entitlement set.
// Commit 1 trusts the caller. Signature checks arrive in commit 2.
package grant

import (
	"path"
	"strings"
)

// Grant is one sash/1 document without a signature.
type Grant struct {
	V            string
	Actor        string
	Host         string
	Purpose      string
	Exp          int64
	LeaseID      string
	Entitlements []string
	FileRead     []string
	FileWrite    []string
	AllowPrivate bool
}

// Has reports whether key is listed.
// A grant containing "*" authorizes nothing, including the listed names beside it.
func (g Grant) Has(key string) bool {
	if key == "" || key == "*" {
		return false
	}
	star := false
	found := false
	for _, e := range g.Entitlements {
		if e == "*" {
			star = true
		}
		if e == key {
			found = true
		}
	}
	return found && !star
}

// AllowsRead reports whether absolute path p is p itself or a descendant of a file_read root.
func (g Grant) AllowsRead(p string) bool { return under(g.FileRead, p) }

// AllowsWrite reports whether absolute path p is p itself or a descendant of a file_write root.
func (g Grant) AllowsWrite(p string) bool { return under(g.FileWrite, p) }

func under(roots []string, p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	c := path.Clean(p)
	for _, r := range roots {
		if !strings.HasPrefix(r, "/") {
			continue
		}
		r = path.Clean(r)
		if c == r || strings.HasPrefix(c, r+"/") {
			return true
		}
	}
	return false
}
```

**Step 5: Run the test and watch it pass**

Run: `gofmt -w internal/grant && go test ./internal/grant -count=1`

Expected: `ok github.com/afterdarksys/sash/internal/grant`

**Step 6: Commit**

```bash
git add go.mod internal/grant
git commit -m "feat: check sash grant names and path roots"
```

---

## Task 2: Parent verb port

**Files:**
- Create: `internal/parent/parent.go`

The agent implements `Do` in commit 3. Commit 1 tests pass a fake. `Verb.Path` is the resolved file path. `Verb.Body` is the text for `file.write`. `Result.Cols` is the render order for rows.

**Step 1: Write the port**

Create `internal/parent/parent.go`:

```go
// Package parent is the toolkit port. Commit 1 tests use a fake.
// The systemapi agent implements it in commit 3.
package parent

// Kind is the shape of a stage result.
type Kind int

const (
	// KindBytes is a text stream. The "|" pipe renders rows into this shape.
	KindBytes Kind = iota
	// KindRows is a table. The "|>" pipe accepts only this shape.
	KindRows
)

// Row is one record. Keys are field names.
type Row map[string]string

// Result is what a stage or a parent verb returns.
type Result struct {
	Kind  Kind
	Bytes []byte
	Rows  []Row
	Cols  []string
	Exit  int
}

// Verb is one entitled call. Path is set for file verbs. Body is the text for file.write.
type Verb struct {
	Key  string
	Argv []string
	Path string
	Body []byte
}

// Parent fulfills toolkit verbs. sash does not exec host binaries.
type Parent interface {
	Do(v Verb) (Result, error)
}
```

**Step 2: Confirm the package builds**

Run: `gofmt -w internal/parent && go test ./internal/parent -count=1`

Expected: `? github.com/afterdarksys/sash/internal/parent [no test files]`

**Step 3: Commit**

```bash
git add internal/parent
git commit -m "feat: add the sash parent verb port"
```

---

## Task 3: Lexer

**Files:**
- Create: `internal/lang/lex_test.go`
- Create: `internal/lang/lex.go`

`||` is the or operator. `|>` is the row pipe. `|` is the byte pipe. A bare `&` is a lex error because job control is a later series. A comma is whitespace so `fn f(a, b)` and `select a, b` parse. `==`, `!=`, `>=`, and `<=` are words, not redirects. A `*` word stays a literal until commit 7.

**Step 1: Write the failing test**

Create `internal/lang/lex_test.go`:

```go
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
```

**Step 2: Run the test and watch it fail**

Run: `go test ./internal/lang -count=1 -run 'TestLex'`

Expected: FAIL, `undefined: lex`.

**Step 3: Write the lexer**

Create `internal/lang/lex.go`:

```go
package lang

import (
	"fmt"
	"strings"
	"unicode"
)

type kind int

const (
	kEOF kind = iota
	kNL
	kWord
	kPipe
	kRow
	kAnd
	kOr
	kSemi
	kGT
	kLT
	kLBrace
	kRBrace
	kLParen
	kRParen
	kEq
)

type token struct {
	kind kind
	word Word
}

// Word is one shell word. Expansion happens later, so a value is never parsed as syntax.
type Word struct {
	Parts []Part
}

// Part is a literal or a $name / $name.field reference.
type Part struct {
	Lit string
	Exp bool
}

func (w Word) empty() bool { return len(w.Parts) == 0 }

func lex(src string) ([]token, error) {
	var out []token
	i := 0
	for i < len(src) {
		c := src[i]
		switch c {
		case ' ', '\t', '\r':
			i++
			continue
		case '\n':
			out = append(out, token{kind: kNL})
			i++
			continue
		case '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		case ';':
			out = append(out, token{kind: kSemi})
			i++
		case '{':
			out = append(out, token{kind: kLBrace})
			i++
		case '}':
			out = append(out, token{kind: kRBrace})
			i++
		case '(':
			out = append(out, token{kind: kLParen})
			i++
		case ')':
			out = append(out, token{kind: kRParen})
			i++
		case '|':
			if i+1 < len(src) && src[i+1] == '>' {
				out = append(out, token{kind: kRow})
				i += 2
				continue
			}
			if i+1 < len(src) && src[i+1] == '|' {
				out = append(out, token{kind: kOr})
				i += 2
				continue
			}
			out = append(out, token{kind: kPipe})
			i++
		case ',':
			i++
			continue
		case '&':
			if i+1 < len(src) && src[i+1] == '&' {
				out = append(out, token{kind: kAnd})
				i += 2
				continue
			}
			return nil, fmt.Errorf("lex: & is not in this series")
		case '>':
			if i+1 < len(src) && src[i+1] == '=' {
				out = append(out, token{kind: kWord, word: Word{Parts: []Part{{Lit: ">="}}}})
				i += 2
				continue
			}
			out = append(out, token{kind: kGT})
			i++
		case '<':
			if i+1 < len(src) && src[i+1] == '=' {
				out = append(out, token{kind: kWord, word: Word{Parts: []Part{{Lit: "<="}}}})
				i += 2
				continue
			}
			out = append(out, token{kind: kLT})
			i++
		case '=':
			if i+1 < len(src) && src[i+1] == '=' {
				out = append(out, token{kind: kWord, word: Word{Parts: []Part{{Lit: "=="}}}})
				i += 2
				continue
			}
			out = append(out, token{kind: kEq})
			i++
		case '!':
			if i+1 < len(src) && src[i+1] == '=' {
				out = append(out, token{kind: kWord, word: Word{Parts: []Part{{Lit: "!="}}}})
				i += 2
				continue
			}
			return nil, fmt.Errorf("lex: bare !")
		case '\'':
			w, n, err := lexRaw(src[i:])
			if err != nil {
				return nil, err
			}
			out = append(out, token{kind: kWord, word: w})
			i += n
		case '"':
			w, n, err := lexDouble(src[i:])
			if err != nil {
				return nil, err
			}
			out = append(out, token{kind: kWord, word: w})
			i += n
		default:
			w, n, err := lexBare(src[i:])
			if err != nil {
				return nil, err
			}
			out = append(out, token{kind: kWord, word: w})
			i += n
		}
	}
	out = append(out, token{kind: kEOF})
	return out, nil
}

func lexRaw(src string) (Word, int, error) {
	if len(src) < 2 || src[0] != '\'' {
		return Word{}, 0, fmt.Errorf("lex: quote")
	}
	end := strings.IndexByte(src[1:], '\'')
	if end < 0 {
		return Word{}, 0, fmt.Errorf("lex: unclosed quote")
	}
	return Word{Parts: []Part{{Lit: src[1 : 1+end]}}}, 2 + end, nil
}

func lexDouble(src string) (Word, int, error) {
	var parts []Part
	i := 1
	lit := strings.Builder{}
	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, Part{Lit: lit.String()})
			lit.Reset()
		}
	}
	for i < len(src) {
		switch src[i] {
		case '"':
			flush()
			return Word{Parts: parts}, i + 1, nil
		case '$':
			flush()
			name, n, err := lexExpand(src[i:])
			if err != nil {
				return Word{}, 0, err
			}
			parts = append(parts, Part{Lit: name, Exp: true})
			i += n
		default:
			lit.WriteByte(src[i])
			i++
		}
	}
	return Word{}, 0, fmt.Errorf("lex: unclosed quote")
}

func lexBare(src string) (Word, int, error) {
	if src[0] == '$' {
		name, n, err := lexExpand(src)
		if err != nil {
			return Word{}, 0, err
		}
		return Word{Parts: []Part{{Lit: name, Exp: true}}}, n, nil
	}
	i := 0
	for i < len(src) && !bareStop(src[i]) {
		i++
	}
	if i == 0 {
		return Word{}, 0, fmt.Errorf("lex: empty word")
	}
	return Word{Parts: []Part{{Lit: src[:i]}}}, i, nil
}

func bareStop(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', '#', ';', '{', '}', '(', ')', '|', '&', '>', '<', '=', '!', '\'', '"', ',':
		return true
	default:
		return false
	}
}

func lexExpand(src string) (string, int, error) {
	if len(src) < 2 || src[0] != '$' {
		return "", 0, fmt.Errorf("lex: $")
	}
	i := 1
	if !isNameStart(rune(src[i])) {
		return "", 0, fmt.Errorf("lex: bad $")
	}
	i++
	for i < len(src) && isName(rune(src[i])) {
		i++
	}
	if i < len(src) && src[i] == '.' {
		j := i + 1
		if j >= len(src) || !isNameStart(rune(src[j])) {
			return "", 0, fmt.Errorf("lex: bad field")
		}
		j++
		for j < len(src) && isName(rune(src[j])) {
			j++
		}
		i = j
	}
	return src[1:i], i, nil
}

func isNameStart(r rune) bool {
	return unicode.IsLetter(r) || r == '_'
}

func isName(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}
```

**Step 4: Run the test and watch it pass**

Run: `gofmt -w internal/lang/lex.go internal/lang/lex_test.go && go test ./internal/lang -count=1 -run 'TestLex'`

Expected: `ok github.com/afterdarksys/sash/internal/lang`

**Step 5: Commit**

```bash
git add internal/lang/lex.go internal/lang/lex_test.go
git commit -m "feat: lex sash pipes, quotes, and expansions"
```

---

## Task 4: Parser

**Files:**
- Create: `internal/lang/parse_test.go`
- Create: `internal/lang/parse.go`

Keywords `let`, `if`, `for`, and `fn` are statements only when the word is a single literal. A newline between `}` and `else` still attaches the else. `let name = word` stores a word. `let name = (pipeline)` stores a pipeline. `fn` is not a pipe stage.

**Step 1: Write the failing test**

Create `internal/lang/parse_test.go`:

```go
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
```

**Step 2: Run the test and watch it fail**

Run: `go test ./internal/lang -count=1 -run 'TestParse'`

Expected: FAIL, `undefined: parse`.

**Step 3: Write the parser**

Create `internal/lang/parse.go`:

```go
package lang

import "fmt"

// Program is a script: the prompt, sash -c, and sash file share this tree.
type Program struct {
	Stmts []Stmt
}

// Stmt is one statement.
type Stmt interface{ stmt() }

// Chain is pipelines joined by && and ||.
type Chain struct {
	Pipes []Pipeline
	Ops   []string
}

func (*Chain) stmt() {}

// Let binds a word or a parenthesized pipeline. The value is not parsed again.
type Let struct {
	Name string
	Word *Word
	Pipe *Pipeline
}

func (*Let) stmt() {}

// If runs Cond. A row pipeline is true when it yields a row. A byte pipeline is true when its exit is 0.
type If struct {
	Cond Pipeline
	Then Program
	Else *Program
}

func (*If) stmt() {}

// For walks the rows of Src.
type For struct {
	Name string
	Src  Pipeline
	Body Program
}

func (*For) stmt() {}

// Fn defines a command. Each call checks entitlements again.
type Fn struct {
	Name   string
	Params []string
	Body   Program
}

func (*Fn) stmt() {}

// Pipeline is stages joined by "|" or "|>".
type Pipeline struct {
	Stages []Stage
	Ops    []string
}

// Stage is one command plus redirects.
type Stage struct {
	Argv   []Word
	Redirs []Redir
}

// Redir is "<" or ">".
type Redir struct {
	Kind string
	Path Word
}

type parser struct {
	tok []token
	i   int
}

func parse(src string) (*Program, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{tok: toks}
	prog, err := p.program()
	if err != nil {
		return nil, err
	}
	p.skipSep()
	if p.peek().kind != kEOF {
		return nil, fmt.Errorf("parse: trailing input")
	}
	return prog, nil
}

func (p *parser) program() (*Program, error) {
	var stmts []Stmt
	p.skipSep()
	for {
		k := p.peek().kind
		if k == kEOF || k == kRBrace {
			break
		}
		s, err := p.stmt()
		if err != nil {
			return nil, err
		}
		stmts = append(stmts, s)
		if !p.atSep() && p.peek().kind != kEOF && p.peek().kind != kRBrace {
			return nil, fmt.Errorf("parse: expected separator")
		}
		p.skipSep()
	}
	return &Program{Stmts: stmts}, nil
}

func (p *parser) atSep() bool {
	k := p.peek().kind
	return k == kNL || k == kSemi
}

func (p *parser) skipSep() {
	for p.atSep() {
		p.i++
	}
}

func (p *parser) stmt() (Stmt, error) {
	if p.peek().kind == kWord && len(p.peek().word.Parts) == 1 && !p.peek().word.Parts[0].Exp {
		switch p.peek().word.Parts[0].Lit {
		case "let":
			return p.let()
		case "if":
			return p.iff()
		case "for":
			return p.forr()
		case "fn":
			return p.fn()
		}
	}
	return p.chain()
}

func (p *parser) let() (Stmt, error) {
	p.i++
	name, err := p.plainName("let")
	if err != nil {
		return nil, err
	}
	if p.peek().kind != kEq {
		return nil, fmt.Errorf("parse: let needs =")
	}
	p.i++
	if p.peek().kind == kLParen {
		p.i++
		pipe, err := p.pipeline()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != kRParen {
			return nil, fmt.Errorf("parse: let needs )")
		}
		p.i++
		return &Let{Name: name, Pipe: &pipe}, nil
	}
	if p.peek().kind != kWord {
		return nil, fmt.Errorf("parse: let needs a word")
	}
	w := p.peek().word
	p.i++
	return &Let{Name: name, Word: &w}, nil
}

func (p *parser) iff() (Stmt, error) {
	p.i++
	cond, err := p.pipeline()
	if err != nil {
		return nil, err
	}
	then, err := p.block()
	if err != nil {
		return nil, err
	}
	p.skipSep()
	var elseP *Program
	if p.peek().kind == kWord && plain(p.peek().word) == "else" {
		p.i++
		body, err := p.block()
		if err != nil {
			return nil, err
		}
		elseP = body
	}
	return &If{Cond: cond, Then: *then, Else: elseP}, nil
}

func (p *parser) forr() (Stmt, error) {
	p.i++
	name, err := p.plainName("for")
	if err != nil {
		return nil, err
	}
	if plain(p.peek().word) != "in" || p.peek().kind != kWord {
		return nil, fmt.Errorf("parse: for needs in")
	}
	p.i++
	src, err := p.pipeline()
	if err != nil {
		return nil, err
	}
	body, err := p.block()
	if err != nil {
		return nil, err
	}
	return &For{Name: name, Src: src, Body: *body}, nil
}

func (p *parser) fn() (Stmt, error) {
	p.i++
	name, err := p.plainName("fn")
	if err != nil {
		return nil, err
	}
	if p.peek().kind != kLParen {
		return nil, fmt.Errorf("parse: fn needs (")
	}
	p.i++
	var params []string
	for p.peek().kind != kRParen {
		if p.peek().kind == kWord && plain(p.peek().word) == "," {
			p.i++
			continue
		}
		n, err := p.plainName("fn param")
		if err != nil {
			return nil, err
		}
		params = append(params, n)
		if p.peek().kind == kWord && plain(p.peek().word) == "," {
			p.i++
		}
	}
	p.i++
	body, err := p.block()
	if err != nil {
		return nil, err
	}
	return &Fn{Name: name, Params: params, Body: *body}, nil
}

func (p *parser) block() (*Program, error) {
	if p.peek().kind != kLBrace {
		return nil, fmt.Errorf("parse: needs {")
	}
	p.i++
	body, err := p.program()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != kRBrace {
		return nil, fmt.Errorf("parse: needs }")
	}
	p.i++
	return body, nil
}

func (p *parser) chain() (Stmt, error) {
	first, err := p.pipeline()
	if err != nil {
		return nil, err
	}
	c := &Chain{Pipes: []Pipeline{first}}
	for p.peek().kind == kAnd || p.peek().kind == kOr {
		op := "&&"
		if p.peek().kind == kOr {
			op = "||"
		}
		p.i++
		next, err := p.pipeline()
		if err != nil {
			return nil, err
		}
		c.Ops = append(c.Ops, op)
		c.Pipes = append(c.Pipes, next)
	}
	return c, nil
}

func (p *parser) pipeline() (Pipeline, error) {
	st, err := p.stage()
	if err != nil {
		return Pipeline{}, err
	}
	pl := Pipeline{Stages: []Stage{st}}
	for p.peek().kind == kPipe || p.peek().kind == kRow {
		op := "|"
		if p.peek().kind == kRow {
			op = "|>"
		}
		p.i++
		next, err := p.stage()
		if err != nil {
			return Pipeline{}, err
		}
		pl.Ops = append(pl.Ops, op)
		pl.Stages = append(pl.Stages, next)
	}
	return pl, nil
}

func (p *parser) stage() (Stage, error) {
	var st Stage
	for {
		switch p.peek().kind {
		case kWord:
			st.Argv = append(st.Argv, p.peek().word)
			p.i++
		case kGT, kLT:
			kind := ">"
			if p.peek().kind == kLT {
				kind = "<"
			}
			p.i++
			if p.peek().kind != kWord {
				return Stage{}, fmt.Errorf("parse: redirect needs a path")
			}
			st.Redirs = append(st.Redirs, Redir{Kind: kind, Path: p.peek().word})
			p.i++
		default:
			if len(st.Argv) == 0 {
				return Stage{}, fmt.Errorf("parse: empty command")
			}
			return st, nil
		}
	}
}

func (p *parser) plainName(what string) (string, error) {
	if p.peek().kind != kWord || plain(p.peek().word) == "" {
		return "", fmt.Errorf("parse: %s needs a name", what)
	}
	n := plain(p.peek().word)
	p.i++
	return n, nil
}

func plain(w Word) string {
	if len(w.Parts) != 1 || w.Parts[0].Exp {
		return ""
	}
	return w.Parts[0].Lit
}

func (p *parser) peek() token {
	if p.i >= len(p.tok) {
		return token{kind: kEOF}
	}
	return p.tok[p.i]
}
```

**Step 4: Run the test and watch it pass**

Run: `gofmt -w internal/lang/parse.go internal/lang/parse_test.go && go test ./internal/lang -count=1 -run 'TestParse|TestLex'`

Expected: `ok github.com/afterdarksys/sash/internal/lang`

**Step 5: Commit**

```bash
git add internal/lang/parse.go internal/lang/parse_test.go
git commit -m "feat: parse sash statements and both pipes"
```

---

## Task 5: Executor

**Files:**
- Create: `internal/lang/lang_test.go`
- Create: `internal/lang/eval.go`

One `Run` serves the prompt, `sash -c`, and a file. Preflight covers the whole `&&` / `||` chain before any parent call, so `echo kept && unit restart cron` prints nothing when `unit.restart` is missing. `|` renders rows to text before the next stage. `|>` into `egrep` is `type |>` and calls nothing. A value is one word: `let pat = "sshd | unit restart cron"` then `egrep $pat` calls only `ps`, and the spaces stay inside the regexp so the row does not match. `let pat = "sshd|unit restart cron"` matches and still calls only `ps`. `>` on a path outside `file_write` calls nothing. A path inside the root calls `ps` then `file.write`, and stdout stays empty. `cd /tmp` with read roots `/var/log` and `/etc` is `denied file.read`, the prefix stays, and the parent is not called. `cd /etc` stats, then the prefix is `/etc`.

The fake `ps` rows use columns `user`, `pid`, `cmd`. The rendered sshd row is `root  1  /usr/sbin/sshd\n` (two spaces). Text render uses `Cols` order, two spaces between columns, one line per row.

**Step 1: Write the failing test**

Create `internal/lang/lang_test.go`:

```go
package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

type fake struct {
	calls    []parent.Verb
	statExit int
}

func (f *fake) Do(v parent.Verb) (parent.Result, error) {
	f.calls = append(f.calls, v)
	switch {
	case v.Key == "ps":
		return parent.Result{
			Kind: parent.KindRows,
			Cols: []string{"user", "pid", "cmd"},
			Rows: []parent.Row{
				{"user": "root", "pid": "1", "cmd": "/usr/sbin/sshd"},
				{"user": "root", "pid": "2", "cmd": "cron"},
			},
		}, nil
	case v.Key == "unit.status":
		return parent.Result{
			Kind: parent.KindRows,
			Cols: []string{"name", "state"},
			Rows: []parent.Row{
				{"name": "cron", "state": "failed"},
				{"name": "sshd", "state": "running"},
			},
		}, nil
	case v.Key == "file.read" && len(v.Argv) > 0 && v.Argv[0] == "stat":
		return parent.Result{Kind: parent.KindBytes, Exit: f.statExit}, nil
	case v.Key == "file.read":
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte("sshd line\n")}, nil
	case v.Key == "file.write":
		return parent.Result{Kind: parent.KindBytes}, nil
	default:
		return parent.Result{Kind: parent.KindBytes, Bytes: []byte("ok\n")}, nil
	}
}

func allow() grant.Grant {
	return grant.Grant{
		V:            "sash/1",
		Entitlements: []string{"ps", "unit.status", "unit.restart", "journal", "package.query", "package.install", "sysctl.get", "sysctl.set", "config.read", "file.read", "file.write"},
		FileRead:     []string{"/var/log", "/etc"},
		FileWrite:    []string{"/var/tmp/ops"},
	}
}

func keys(f *fake) []string {
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.Key
	}
	return out
}

func TestBytePipeFiltersText(t *testing.T) {
	f := &fake{}
	got := Run(`ps -ef | egrep sshd | head -n 1`, allow(), f, Options{})
	if got.Status != 0 {
		t.Fatalf("status %d stderr %s", got.Status, got.Stderr)
	}
	if got.Stdout != "root  1  /usr/sbin/sshd\n" {
		t.Fatalf("stdout %q", got.Stdout)
	}
	if strings.Join(keys(f), ",") != "ps" {
		t.Fatalf("calls %v", keys(f))
	}
}

func TestRowPipeKeepsRows(t *testing.T) {
	f := &fake{}
	got := Run(`unit status |> where state == "failed"`, allow(), f, Options{})
	if got.Status != 0 {
		t.Fatalf("status %d stderr %s", got.Status, got.Stderr)
	}
	if got.Stdout != "cron  failed\n" {
		t.Fatalf("stdout %q", got.Stdout)
	}
}

func TestRowPipeTypeErrorRunsNothing(t *testing.T) {
	f := &fake{}
	got := Run(`ps -ef |> egrep sshd`, allow(), f, Options{})
	if got.Status != exitType {
		t.Fatalf("status %d", got.Status)
	}
	if !strings.Contains(got.Stderr, "type |>") {
		t.Fatalf("stderr %q", got.Stderr)
	}
	if len(f.calls) != 0 {
		t.Fatalf("parent called: %v", keys(f))
	}
}

func TestDenialRunsNothing(t *testing.T) {
	f := &fake{}
	g := allow()
	g.Entitlements = []string{"ps"}
	got := Run(`ps -ef | unit restart cron`, g, f, Options{})
	if got.Status != exitDenied {
		t.Fatalf("status %d stderr %s", got.Status, got.Stderr)
	}
	if !strings.Contains(got.Stderr, "denied unit.restart") {
		t.Fatalf("stderr %q", got.Stderr)
	}
	if len(f.calls) != 0 {
		t.Fatalf("parent called: %v", keys(f))
	}
}

func TestAndDenialRunsNothing(t *testing.T) {
	f := &fake{}
	g := allow()
	g.Entitlements = []string{"ps"}
	got := Run(`echo kept && unit restart cron`, g, f, Options{})
	if got.Status != exitDenied || got.Stdout != "" || len(f.calls) != 0 {
		t.Fatalf("status %d stdout %q calls %v stderr %s", got.Status, got.Stdout, keys(f), got.Stderr)
	}
}

func TestLetValueIsNotSyntax(t *testing.T) {
	f := &fake{}
	src := "let pat = \"sshd|unit restart cron\"\nps -ef | egrep $pat\n"
	got := Run(src, allow(), f, Options{})
	if strings.Join(keys(f), ",") != "ps" {
		t.Fatalf("calls %v stderr %s", keys(f), got.Stderr)
	}
	if got.Status != 0 || !strings.Contains(got.Stdout, "sshd") {
		t.Fatalf("status %d stdout %q stderr %s", got.Status, got.Stdout, got.Stderr)
	}

	f = &fake{}
	src = "let pat = \"sshd | unit restart cron\"\nps -ef | egrep $pat\n"
	got = Run(src, allow(), f, Options{})
	if strings.Join(keys(f), ",") != "ps" {
		t.Fatalf("spaced calls %v stderr %s", keys(f), got.Stderr)
	}
	// Spaces stay inside the one regexp, so "sshd " does not match the row.
	// A word split would have searched for sshd and printed it.
	if got.Status != 1 || got.Stdout != "" {
		t.Fatalf("spaced status %d stdout %q stderr %s", got.Status, got.Stdout, got.Stderr)
	}
}

func TestIfRowTruth(t *testing.T) {
	f := &fake{}
	src := "if unit status |> where state == \"failed\" {\n  echo yes\n} else {\n  echo no\n}\n"
	got := Run(src, allow(), f, Options{})
	if got.Stdout != "yes\n" || got.Status != 0 {
		t.Fatalf("status %d stdout %q stderr %s", got.Status, got.Stdout, got.Stderr)
	}
	src = "if unit status |> where state == \"missing\" {\n  echo yes\n} else {\n  echo no\n}\n"
	got = Run(src, allow(), f, Options{})
	if got.Stdout != "no\n" || got.Status != 0 {
		t.Fatalf("stdout %q status %d stderr %s", got.Stdout, got.Status, got.Stderr)
	}
}

func TestForWalksRows(t *testing.T) {
	f := &fake{}
	src := "for row in unit status |> where state == \"failed\" {\n  echo $row.name\n}\n"
	got := Run(src, allow(), f, Options{})
	if got.Stdout != "cron\n" || got.Status != 0 {
		t.Fatalf("status %d stdout %q stderr %s", got.Status, got.Stdout, got.Stderr)
	}
}

func TestFnChecksEveryCall(t *testing.T) {
	f := &fake{}
	g := allow()
	g.Entitlements = []string{"unit.status"}
	src := "fn boom() {\n  unit restart cron\n}\nboom\n"
	got := Run(src, g, f, Options{})
	if got.Status != exitDenied || len(f.calls) != 0 {
		t.Fatalf("status %d calls %v stderr %s", got.Status, keys(f), got.Stderr)
	}
	g = allow()
	got = Run(src, g, f, Options{})
	if got.Status != 0 || strings.Join(keys(f), ",") != "unit.restart" {
		t.Fatalf("status %d calls %v stderr %s", got.Status, keys(f), got.Stderr)
	}
}

func TestRedirectPreflight(t *testing.T) {
	f := &fake{}
	got := Run(`ps -ef > /etc/passwd`, allow(), f, Options{})
	if got.Status != exitDenied || len(f.calls) != 0 {
		t.Fatalf("escape status %d calls %v stderr %s", got.Status, keys(f), got.Stderr)
	}
	got = Run(`ps -ef > /var/tmp/ops/out`, allow(), f, Options{})
	if got.Status != 0 || strings.Join(keys(f), ",") != "ps,file.write" {
		t.Fatalf("write status %d calls %v stderr %s", got.Status, keys(f), got.Stderr)
	}
	if got.Stdout != "" {
		t.Fatalf("redirect printed %q", got.Stdout)
	}
	if !strings.Contains(string(f.calls[1].Body), "sshd") {
		t.Fatalf("body %q", f.calls[1].Body)
	}
}

func TestUnknown(t *testing.T) {
	f := &fake{}
	got := Run(`rm -rf /`, allow(), f, Options{})
	if got.Status != exitUnknown || len(f.calls) != 0 {
		t.Fatalf("status %d calls %v", got.Status, keys(f))
	}
	if !strings.Contains(got.Stderr, "unknown rm") {
		t.Fatalf("stderr %q", got.Stderr)
	}
}

func TestPipefail(t *testing.T) {
	f := &fake{}
	got := Run(`echo hello | egrep sshd | echo kept`, allow(), f, Options{})
	if got.Status != 1 || got.Stdout != "kept\n" {
		t.Fatalf("status %d stdout %q", got.Status, got.Stdout)
	}
}

func TestStarGrantAuthorizesNothing(t *testing.T) {
	f := &fake{}
	g := allow()
	g.Entitlements = []string{"*", "ps"}
	got := Run(`ps -ef`, g, f, Options{})
	if got.Status != exitDenied || len(f.calls) != 0 {
		t.Fatalf("status %d calls %v stderr %s", got.Status, keys(f), got.Stderr)
	}
}

func TestCdStaysUnderReadRoot(t *testing.T) {
	f := &fake{}
	got := Run("cd /tmp\ndirs\n", allow(), f, Options{Prefix: "/var/log"})
	if got.Status != exitDenied || got.Prefix != "/var/log" || len(f.calls) != 0 {
		t.Fatalf("status %d prefix %s calls %v stderr %s stdout %q", got.Status, got.Prefix, keys(f), got.Stderr, got.Stdout)
	}
	f = &fake{}
	got = Run("cd /etc\ndirs\n", allow(), f, Options{Prefix: "/var/log"})
	if got.Status != 0 || got.Prefix != "/etc" || got.Stdout != "/etc\n" {
		t.Fatalf("status %d prefix %s stdout %q stderr %s calls %v", got.Status, got.Prefix, got.Stdout, got.Stderr, keys(f))
	}
	if f.calls[0].Argv[0] != "stat" || f.calls[0].Path != "/etc" {
		t.Fatalf("stat verb %+v", f.calls[0])
	}
}
```

**Step 2: Run the test and watch it fail**

Run: `go test ./internal/lang -count=1 -run 'TestBytePipe'`

Expected: FAIL, `undefined: Run`.

**Step 3: Write the executor**

Create `internal/lang/eval.go`:

```go
package lang

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

const (
	exitType    = 2
	exitDenied  = 126
	exitUnknown = 127
)

const (
	valStr = iota
	valBytes
	valRows
	valRow
)

const (
	inNone = iota
	inBytes
	inRows
	inFlex
)

// Outcome is the result of one script, one -c string, or one file.
type Outcome struct {
	Status int
	Stdout string
	Stderr string
	Prefix string
}

// Options carries the session prefix. The process cwd is not this prefix.
type Options struct {
	Prefix string
}

// Run parses src and executes it. Toolkit names call p. Filters stay in process.
// A denied or mistyped pipeline performs no parent call.
func Run(src string, g grant.Grant, p parent.Parent, opt Options) Outcome {
	prog, err := parse(src)
	if err != nil {
		return Outcome{Status: exitType, Stderr: err.Error() + "\n", Prefix: opt.Prefix}
	}
	m := &machine{
		g:    g,
		p:    p,
		vars: map[string]value{},
		fns:  map[string]*Fn{},
	}
	if opt.Prefix != "" {
		m.stack = []string{path.Clean(opt.Prefix)}
	}
	status, _ := m.runProg(prog)
	return Outcome{Status: status, Stdout: m.out.String(), Stderr: m.err.String(), Prefix: m.prefix()}
}

type value struct {
	kind int
	s    string
	b    []byte
	rows []parent.Row
	cols []string
	row  parent.Row
}

type machine struct {
	g     grant.Grant
	p     parent.Parent
	stack []string
	vars  map[string]value
	fns   map[string]*Fn
	out   strings.Builder
	err   strings.Builder
}

func (m *machine) prefix() string {
	if len(m.stack) == 0 {
		return ""
	}
	return m.stack[len(m.stack)-1]
}

func (m *machine) runProg(prog *Program) (int, bool) {
	status := 0
	for _, s := range prog.Stmts {
		st, stop := m.runStmt(s)
		status = st
		if stop {
			return status, true
		}
	}
	return status, false
}

func (m *machine) runStmt(s Stmt) (int, bool) {
	switch s := s.(type) {
	case *Let:
		return m.runLet(s)
	case *If:
		return m.runIf(s)
	case *For:
		return m.runFor(s)
	case *Fn:
		return m.defFn(s)
	case *Chain:
		return m.runChain(s)
	default:
		m.err.WriteString("type stmt\n")
		return exitType, true
	}
}

func (m *machine) defFn(fn *Fn) (int, bool) {
	if reserved(fn.Name) {
		m.err.WriteString("usage fn\n")
		return exitType, true
	}
	m.fns[fn.Name] = fn
	return 0, false
}

func (m *machine) runLet(s *Let) (int, bool) {
	if s.Word != nil {
		text, err := m.expand(*s.Word)
		if err != nil {
			m.err.WriteString(err.Error() + "\n")
			return exitType, true
		}
		m.vars[s.Name] = value{kind: valStr, s: text}
		return 0, false
	}
	st, stop, v := m.runPipe(*s.Pipe)
	if stop {
		return st, true
	}
	m.vars[s.Name] = v
	return 0, false
}

func (m *machine) runIf(s *If) (int, bool) {
	st, stop, v := m.runPipe(s.Cond)
	if stop {
		return st, true
	}
	take := st == 0
	if v.kind == valRows {
		take = len(v.rows) > 0
	}
	var branch *Program
	if take {
		branch = &s.Then
	} else {
		branch = s.Else
	}
	if branch == nil {
		return 0, false
	}
	return m.runProg(branch)
}

func (m *machine) runFor(s *For) (int, bool) {
	st, stop, v := m.runPipe(s.Src)
	if stop {
		return st, true
	}
	if v.kind != valRows {
		m.err.WriteString("type for\n")
		return exitType, true
	}
	status := 0
	for _, row := range v.rows {
		prev, had := m.vars[s.Name]
		m.vars[s.Name] = value{kind: valRow, row: row}
		st, stop = m.runProg(&s.Body)
		if had {
			m.vars[s.Name] = prev
		} else {
			delete(m.vars, s.Name)
		}
		status = st
		if stop {
			return status, true
		}
	}
	return status, false
}

func (m *machine) runChain(c *Chain) (int, bool) {
	prepared := make([][]expStage, len(c.Pipes))
	for i, pipe := range c.Pipes {
		exp, prob := m.prepare(pipe)
		if prob != nil {
			m.err.WriteString(prob.msg + "\n")
			return prob.status, true
		}
		prepared[i] = exp
	}
	status := 0
	for i, exp := range prepared {
		if i > 0 && c.Ops[i-1] == "&&" && status != 0 {
			break
		}
		if i > 0 && c.Ops[i-1] == "||" && status == 0 {
			break
		}
		st, v := m.execPrepared(exp)
		status = st
		if exp[len(exp)-1].outPath == "" && exp[0].call == nil {
			m.out.WriteString(render(v))
		}
		if st == exitDenied || st == exitUnknown || st == exitType {
			return st, true
		}
	}
	return status, false
}

func (m *machine) runPipe(pipe Pipeline) (int, bool, value) {
	exp, prob := m.prepare(pipe)
	if prob != nil {
		m.err.WriteString(prob.msg + "\n")
		return prob.status, true, value{}
	}
	st, v := m.execPrepared(exp)
	stop := st == exitDenied || st == exitUnknown || st == exitType
	return st, stop, v
}

type expStage struct {
	argv    []string
	inPath  string
	outPath string
	path    string
	key     string
	op      string
	call    *Fn
	mover   bool
}

type problem struct {
	status int
	msg    string
}

func (m *machine) prepare(pipe Pipeline) ([]expStage, *problem) {
	exp := make([]expStage, len(pipe.Stages))
	for i, st := range pipe.Stages {
		argv, err := m.expandArgv(st.Argv)
		if err != nil {
			return nil, &problem{exitType, err.Error()}
		}
		var inPath, outPath string
		for _, r := range st.Redirs {
			text, err := m.expand(r.Path)
			if err != nil {
				return nil, &problem{exitType, err.Error()}
			}
			full, perr := resolvePath(m.prefix(), text)
			if perr != nil {
				return nil, &problem{exitType, "prefix_unset"}
			}
			if r.Kind == "<" {
				if i != 0 || inPath != "" {
					return nil, &problem{exitType, "usage <"}
				}
				if !m.g.AllowsRead(full) {
					return nil, &problem{exitDenied, "denied file.read"}
				}
				inPath = full
			} else {
				if i != len(pipe.Stages)-1 || outPath != "" {
					return nil, &problem{exitType, "usage >"}
				}
				if !m.g.AllowsWrite(full) {
					return nil, &problem{exitDenied, "denied file.write"}
				}
				outPath = full
			}
		}
		info, prob := classify(argv, m.fns)
		if prob != nil {
			return nil, prob
		}
		if info.call != nil && (len(pipe.Stages) != 1 || inPath != "" || outPath != "") {
			return nil, &problem{exitType, "type fn"}
		}
		if info.mover && (len(pipe.Stages) != 1 || inPath != "" || outPath != "") {
			return nil, &problem{exitType, "type " + argv[0]}
		}
		op := ""
		if i > 0 {
			op = pipe.Ops[i-1]
		}
		exp[i] = expStage{
			argv: argv, inPath: inPath, outPath: outPath,
			key: info.key, op: op, call: info.call, mover: info.mover,
		}
	}
	if prob := typecheck(exp); prob != nil {
		return nil, prob
	}
	for i := range exp {
		if exp[i].key != "" && !m.g.Has(exp[i].key) {
			return nil, &problem{exitDenied, "denied " + exp[i].key}
		}
	}
	for i := range exp {
		if prob := m.bindPaths(&exp[i]); prob != nil {
			return nil, prob
		}
	}
	return exp, nil
}

type classified struct {
	key   string
	in    int
	out   int
	call  *Fn
	mover bool
}

func classify(argv []string, fns map[string]*Fn) (classified, *problem) {
	if len(argv) == 0 {
		return classified{}, &problem{exitType, "usage empty"}
	}
	name := argv[0]
	if fn, ok := fns[name]; ok {
		if len(argv)-1 != len(fn.Params) {
			return classified{}, &problem{exitType, "usage " + name}
		}
		return classified{call: fn, in: inNone, out: inBytes}, nil
	}
	switch name {
	case "echo", "dirs":
		if name == "dirs" && len(argv) != 1 {
			return classified{}, &problem{exitType, "usage dirs"}
		}
		return classified{in: inNone, out: inBytes}, nil
	case "egrep":
		if len(argv) != 2 {
			return classified{}, &problem{exitType, "usage egrep"}
		}
		if _, err := regexp.Compile(argv[1]); err != nil {
			return classified{}, &problem{exitType, "usage egrep"}
		}
		return classified{in: inBytes, out: inBytes}, nil
	case "cut":
		if !hasFlag(argv, "-f") {
			return classified{}, &problem{exitType, "usage cut"}
		}
		return classified{in: inBytes, out: inBytes}, nil
	case "head", "wc":
		return classified{in: inFlex, out: inFlex}, nil
	case "where":
		if len(argv) != 4 || !isOp(argv[2]) {
			return classified{}, &problem{exitType, "usage where"}
		}
		return classified{in: inRows, out: inRows}, nil
	case "select":
		if len(argv) < 2 {
			return classified{}, &problem{exitType, "usage select"}
		}
		return classified{in: inRows, out: inRows}, nil
	case "cd", "pushd":
		if len(argv) != 2 {
			return classified{}, &problem{exitType, "usage " + name}
		}
		return classified{key: "file.read", mover: true, in: inNone, out: inBytes}, nil
	case "popd":
		if len(argv) != 1 {
			return classified{}, &problem{exitType, "usage popd"}
		}
		return classified{mover: true, in: inNone, out: inBytes}, nil
	case "ps":
		return classified{key: "ps", in: inNone, out: inRows}, nil
	case "journal":
		return classified{key: "journal", in: inNone, out: inRows}, nil
	case "ls":
		if len(argv) > 2 {
			return classified{}, &problem{exitType, "usage ls"}
		}
		return classified{key: "file.read", in: inNone, out: inRows}, nil
	case "cat":
		if len(argv) == 1 {
			return classified{in: inBytes, out: inBytes}, nil
		}
		if len(argv) == 2 {
			return classified{key: "file.read", in: inNone, out: inBytes}, nil
		}
		return classified{}, &problem{exitType, "usage cat"}
	case "unit":
		sub := "status"
		if len(argv) > 1 {
			sub = argv[1]
		}
		switch sub {
		case "status":
			return classified{key: "unit.status", in: inNone, out: inRows}, nil
		case "restart":
			if len(argv) != 3 {
				return classified{}, &problem{exitType, "usage unit"}
			}
			return classified{key: "unit.restart", in: inNone, out: inBytes}, nil
		default:
			return classified{}, &problem{exitType, "usage unit"}
		}
	case "package":
		if len(argv) < 2 {
			return classified{}, &problem{exitType, "usage package"}
		}
		switch argv[1] {
		case "query":
			return classified{key: "package.query", in: inNone, out: inRows}, nil
		case "install":
			if len(argv) != 3 {
				return classified{}, &problem{exitType, "usage package"}
			}
			return classified{key: "package.install", in: inNone, out: inBytes}, nil
		default:
			return classified{}, &problem{exitType, "usage package"}
		}
	case "sysctl":
		if len(argv) < 2 {
			return classified{}, &problem{exitType, "usage sysctl"}
		}
		switch argv[1] {
		case "get":
			if len(argv) != 3 {
				return classified{}, &problem{exitType, "usage sysctl"}
			}
			return classified{key: "sysctl.get", in: inNone, out: inBytes}, nil
		case "set":
			if len(argv) != 4 {
				return classified{}, &problem{exitType, "usage sysctl"}
			}
			return classified{key: "sysctl.set", in: inNone, out: inBytes}, nil
		default:
			return classified{}, &problem{exitType, "usage sysctl"}
		}
	case "config":
		if len(argv) != 3 || argv[1] != "read" {
			return classified{}, &problem{exitType, "usage config"}
		}
		return classified{key: "config.read", in: inNone, out: inBytes}, nil
	default:
		return classified{}, &problem{exitUnknown, "unknown " + name}
	}
}

func typecheck(exp []expStage) *problem {
	kind := inNone
	for i, st := range exp {
		in, out := stageShape(st)
		if i == 0 {
			if in == inRows {
				return &problem{exitType, "type rows"}
			}
			if in == inFlex {
				out = inBytes
			}
			kind = out
			continue
		}
		if st.op == "|>" {
			if kind != inRows || in == inBytes {
				return &problem{exitType, "type |>"}
			}
			if in == inFlex {
				out = inRows
			}
		} else {
			if in == inRows {
				return &problem{exitType, "type |"}
			}
			if in == inFlex {
				out = inBytes
			}
		}
		kind = out
	}
	return nil
}

func stageShape(st expStage) (int, int) {
	if st.call != nil {
		return inNone, inBytes
	}
	switch st.argv[0] {
	case "echo", "dirs", "cd", "pushd", "popd":
		return inNone, inBytes
	case "egrep", "cut":
		return inBytes, inBytes
	case "where", "select":
		return inRows, inRows
	case "head", "wc":
		return inFlex, inFlex
	case "ps", "journal", "ls":
		return inNone, inRows
	case "unit":
		if len(st.argv) > 1 && st.argv[1] == "restart" {
			return inNone, inBytes
		}
		return inNone, inRows
	case "package":
		if len(st.argv) > 1 && st.argv[1] == "query" {
			return inNone, inRows
		}
		return inNone, inBytes
	case "cat":
		if len(st.argv) == 1 {
			return inBytes, inBytes
		}
		return inNone, inBytes
	default:
		return inNone, inBytes
	}
}

func (m *machine) bindPaths(st *expStage) *problem {
	switch st.argv[0] {
	case "cat":
		if len(st.argv) == 2 {
			return m.needRead(st, st.argv[1])
		}
	case "ls":
		p := m.prefix()
		if len(st.argv) == 2 {
			p = st.argv[1]
		}
		return m.needRead(st, p)
	case "config":
		return m.needRead(st, st.argv[2])
	case "cd", "pushd":
		return m.needRead(st, st.argv[1])
	}
	return nil
}

func (m *machine) needRead(st *expStage, p string) *problem {
	full, err := resolvePath(m.prefix(), p)
	if err != nil {
		return &problem{exitType, "prefix_unset"}
	}
	if !m.g.AllowsRead(full) {
		return &problem{exitDenied, "denied file.read"}
	}
	st.path = full
	return nil
}

func (m *machine) execPrepared(exp []expStage) (int, value) {
	if len(exp) == 1 && exp[0].call != nil {
		st, _ := m.callFn(exp[0])
		return st, value{kind: valBytes}
	}
	cur := value{kind: valBytes}
	if exp[0].inPath != "" {
		res, err := m.p.Do(parent.Verb{Key: "file.read", Argv: []string{"<", exp[0].inPath}, Path: exp[0].inPath})
		if err != nil {
			m.err.WriteString("parent file.read\n")
			return 1, value{}
		}
		cur = fromResult(res)
	}
	exits := make([]int, len(exp))
	for i, st := range exp {
		if st.op == "|" && cur.kind == valRows {
			cur = value{kind: valBytes, b: []byte(render(cur))}
		}
		v, code := m.execStage(st, cur)
		exits[i] = code
		cur = v
		if code == exitType || code == exitDenied || code == exitUnknown {
			return code, cur
		}
	}
	status := 0
	for i := len(exits) - 1; i >= 0; i-- {
		if exits[i] != 0 {
			status = exits[i]
			break
		}
	}
	if exp[len(exp)-1].outPath != "" {
		body := render(cur)
		if _, err := m.p.Do(parent.Verb{
			Key:  "file.write",
			Argv: []string{">", exp[len(exp)-1].outPath},
			Path: exp[len(exp)-1].outPath,
			Body: []byte(body),
		}); err != nil {
			m.err.WriteString("parent file.write\n")
			return 1, cur
		}
	}
	return status, cur
}

func (m *machine) callFn(st expStage) (int, bool) {
	fn := st.call
	saved := map[string]value{}
	had := map[string]bool{}
	for i, name := range fn.Params {
		if old, ok := m.vars[name]; ok {
			saved[name] = old
			had[name] = true
		}
		m.vars[name] = value{kind: valStr, s: st.argv[i+1]}
	}
	status, stop := m.runProg(&fn.Body)
	for _, name := range fn.Params {
		if had[name] {
			m.vars[name] = saved[name]
		} else {
			delete(m.vars, name)
		}
	}
	return status, stop
}

func (m *machine) execStage(st expStage, in value) (value, int) {
	switch st.argv[0] {
	case "echo":
		return value{kind: valBytes, b: []byte(strings.Join(st.argv[1:], " ") + "\n")}, 0
	case "egrep":
		return runEgrep(asBytes(in), st.argv[1])
	case "head":
		return m.runHead(st.argv, in)
	case "wc":
		if in.kind == valRows {
			return value{kind: valBytes, b: []byte(strconv.Itoa(len(in.rows)) + "\n")}, 0
		}
		return value{kind: valBytes, b: []byte(strconv.Itoa(len(splitLines(asBytes(in)))) + "\n")}, 0
	case "cut":
		return value{kind: valBytes, b: []byte(runCut(asBytes(in), st.argv[1:]))}, 0
	case "where":
		rows, err := runWhere(in, st.argv[1], st.argv[2], st.argv[3])
		if err != nil {
			m.err.WriteString(err.Error() + "\n")
			return value{}, exitType
		}
		return value{kind: valRows, rows: rows, cols: in.cols}, 0
	case "select":
		return value{kind: valRows, rows: in.rows, cols: append([]string(nil), st.argv[1:]...)}, 0
	case "dirs":
		var b strings.Builder
		for _, d := range m.stack {
			b.WriteString(d)
			b.WriteByte('\n')
		}
		return value{kind: valBytes, b: []byte(b.String())}, 0
	case "popd":
		if len(m.stack) < 2 {
			m.err.WriteString("usage popd\n")
			return value{}, exitType
		}
		m.stack = m.stack[:len(m.stack)-1]
		return value{kind: valBytes}, 0
	case "cd", "pushd":
		return m.move(st)
	case "cat":
		if st.path == "" {
			return value{kind: valBytes, b: []byte(asBytes(in))}, 0
		}
		return m.toolkit(st)
	default:
		return m.toolkit(st)
	}
}

func (m *machine) move(st expStage) (value, int) {
	res, err := m.p.Do(parent.Verb{Key: "file.read", Argv: []string{"stat", st.path}, Path: st.path})
	if err != nil {
		m.err.WriteString("parent file.read\n")
		return value{}, 1
	}
	if res.Exit != 0 {
		return value{}, res.Exit
	}
	if st.argv[0] == "pushd" {
		m.stack = append(m.stack, st.path)
	} else if len(m.stack) == 0 {
		m.stack = []string{st.path}
	} else {
		m.stack[len(m.stack)-1] = st.path
	}
	return value{kind: valBytes}, 0
}

func (m *machine) toolkit(st expStage) (value, int) {
	res, err := m.p.Do(parent.Verb{Key: st.key, Argv: st.argv, Path: st.path})
	if err != nil {
		m.err.WriteString("parent " + st.key + "\n")
		return value{}, 1
	}
	return fromResult(res), res.Exit
}

func (m *machine) runHead(argv []string, in value) (value, int) {
	n, err := headCount(argv)
	if err != nil {
		m.err.WriteString("usage head\n")
		return value{}, exitType
	}
	if in.kind == valRows {
		if n > len(in.rows) {
			n = len(in.rows)
		}
		if n < 0 {
			n = 0
		}
		return value{kind: valRows, rows: in.rows[:n], cols: in.cols}, 0
	}
	return value{kind: valBytes, b: []byte(headLines(asBytes(in), n))}, 0
}

func (m *machine) expandArgv(words []Word) ([]string, error) {
	out := make([]string, len(words))
	for i, w := range words {
		s, err := m.expand(w)
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

func (m *machine) expand(w Word) (string, error) {
	var b strings.Builder
	for _, part := range w.Parts {
		if !part.Exp {
			b.WriteString(part.Lit)
			continue
		}
		name, field, hasField := strings.Cut(part.Lit, ".")
		v, ok := m.vars[name]
		if !ok {
			return "", fmt.Errorf("type value")
		}
		switch v.kind {
		case valStr:
			if hasField {
				return "", fmt.Errorf("type value")
			}
			b.WriteString(v.s)
		case valBytes:
			if hasField {
				return "", fmt.Errorf("type value")
			}
			b.WriteString(string(v.b))
		case valRow:
			if !hasField {
				return "", fmt.Errorf("type value")
			}
			b.WriteString(v.row[field])
		default:
			return "", fmt.Errorf("type value")
		}
	}
	return b.String(), nil
}

func fromResult(res parent.Result) value {
	if res.Kind == parent.KindRows {
		return value{kind: valRows, rows: res.Rows, cols: append([]string(nil), res.Cols...)}
	}
	return value{kind: valBytes, b: res.Bytes}
}

func render(v value) string {
	switch v.kind {
	case valRows:
		cols := v.cols
		if len(cols) == 0 && len(v.rows) > 0 {
			for k := range v.rows[0] {
				cols = append(cols, k)
			}
			sort.Strings(cols)
		}
		var b strings.Builder
		for _, row := range v.rows {
			for i, c := range cols {
				if i > 0 {
					b.WriteString("  ")
				}
				b.WriteString(row[c])
			}
			b.WriteByte('\n')
		}
		return b.String()
	case valStr:
		return v.s
	default:
		return string(v.b)
	}
}

func asBytes(v value) string {
	if v.kind == valRows || v.kind == valRow || v.kind == valStr {
		return render(v)
	}
	return string(v.b)
}

func runEgrep(text, pat string) (value, int) {
	re := regexp.MustCompile(pat)
	var b strings.Builder
	n := 0
	for _, line := range splitLines(text) {
		if re.MatchString(line) {
			b.WriteString(line)
			b.WriteByte('\n')
			n++
		}
	}
	if n == 0 {
		return value{kind: valBytes}, 1
	}
	return value{kind: valBytes, b: []byte(b.String())}, 0
}

func runWhere(in value, field, op, want string) ([]parent.Row, error) {
	if in.kind != valRows {
		return nil, fmt.Errorf("type |")
	}
	var keep []parent.Row
	for _, row := range in.rows {
		ok, err := compare(row[field], op, want)
		if err != nil {
			return nil, err
		}
		if ok {
			keep = append(keep, row)
		}
	}
	return keep, nil
}

func compare(got, op, want string) (bool, error) {
	gi, gerr := strconv.Atoi(got)
	wi, werr := strconv.Atoi(want)
	if gerr == nil && werr == nil {
		switch op {
		case "==":
			return gi == wi, nil
		case "!=":
			return gi != wi, nil
		case ">":
			return gi > wi, nil
		case "<":
			return gi < wi, nil
		case ">=":
			return gi >= wi, nil
		case "<=":
			return gi <= wi, nil
		}
	}
	switch op {
	case "==":
		return got == want, nil
	case "!=":
		return got != want, nil
	case ">":
		return got > want, nil
	case "<":
		return got < want, nil
	case ">=":
		return got >= want, nil
	case "<=":
		return got <= want, nil
	default:
		return false, fmt.Errorf("usage where")
	}
}

func runCut(text string, args []string) string {
	delim := ""
	field := 1
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-d":
			if i+1 < len(args) {
				delim = args[i+1]
				i++
			}
		case "-f":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					field = n
				}
				i++
			}
		}
	}
	var b strings.Builder
	for _, line := range splitLines(text) {
		var parts []string
		if delim == "" {
			parts = strings.Fields(line)
		} else {
			parts = strings.Split(line, delim)
		}
		if field >= 1 && field <= len(parts) {
			b.WriteString(parts[field-1])
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func headCount(argv []string) (int, error) {
	switch len(argv) {
	case 1:
		return 10, nil
	case 2:
		return strconv.Atoi(argv[1])
	case 3:
		if argv[1] != "-n" {
			return 0, fmt.Errorf("usage head")
		}
		return strconv.Atoi(argv[2])
	default:
		return 0, fmt.Errorf("usage head")
	}
}

func headLines(text string, n int) string {
	lines := splitLines(text)
	if n > len(lines) {
		n = len(lines)
	}
	if n < 0 {
		n = 0
	}
	if n == 0 {
		return ""
	}
	return strings.Join(lines[:n], "\n") + "\n"
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	parts := strings.Split(text, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func resolvePath(prefix, p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty")
	}
	if strings.HasPrefix(p, "/") {
		return path.Clean(p), nil
	}
	if prefix == "" {
		return "", fmt.Errorf("prefix_unset")
	}
	return path.Clean(path.Join(prefix, p)), nil
}

func reserved(name string) bool {
	switch name {
	case "echo", "egrep", "head", "cut", "wc", "where", "select",
		"ps", "unit", "journal", "package", "sysctl", "config", "cat", "ls",
		"cd", "pushd", "popd", "dirs", "let", "if", "for", "fn", "else", "in":
		return true
	default:
		return false
	}
}

func isOp(s string) bool {
	switch s {
	case "==", "!=", ">", "<", ">=", "<=":
		return true
	default:
		return false
	}
}

func hasFlag(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
	}
	return false
}
```

**Step 4: Run the tests and watch them pass**

Run: `gofmt -w internal/lang && go test ./internal/lang -count=1`

Expected: `ok github.com/afterdarksys/sash/internal/lang`

**Step 5: Commit**

```bash
git add internal/lang/eval.go internal/lang/lang_test.go
git commit -m "feat: execute sash scripts against a parent port"
```

---

## Task 6: Command entry

**Files:**
- Create: `cmd/sash/main.go`

No readline. No grant pipe. Args are `sash -c script`, `sash file`, or stdin as one script. The hand-started grant is empty and the parent returns an error, so a toolkit verb denies and `echo` still runs.

**Step 1: Write the entry**

Create `cmd/sash/main.go`:

```go
// Command sash is the non-interactive entry for commit 1.
// The live prompt arrives with the line editor. A hand start has an empty
// grant, so toolkit verbs are denied and filters still run.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/lang"
	"github.com/afterdarksys/sash/internal/parent"
)

type closedParent struct{}

func (closedParent) Do(parent.Verb) (parent.Result, error) {
	return parent.Result{}, fmt.Errorf("no parent")
}

func main() {
	src, err := readSource(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(langExit(2))
	}
	got := lang.Run(src, grant.Grant{}, closedParent{}, lang.Options{})
	fmt.Fprint(os.Stdout, got.Stdout)
	fmt.Fprint(os.Stderr, got.Stderr)
	os.Exit(langExit(got.Status))
}

func readSource(args []string) (string, error) {
	if len(args) >= 2 && args[0] == "-c" {
		return args[1], nil
	}
	if len(args) == 1 {
		b, err := os.ReadFile(args[0])
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	if len(args) == 0 {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	return "", fmt.Errorf("usage: sash [-c script | file]")
}

func langExit(code int) int {
	if code < 0 {
		return 1
	}
	return code
}
```

**Step 2: Run the package tests**

Run: `gofmt -w cmd/sash && go test ./... -count=1`

Expected:

```
?    github.com/afterdarksys/sash/cmd/sash
ok   github.com/afterdarksys/sash/internal/grant
ok   github.com/afterdarksys/sash/internal/lang
?    github.com/afterdarksys/sash/internal/parent
```

**Step 3: Exercise the entry**

Run each command from `/Users/ryan/development/sash`.

```bash
go run ./cmd/sash -c 'echo hi'
```

Expected stdout `hi` and a trailing newline. Exit 0.

```bash
go run ./cmd/sash -c 'ps'
```

Expected stderr `denied ps`. Exit 126.

```bash
printf 'echo a\necho b\n' | go run ./cmd/sash
```

Expected stdout:

```
a
b
```

Exit 0.

```bash
go run ./cmd/sash </dev/null
```

Expected empty stdout and empty stderr. Exit 0. No arguments means "read stdin as one script." An empty script is success.

```bash
go run ./cmd/sash a b
```

Expected stderr `usage: sash [-c script | file]`. Exit 2. Two arguments that are not `-c` plus a script are a usage error.

**Step 4: Commit**

```bash
git add cmd/sash
git commit -m "feat: run sash -c, a file, or stdin as one script"
```

---

## Done

`go test ./... -count=1` is ok for `internal/grant` and `internal/lang`. The four manual commands in task 6 match the expected output. `git log --format=%B` shows five commits after the docs commit, and none of the messages contain `Co-Authored-By`.

`grep -R 'ed25519\|crypto/\|adssh\|readline' --include='*.go' .` prints nothing.

Stop. Commit 2 is the signed grant pipe. It is not part of this plan.

## Next action

When Ryan says to build, start at "Before task 1". Until then, leave the tree as docs only.
