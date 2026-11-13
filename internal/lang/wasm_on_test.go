//go:build sashwasm

package lang

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/grant"
	"github.com/afterdarksys/sash/internal/parent"
)

// wasmParent serves testdata/wasm/<name>.wasm at /srv/wasm/<name>.wasm,
// through file.read only, and counts every call.
type wasmParent struct {
	t     *testing.T
	calls int
}

func (p *wasmParent) Do(v parent.Verb) (parent.Result, error) {
	p.calls++
	if v.Key != "file.read" || !strings.HasPrefix(v.Path, "/srv/wasm/") {
		return parent.Result{Kind: parent.KindBytes, Exit: 1}, nil
	}
	b, err := os.ReadFile(filepath.Join("testdata", "wasm", strings.TrimPrefix(v.Path, "/srv/wasm/")))
	if err != nil {
		return parent.Result{Kind: parent.KindBytes, Exit: 1}, nil
	}
	return parent.Result{Kind: parent.KindBytes, Bytes: b}, nil
}

func wasmGrant() grant.Grant {
	return grant.Grant{V: "sash/1", Entitlements: []string{"file.read"}, FileRead: []string{"/srv/wasm"}}
}

func modHash(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "wasm", name+".wasm"))
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func load(t *testing.T, name string) string {
	return "wasm m = /srv/wasm/" + name + ".wasm sha256 " + modHash(t, name) + "\n"
}

func runWasmSrc(t *testing.T, src string) (Outcome, *wasmParent) {
	t.Helper()
	p := &wasmParent{t: t}
	return Run(src, wasmGrant(), p, Options{}), p
}

func TestWasmEcho(t *testing.T) {
	out, p := runWasmSrc(t, load(t, "echo")+`let y = [ m::echo("a", 2, {"k": [1, true]}) ]`+"\necho [ $y[2].k[0] ] [ len($y) ]\necho [ m::echo() ]")
	if out.Status != 0 || out.Stdout != "1 3\n[]\n" || p.calls != 1 {
		t.Fatalf("%+v calls %d", out, p.calls)
	}
	// The module is compiled once per process and loads through the door
	// every time it is declared.
	out, p = runWasmSrc(t, load(t, "echo")+load(t, "echo")+"echo [ m::echo(1) ]")
	if out.Status != 0 || p.calls != 2 {
		t.Fatalf("again %+v calls %d", out, p.calls)
	}
}

func TestWasmRefusals(t *testing.T) {
	bad := strings.Repeat("0", 64)
	for name, c := range map[string]struct{ src, want string }{
		"hash mismatch":   {"wasm m = /srv/wasm/echo.wasm sha256 " + bad + "\necho ran", "wasm_hash"},
		"an import":       {load(t, "wasi") + "echo ran", "wasm_import"},
		"no sash_alloc":   {load(t, "noabi") + "echo ran", "wasm_abi"},
		"unknown module":  {"echo [ nope::f(1) ]", "unset wasm nope"},
		"unknown export":  {load(t, "echo") + "echo [ m::nosuch(1) ]", "wasm_abi"},
		"the alloc":       {load(t, "echo") + "echo [ m::sash_alloc(1) ]", "usage wasm"},
		"a loop":          {load(t, "loop") + "echo [ m::spin(1) ]", "wasm_trap"},
		"memory past cap": {load(t, "grow") + "echo [ m::grow(1) ]", "wasm_trap"},
		"huge result":     {load(t, "huge") + "echo [ m::huge(1) ]", "too large"},
		"out of bounds":   {load(t, "huge") + "echo [ m::oob(1) ]", "wasm_trap"},
		"invalid json":    {load(t, "data") + "echo [ m::bad(1) ]", "type wasm result json"},
		"NaN":             {load(t, "data") + "echo [ m::nan(1) ]", "type wasm result json"},
		"not utf-8":       {load(t, "data") + "echo [ m::notutf8(1) ]", "type wasm result utf8"},
		"a secret":        {load(t, "echo") + "declare -s tok = abc\necho [ m::echo($tok) ]", "wasm_secret"},
		"a secret later":  {load(t, "echo") + "declare -s tok = abc\necho [ m::echo(1) + len($tok) ]", ""},
		"outside roots":   {"wasm m = /etc/x.wasm sha256 " + bad, "denied file.read"},
	} {
		start := time.Now()
		out, _ := runWasmSrc(t, c.src)
		if c.want == "" {
			continue
		}
		if out.Status == 0 || !strings.Contains(out.Stderr, c.want) || strings.Contains(out.Stdout, "ran") {
			t.Errorf("%s: %+v", name, out)
		}
		if time.Since(start) > 3*time.Second {
			t.Errorf("%s: took %v", name, time.Since(start))
		}
	}
}

// A call stops at the earlier of 1 s and the enclosing timeout, and counts
// against verbmax.
func TestWasmLimits(t *testing.T) {
	start := time.Now()
	out, _ := runWasmSrc(t, load(t, "loop")+"timeout 200ms { echo [ m::spin(1) ] }")
	if out.Status != exitType || !strings.Contains(out.Stderr, "wasm_trap") || time.Since(start) > 900*time.Millisecond {
		t.Fatalf("timeout: %+v after %v", out, time.Since(start))
	}
	out, _ = runWasmSrc(t, load(t, "echo")+"set verbmax 3\nfor i in [ 1..5 ] { echo [ m::echo($i) ] }")
	if out.Status != exitType || !strings.Contains(out.Stderr, "verb max") || strings.Count(out.Stdout, "\n") != 2 {
		t.Fatalf("verbmax: %+v", out)
	}
}

// A call adds an origin wasm m::fn with the module's hash.
func TestWasmProvenance(t *testing.T) {
	st := NewState()
	p := &wasmParent{t: t}
	Run(load(t, "echo")+"let y = [ m::echo(1) ]", wasmGrant(), p, Options{State: st})
	o := Run("origin y", wasmGrant(), p, Options{State: st})
	if !strings.Contains(o.Stdout, "wasm  m::echo  2  "+modHash(t, "echo")) {
		t.Fatalf("%q", o.Stdout)
	}
}

// A recording names the module's hash in its header and replays.
func TestWasmRecordReplay(t *testing.T) {
	src := load(t, "echo") + "echo [ m::echo(\"x\") ]"
	var buf bytes.Buffer
	tape, err := NewRecorder(&buf, src, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	out1 := Run(src, wasmGrant(), &wasmParent{t: t}, Options{Tape: tape})
	if err := tape.Finish(); err != nil || !bytes.Contains(buf.Bytes(), []byte(`"modules":{"m":"`+modHash(t, "echo")+`"}`)) {
		t.Fatalf("%v\n%s", err, buf.Bytes())
	}
	rt, err := LoadReplay(bytes.NewReader(buf.Bytes()), src, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cp := &countParent{}
	out2 := Run(src, wasmGrant(), cp, Options{Tape: rt})
	if err := rt.Finish(); err != nil || out1.Stdout != out2.Stdout || cp.n != 0 {
		t.Fatalf("%v %+v %+v", err, out1, out2)
	}
}
