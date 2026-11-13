package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
)

const wasmHash0 = "0000000000000000000000000000000000000000000000000000000000000000"

// The statement parses in every build; a path, name, or hash that is not
// a literal is a parse error, as a command name from a value is.
func TestWasmParse(t *testing.T) {
	for _, src := range []string{
		"wasm m = $p sha256 " + wasmHash0,
		"wasm m = /srv/x.wasm sha256 $h",
		"wasm m = /srv/x.wasm sha256 abc",
		"wasm m = /srv/x.wasm sha256 " + strings.ToUpper("ab"+wasmHash0[2:]),
		"wasm m = /srv/x.wasm md5 " + wasmHash0,
		"wasm $m = /srv/x.wasm sha256 " + wasmHash0,
		"wasm 1m = /srv/x.wasm sha256 " + wasmHash0,
		"wasm m /srv/x.wasm sha256 " + wasmHash0,
		"echo [ m::(1) ]",
		"echo [ m:: f(1) ]",
	} {
		if out := Run(src, grant.Grant{}, &countParent{}, Options{}); out.Status != exitType || !strings.Contains(out.Stderr, "wasm") && !strings.Contains(out.Stderr, "expr") {
			t.Errorf("%q: %+v", src, out)
		}
	}
}

// needs: one file.read need noted wasm, and the manifest stays complete;
// why reports the file.read; lint treats a call as pure and a secret
// into one as a sink.
func TestWasmStatic(t *testing.T) {
	src := "wasm m = /srv/ops/fmt.wasm sha256 " + wasmHash0 + "\nlet y = [ m::render(1) ]"
	out := Needs(src, NeedsOptions{})
	if out.Status != 0 || !strings.Contains(out.Stdout, "file.read  /srv/ops/fmt.wasm") || !strings.Contains(out.Stdout, "wasm") {
		t.Fatalf("needs %+v", out)
	}
	if out := Needs(src, NeedsOptions{Policy: true}); out.Status != 0 || out.Stdout != "--file-read /srv/ops\n" {
		t.Fatalf("policy %+v", out)
	}
	g := grant.Grant{V: "sash/1", Entitlements: []string{"file.read"}, FileRead: []string{"/srv"}}
	if out := Why(src, g, Options{}); !strings.Contains(out.Stdout, "wasm m = /srv/ops/fmt.wasm") || !strings.Contains(out.Stdout, "granted") {
		t.Fatalf("why %+v", out)
	}
	if out := Why(src, grant.Grant{V: "sash/1"}, Options{}); !strings.Contains(out.Stdout, "file.read  no") || !strings.Contains(out.Stdout, "not_in_grant") {
		t.Fatalf("why denied %+v", out)
	}
	if pv := Preview(src, grant.Grant{V: "sash/1"}, Options{}); !pv.Block || pv.Smile != "denied file.read" {
		t.Fatalf("preview %+v", pv)
	}
	if out := Lint(src + "\necho $y"); out.Status != 0 {
		t.Fatalf("lint %+v", out)
	}
	rows := lintRows(t, Lint(src+"\ndeclare -s tok = abc\nlet z = [ m::render($tok) ]"))
	if !hasRule(rows, "secret-to-sink", "error") {
		t.Fatalf("lint secret %+v", rows)
	}
}
