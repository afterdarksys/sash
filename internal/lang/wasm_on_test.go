//go:build sashwasm

package lang

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
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

// Tables and section sizes are checked before compile; a table within
// the cap loads and runs.
func TestWasmTablesAndSizes(t *testing.T) {
	for name, want := range map[string]string{"bigtable": "wasm_limits", "tablemax": "wasm_limits", "overflow": "wasm_compile"} {
		out, _ := runWasmSrc(t, load(t, name)+"echo ran")
		if out.Status != exitType || !strings.Contains(out.Stderr, want) || strings.Contains(out.Stdout, "ran") {
			t.Errorf("%s: %+v", name, out)
		}
	}
	if out, _ := runWasmSrc(t, load(t, "table")+"echo [ m::echo(7) ]"); out.Status != 0 || out.Stdout != "[\n  7\n]\n" && out.Stdout != "[7]\n" {
		t.Fatalf("table: %+v", out)
	}
}

// Compiling stops at its deadline: wazero does not check the context
// while compiling, so the compile is abandoned.
func TestWasmCompileDeadline(t *testing.T) {
	old := wasmCompileMax
	defer func() { wasmCompileMax = old }()
	wasmCompileMax = time.Nanosecond
	b, err := os.ReadFile(filepath.Join("testdata", "wasm", "data.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := wasmCompileTimed(b); err == nil || err.Error() != "wasm_compile" || time.Since(start) > time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
}

// Locals, functions, and tables or memories without a declared max are
// refused before compile; table.grow past a declared max fails inside
// wazero and the module traps, without allocating the growth.
func TestWasmCaps(t *testing.T) {
	for name, want := range map[string]string{
		"locals": "wasm_limits", "locals2": "wasm_limits", "manyfuncs": "wasm_limits",
		"tablenomax": "wasm_limits", "memnomax": "wasm_limits",
	} {
		start := time.Now()
		out, _ := runWasmSrc(t, load(t, name)+"echo ran")
		if out.Status != exitType || !strings.Contains(out.Stderr, want) || strings.Contains(out.Stdout, "ran") || time.Since(start) > time.Second {
			t.Errorf("%s: %+v in %v", name, out, time.Since(start))
		}
	}
	var ms0, ms1 runtime.MemStats
	runtime.ReadMemStats(&ms0)
	out, _ := runWasmSrc(t, load(t, "tablegrow")+"echo [ m::grow(1) ]")
	runtime.ReadMemStats(&ms1)
	if out.Status != exitType || !strings.Contains(out.Stderr, "wasm_trap") || ms1.TotalAlloc-ms0.TotalAlloc > 64<<20 {
		t.Fatalf("table.grow: %+v, allocated %d MB", out, (ms1.TotalAlloc-ms0.TotalAlloc)>>20)
	}
}

// A refused module is remembered by hash: loading it again does not
// compile it again.
func TestWasmNegativeCache(t *testing.T) {
	compiles := 0
	old := wasmCompileHook
	defer func() { wasmCompileHook = old }()
	wasmCompileHook = func() { compiles++ }
	for i := 0; i < 3; i++ {
		out, _ := runWasmSrc(t, load(t, "badcode")+"echo ran")
		if out.Status != exitType || !strings.Contains(out.Stderr, "wasm_compile") {
			t.Fatalf("%d: %+v", i, out)
		}
	}
	if compiles != 1 {
		t.Fatalf("compiled %d times", compiles)
	}
}

// One compile in flight per process, abandoned ones included: a second is
// wasm_busy at once, not queued.
func TestWasmBusy(t *testing.T) {
	oldHook, oldMax := wasmCompileHook, wasmCompileMax
	defer func() { wasmCompileHook, wasmCompileMax = oldHook, oldMax }()
	release := make(chan struct{})
	wasmCompileHook = func() { <-release }
	wasmCompileMax = 50 * time.Millisecond
	// The caches are per process and per hash: start these two modules
	// uncompiled, and leave them so for the other tests.
	forget := func() {
		wasmMu.Lock()
		defer wasmMu.Unlock()
		for _, n := range []string{"tablegrow", "huge"} {
			h := sha256.Sum256(mustRead(t, n))
			delete(wasmCompiled, h)
			delete(wasmFailed, h)
		}
	}
	forget()
	defer forget()
	src1 := "wasm a = /srv/wasm/tablegrow.wasm sha256 " + modHash(t, "tablegrow") + "\necho ran"
	if out, _ := runWasmSrc(t, src1); !strings.Contains(out.Stderr, "wasm_compile") {
		t.Fatalf("slow compile not abandoned: %+v", out)
	}
	start := time.Now()
	src2 := "wasm b = /srv/wasm/huge.wasm sha256 " + modHash(t, "huge") + "\necho ran"
	if out, _ := runWasmSrc(t, src2); !strings.Contains(out.Stderr, "wasm_busy") || time.Since(start) > 40*time.Millisecond {
		t.Fatalf("not busy: %+v after %v", out, time.Since(start))
	}
	close(release)
	for i := 0; wasmCompiling.Load() && i < 200; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	wasmCompileHook = nil
	// Busy was not remembered: the module loads once the compile is done.
	if out, _ := runWasmSrc(t, src2); out.Status != 0 {
		t.Fatalf("after: %+v", out)
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "wasm", name+".wasm"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A module past 256 KiB is refused before it is hashed or compiled.
func TestWasmModuleSizeCap(t *testing.T) {
	out, _ := runWasmSrc(t, load(t, "big")+"echo ran")
	if out.Status != exitType || !strings.Contains(out.Stderr, "wasm_too_large") {
		t.Fatalf("%+v", out)
	}
}

// A compile that ran out of time is not remembered: the same module loads
// once a compile can finish.
func TestWasmDeadlineNotCached(t *testing.T) {
	oldHook, oldMax := wasmCompileHook, wasmCompileMax
	defer func() { wasmCompileHook, wasmCompileMax = oldHook, oldMax }()
	h := sha256.Sum256(mustRead(t, "loop"))
	forget := func() {
		wasmMu.Lock()
		defer wasmMu.Unlock()
		delete(wasmCompiled, h)
		delete(wasmFailed, h)
	}
	forget()
	defer forget()
	release := make(chan struct{})
	wasmCompileHook = func() { <-release }
	wasmCompileMax = 30 * time.Millisecond
	if out, _ := runWasmSrc(t, load(t, "loop")+"echo ran"); !strings.Contains(out.Stderr, "wasm_compile") {
		t.Fatalf("no deadline: %+v", out)
	}
	close(release)
	for i := 0; wasmCompiling.Load() && i < 200; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	wasmCompileHook, wasmCompileMax = nil, oldMax
	if out, _ := runWasmSrc(t, load(t, "loop")+"echo ran"); out.Status != 0 || out.Stdout != "ran\n" {
		t.Fatalf("the deadline was cached: %+v", out)
	}
}

// The worst compile measured (nested ifs, about 633 bytes allocated per
// module byte) at the 256 KiB cap stays under 256 MB.
func TestWasmCompileAllocBudget(t *testing.T) {
	uleb := func(n uint64) []byte {
		var out []byte
		for {
			b := byte(n & 0x7f)
			n >>= 7
			if n == 0 {
				return append(out, b)
			}
			out = append(out, b|0x80)
		}
	}
	sec := func(id byte, body []byte) []byte {
		return append(append([]byte{id}, uleb(uint64(len(body)))...), body...)
	}
	k := (wasmModuleMax - 64) / 5
	body := []byte{0}
	for i := 0; i < k; i++ {
		body = append(body, 0x41, 0, 0x04, 0x40)
	}
	for i := 0; i < k; i++ {
		body = append(body, 0x0b)
	}
	body = append(body, 0x0b)
	m := []byte("\x00asm\x01\x00\x00\x00")
	m = append(m, sec(1, []byte{1, 0x60, 0, 0})...)
	m = append(m, sec(3, []byte{1, 0})...)
	m = append(m, sec(10, append(append([]byte{1}, uleb(uint64(len(body)))...), body...))...)
	if len(m) > wasmModuleMax {
		t.Fatalf("%d bytes", len(m))
	}
	var ms0, ms1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms0)
	c, err := wasmRuntime().CompileModule(context.Background(), m)
	runtime.ReadMemStats(&ms1)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := ms1.TotalAlloc - ms0.TotalAlloc; got > 256<<20 {
		t.Fatalf("compile allocated %d MB", got>>20)
	} else {
		t.Logf("%d bytes: %d MB", len(m), got>>20)
	}
}

// Recursion at the frame caps to wazero's 2,000-frame ceiling allocates
// under 256 MB and traps within the deadline; the old 49,000-locals bomb
// and a frame one slot over are refused at load; SIMD is off.
func TestWasmRunAllocBudget(t *testing.T) {
	var ms0, ms1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms0)
	start := time.Now()
	out, _ := runWasmSrc(t, load(t, "deep")+"echo [ m::deep(1) ]")
	took := time.Since(start)
	runtime.ReadMemStats(&ms1)
	alloc := ms1.TotalAlloc - ms0.TotalAlloc
	if out.Status != exitType || !strings.Contains(out.Stderr, "wasm_trap") || alloc > 256<<20 || took > wasmCallMax+500*time.Millisecond {
		t.Fatalf("%+v: %d MB in %v", out, alloc>>20, took)
	}
	t.Logf("deep recursion at the caps: %d MB in %v", alloc>>20, took)
	for name, want := range map[string]string{"frame": "wasm_limits", "deep49k": "wasm_limits", "results2": "wasm_limits", "simd": "wasm_limits"} {
		if out, _ := runWasmSrc(t, load(t, name)+"echo ran"); out.Status != exitType || !strings.Contains(out.Stderr, want) {
			t.Errorf("%s: %+v", name, out)
		}
	}
}
