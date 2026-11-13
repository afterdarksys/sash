package lang

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func wasmFile(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "wasm", name+".wasm"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// checkEnds runs wasmCheckModule and fails if it does not end at once.
func checkEnds(t testing.TB, b []byte) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- wasmCheckModule(b) }()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatalf("section walk did not end on % x", b[:min(len(b), 32)])
		return nil
	}
}

// The section walker ends on any input: a size of 2^64-11 is refused, not
// wrapped into an index. Imports and oversized tables are refused before
// wazero sees the bytes.
func TestWasmCheckModule(t *testing.T) {
	for name, want := range map[string]string{
		"echo": "", "table": "", "overflow": "wasm_compile", "bigtable": "wasm_limits",
		"tablemax": "wasm_limits", "wasi": "wasm_import", "locals": "wasm_limits", "locals2": "wasm_limits", "locals3": "wasm_limits",
		"params": "wasm_limits", "paramslocals": "wasm_limits", "manyparams": "wasm_limits",
		"manyfuncs": "wasm_limits", "tablenomax": "wasm_limits", "memnomax": "wasm_limits",
		"tablegrow": "", "badcode": "", "grow": "", "huge": "", "data": "", "loop": "", "noabi": "",
	} {
		err := checkEnds(t, wasmFile(t, name))
		if (want == "" && err != nil) || (want != "" && (err == nil || err.Error() != want)) {
			t.Errorf("%s: %v want %q", name, err, want)
		}
	}
	// LEB128: at most 10 bytes, and the tenth fits the top bit only.
	for _, c := range []struct {
		b []byte
		n int
	}{
		{[]byte{0x7f}, 1},
		{[]byte{0x80, 0x01}, 2},
		{append(bytes.Repeat([]byte{0xff}, 9), 0x01), 10},
		{append(bytes.Repeat([]byte{0xff}, 9), 0x02), 0},
		{bytes.Repeat([]byte{0x80}, 11), 0},
		{[]byte{0x80}, 0},
		{nil, 0},
	} {
		if _, n := uleb(c.b); n != c.n {
			t.Errorf("% x: %d want %d", c.b, n, c.n)
		}
	}
}

func FuzzWasmSections(f *testing.F) {
	for _, n := range []string{"echo", "data", "wasi", "table", "bigtable", "tablemax", "overflow", "huge", "loop", "grow", "noabi",
		"locals", "locals2", "locals3", "tablegrow", "tablenomax", "memnomax", "badcode", "params", "paramslocals", "manyparams"} {
		f.Add(wasmFile(f, n))
	}
	f.Add([]byte("\x00asm\x01\x00\x00\x00\x04\x05\x01\x70\x01\xff\xff"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<16 {
			return
		}
		checkEnds(t, b)
	})
}
