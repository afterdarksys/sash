package lang

// WASM functions (docs/plans/2026-11-12-sash-v6-tier3.md §1): the parts
// every build shares. The statement `wasm m = <path> sha256 <hex>` and
// the call `[ m::fn(args) ]` parse everywhere, so needs, why, lint, and
// the preview see them in any build; only a build with the sashwasm tag
// runs them (wasm_on.go). Without it, a wasm statement or call is exit 2
// wasm_unsupported.
//
// Threats: a module is bytes pinned by hash, read only through file.read
// at machine.do, at most 256 KiB, and checked before it is compiled. It
// runs in wazero's interpreter (no generated native code) with no imports
// at all: no WASI, no host functions, so it reaches no clock, randomness,
// filesystem, network, or verb, and any module that imports anything is
// refused before it is instantiated. Each call gets a fresh instance, a
// 16 MiB memory cap, the earlier of 1 s and the enclosing timeout, and
// counts against verbmax. A secret is never passed in (its trap and run
// time would be a side channel). Nothing a module returns is parsed as
// source or used as a command name: it is one JSON value.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/afterdarksys/sash/internal/parent"
)

func parentVerbRead(full string) parent.Verb {
	return parent.Verb{Key: "file.read", Argv: []string{"cat", full}, Path: full}
}

// wasmMemoryPagesCap is the memory cap: 256 pages, 16 MiB per instance.
// A module must declare a max at or under it, and the runtime enforces it
// too.
const wasmMemoryPagesCap = 256

const (
	wasmCallMax = time.Second
	// wasmModuleMax caps a module at 256 KiB, so the worst compile stays
	// under about 256 MB: wazero's interpreter allocates up to about 633
	// bytes per module byte (nested ifs; loops 588, pushes 246, br_table
	// 37), 158 MB at this cap, plus up to 23 MB for locals at their cap.
	wasmModuleMax = 256 << 10
	wasmResultMax = 8 << 20
	noteWasm      = "wasm"
)

var (
	errWasmUnsupported = errors.New("wasm_unsupported")
	errWasmSecret      = errors.New("wasm_secret")
	errWasmTrap        = errors.New("wasm_trap")
	wasmNameRe         = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	wasmHashRe         = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Wasm is `wasm Name = Path sha256 Hash`. Path and Hash are literals.
type Wasm struct {
	Name string
	Path Word
	Hash string
}

func (*Wasm) stmt() {}

// exprWasmCall is m::fn(args) in an expression: both names literal.
type exprWasmCall struct {
	mod, fn string
	args    []Expr
}

func (exprWasmCall) expr() {}

// wasmModule is a loaded module: its name, path, hash, and, in a build
// with the tag, the compiled module.
type wasmModule struct {
	name, path string
	hash       [32]byte
	impl       any
}

// parseWasm reads `wasm m = /path sha256 <hex>`. A path or hash from a
// value is refused here, as a command name from a value is.
func (p *parser) parseWasm() (Stmt, error) {
	p.i++
	if p.peek().kind != kWord || !wasmNameRe.MatchString(plain(p.peek().word)) {
		return nil, fmt.Errorf("parse: wasm needs a name")
	}
	w := &Wasm{Name: plain(p.peek().word)}
	p.i++
	if p.peek().kind != kEq {
		return nil, fmt.Errorf("parse: wasm needs =")
	}
	p.i++
	if p.peek().kind != kWord || plain(p.peek().word) == "" {
		return nil, fmt.Errorf("parse: wasm needs a literal path")
	}
	w.Path = p.peek().word
	p.i++
	if p.peek().kind != kWord || plain(p.peek().word) != "sha256" {
		return nil, fmt.Errorf("parse: wasm needs sha256 <hex>")
	}
	p.i++
	if p.peek().kind != kWord || !wasmHashRe.MatchString(plain(p.peek().word)) {
		return nil, fmt.Errorf("parse: wasm needs sha256 <64 lowercase hex>")
	}
	w.Hash = plain(p.peek().word)
	p.i++
	return w, nil
}

// wasmCallAt reports whether the tokens at p.i are id :: id (: an export
// call), and parses it.
func (p *exprParser) wasmCall(mod etok) (Expr, bool, error) {
	if p.i+2 >= len(p.t) {
		return nil, false, nil
	}
	c1, c2, fn := p.t[p.i], p.t[p.i+1], p.t[p.i+2]
	if c1.k != "op" || c1.s != ":" || c2.k != "op" || c2.s != ":" || fn.k != "id" ||
		c1.pos != mod.pos+len(mod.s) || c2.pos != c1.pos+1 || fn.pos != c2.pos+1 {
		return nil, false, nil
	}
	p.i += 3
	if !p.isOp("(") {
		return nil, true, fmt.Errorf("type expr wasm call needs (")
	}
	p.i++
	if err := p.enter(); err != nil {
		return nil, true, err
	}
	defer func() { p.depth-- }()
	var args []Expr
	for !p.isOp(")") {
		a, err := p.or()
		if err != nil {
			return nil, true, err
		}
		args = append(args, a)
		if p.isOp(",") {
			p.i++
			continue
		}
		if !p.isOp(")") {
			return nil, true, fmt.Errorf("type expr expected )")
		}
	}
	p.i++
	return exprWasmCall{mod: mod.s, fn: fn.s, args: args}, true, nil
}

// evalWasmCall evaluates the arguments and hands them to the run's hook.
// A secret anywhere in the evaluation so far is refused before the call.
func evalWasmCall(x exprWasmCall, lookup func(string) (any, bool), c *evalCtx) (any, error) {
	args := make([]any, len(x.args))
	for i, a := range x.args {
		d, err := evalNode(a, lookup, c)
		if err != nil {
			return nil, err
		}
		args[i] = d
	}
	if c.tainted {
		return nil, errWasmSecret
	}
	if c.wasm == nil {
		return nil, errWasmUnsupported
	}
	return c.wasm(x.mod, x.fn, args, c)
}

// wasmInput is the arguments as one JSON array, by the ordered encoder.
func wasmInput(args []any) ([]byte, error) {
	b, err := encodeJSONIndent(args)
	if err != nil {
		return nil, err
	}
	return bytes.TrimSpace(b), nil
}

// wasmOutput reads a result: at most 8 MiB, UTF-8, one JSON value (which
// has no NaN or Inf), charged before it is built.
func wasmOutput(b []byte, c *evalCtx) (any, error) {
	if len(b) > wasmResultMax {
		return nil, fmt.Errorf("type wasm result too large")
	}
	if err := charge(c, len(b)); err != nil {
		return nil, err
	}
	if !utf8.Valid(b) {
		return nil, fmt.Errorf("type wasm result utf8")
	}
	d, err := decodeJSON(b)
	if err != nil {
		return nil, fmt.Errorf("type wasm result json")
	}
	return d, nil
}

// wasmLoadChecks are the build-independent checks on loaded bytes: size
// and hash, before anything compiles.
func wasmLoadChecks(body []byte, want string) ([32]byte, error) {
	if len(body) > wasmModuleMax {
		return [32]byte{}, errors.New("wasm_too_large")
	}
	h := sha256.Sum256(body)
	if hex.EncodeToString(h[:]) != want {
		return h, errors.New("wasm_hash")
	}
	return h, nil
}

// wasmTableMax caps every table's min and max. wazero has no table
// limit (only a memory one) and allocates a table's min entries on every
// instantiation, about 8 bytes each: a min of 100M was 763 MB per call.
// 10,000 entries is 80 KB, well past the indirect-call tables of real
// compiled modules (Rust and C emit tens to a few thousand).
const wasmTableMax = 10000

var errWasmBad = errors.New("wasm_compile")

// wasmSections walks a module's sections, handing f each id and body. All
// size math is in uint64 against what is left, a LEB128 is at most 10
// bytes, and the index strictly increases, so it ends on any input.
func wasmSections(b []byte, f func(id byte, body []byte) error) error {
	if len(b) < 8 || !bytes.Equal(b[:4], []byte{0, 'a', 's', 'm'}) {
		return errWasmBad
	}
	i := 8
	for i < len(b) {
		start := i
		id := b[i]
		i++
		size, n := uleb(b[i:])
		if n == 0 {
			return errWasmBad
		}
		i += n
		if size > uint64(len(b)-i) {
			return errWasmBad
		}
		body := b[i : i+int(size)]
		i += int(size)
		if i <= start {
			return errWasmBad
		}
		if err := f(id, body); err != nil {
			return err
		}
	}
	return nil
}

// Per-entry costs measured in wazero v1.12.0's interpreter (i9, darwin);
// a function's params count as its locals (a 3M-param type allocated
// 20.7 GB):
// a function costs about 256 bytes and 0.5 us to compile (500,000 took
// 128 MB and 263 ms), and a declared local about 400 bytes (one function
// with 50,000 locals took 20 MB and 21 ms; 100 such took 322 MB). A 4 MiB
// module could otherwise declare a million functions or 100M locals (43 GB
// and 4m39s). The caps keep one compile near 40 MB and 50 ms, far above
// what small compiled helper modules declare.
const (
	wasmParamsMax      = 1000 // params, and results, per function type
	wasmFuncMax        = 10000
	wasmLocalsPerFunc  = 50000
	wasmLocalsPerMod   = 100000
	wasmMemoryMaxPages = wasmMemoryPagesCap
)

// wasmCheckModule is what sash refuses before wazero sees the bytes: any
// import of any kind (function, table, memory, global); a table or memory
// without a declared max, or past its cap (tables 10,000 entries, memory
// 256 pages), so table.grow and memory.grow stop there; more than 10,000
// functions; and more locals than the caps, summed with overflow checks.
func wasmCheckModule(b []byte) error {
	limits := errors.New("wasm_limits")
	var locals uint64
	var typeParams []uint64 // params per type index
	var funcTypes []uint64  // type index per function
	return wasmSections(b, func(id byte, body []byte) error {
		count, n := uleb(body)
		switch id {
		case 1, 2, 3, 4, 5, 10:
			if n == 0 {
				return errWasmBad
			}
		}
		p := body[n:]
		// limitsMax reads limits that must be 0x01 min max with
		// min <= max <= cap.
		limitsMax := func(cap uint64) error {
			if len(p) < 1 || p[0] != 0x01 {
				if len(p) >= 1 && p[0] == 0x00 {
					return limits
				}
				return errWasmBad
			}
			p = p[1:]
			min, a := uleb(p)
			if a == 0 {
				return errWasmBad
			}
			p = p[a:]
			max, c := uleb(p)
			if c == 0 {
				return errWasmBad
			}
			p = p[c:]
			if max > cap || min > max {
				return limits
			}
			return nil
		}
		// vecLen reads a vector of one-byte value types: at most
		// wasmParamsMax, and all present.
		vecLen := func() (uint64, error) {
			v, a := uleb(p)
			if a == 0 {
				return 0, errWasmBad
			}
			if v > wasmParamsMax {
				return 0, limits
			}
			if v > uint64(len(p)-a) {
				return 0, errWasmBad
			}
			p = p[a+int(v):]
			return v, nil
		}
		switch id {
		case 1:
			if count > wasmFuncMax {
				return limits
			}
			for k := uint64(0); k < count; k++ {
				if len(p) < 1 || p[0] != 0x60 {
					return errWasmBad
				}
				p = p[1:]
				params, err := vecLen()
				if err != nil {
					return err
				}
				if _, err := vecLen(); err != nil {
					return err
				}
				typeParams = append(typeParams, params)
			}
		case 2:
			if count > 0 {
				return errors.New("wasm_import")
			}
		case 3:
			if count > wasmFuncMax {
				return limits
			}
			for k := uint64(0); k < count; k++ {
				ti, a := uleb(p)
				if a == 0 || ti >= uint64(len(typeParams)) {
					return errWasmBad
				}
				p = p[a:]
				funcTypes = append(funcTypes, ti)
			}
		case 4:
			for k := uint64(0); k < count; k++ {
				if len(p) < 1 {
					return errWasmBad
				}
				p = p[1:] // reftype
				if err := limitsMax(wasmTableMax); err != nil {
					return err
				}
			}
		case 5:
			for k := uint64(0); k < count; k++ {
				if err := limitsMax(wasmMemoryMaxPages); err != nil {
					return err
				}
			}
		case 10:
			if count > wasmFuncMax {
				return limits
			}
			for k := uint64(0); k < count; k++ {
				size, a := uleb(p)
				if a == 0 || size > uint64(len(p)-a) {
					return errWasmBad
				}
				fn := p[a : a+int(size)]
				p = p[a+int(size):]
				decls, c := uleb(fn)
				if c == 0 {
					return errWasmBad
				}
				q := fn[c:]
				// A function's params are locals too.
				if k >= uint64(len(funcTypes)) {
					return errWasmBad
				}
				mine := typeParams[funcTypes[k]]
				for d := uint64(0); d < decls; d++ {
					v, e := uleb(q)
					if e == 0 || len(q) < e+1 {
						return errWasmBad
					}
					q = q[e+1:] // the count, then the value type
					if v > wasmLocalsPerFunc || mine+v > wasmLocalsPerFunc {
						return limits
					}
					mine += v
				}
				if locals+mine > wasmLocalsPerMod {
					return limits
				}
				locals += mine
			}
		}
		return nil
	})
}

// uleb reads an unsigned LEB128 of at most 10 bytes that fits in 64 bits;
// n is 0 when it does not.
func uleb(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		c := b[i]
		if i == 9 && c > 1 {
			return 0, 0
		}
		v |= uint64(c&0x7f) << (7 * uint(i))
		if c&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}

// runWasm is the wasm statement: read the module through the door, check
// size and hash, then load it (wasm_on.go) or refuse (wasm_off.go).
func (m *machine) runWasm(s *Wasm) (int, bool) {
	fail := func(status int, msg string) (int, bool) {
		m.err.WriteString(msg + "\n")
		return status, true
	}
	if !wasmBuilt {
		return fail(exitType, errWasmUnsupported.Error())
	}
	if !m.g.Has("file.read") {
		return fail(exitDenied, "denied file.read")
	}
	full, err := resolvePath(m.prefix(), plain(s.Path))
	if err != nil {
		return fail(exitType, "prefix_unset")
	}
	if !m.g.AllowsRead(full) {
		return fail(exitDenied, "denied file.read")
	}
	res, err := m.do(parentVerbRead(full))
	if err != nil || res.Exit != 0 {
		return fail(1, "wasm: cannot read")
	}
	h, err := wasmLoadChecks(res.Bytes, s.Hash)
	if err != nil {
		return fail(exitType, err.Error())
	}
	impl, err := wasmCompile(res.Bytes, h)
	if err != nil {
		return fail(exitType, err.Error())
	}
	if m.st.wasmMods == nil {
		m.st.wasmMods = map[string]*wasmModule{}
	}
	m.st.wasmMods[s.Name] = &wasmModule{name: s.Name, path: full, hash: h, impl: impl}
	if m.tape != nil {
		if err := m.tape.module(s.Name, hex.EncodeToString(h[:])); err != nil {
			m.ctl = ctlHalt
			return exitType, true
		}
	}
	return 0, false
}

// wasmHook is the evaluation hook a machine gives evalCtx: it finds the
// module, counts the call against verbmax, and runs it with a deadline.
func (m *machine) wasmHook() func(mod, fn string, args []any, c *evalCtx) (any, error) {
	return func(mod, fn string, args []any, c *evalCtx) (any, error) {
		if !wasmBuilt {
			return nil, errWasmUnsupported
		}
		w, ok := m.st.wasmMods[mod]
		if !ok {
			return nil, fmt.Errorf("unset wasm %s", mod)
		}
		if strings.HasPrefix(fn, "sash_") {
			return nil, fmt.Errorf("usage wasm %s::%s", mod, fn)
		}
		if m.verbs >= m.st.verbMax {
			m.ctl = ctlHalt
			return nil, errVerbMax
		}
		m.verbs++
		deadline := m.now().Add(wasmCallMax)
		if !m.deadline.IsZero() && m.deadline.Before(deadline) {
			deadline = m.deadline
		}
		in, err := wasmInput(args)
		if err != nil {
			return nil, err
		}
		out, err := wasmRun(w, fn, in, deadline)
		if err != nil {
			return nil, err
		}
		d, err := wasmOutput(out, c)
		if err != nil {
			return nil, err
		}
		id := m.st.addOrigin(origin{src: m.provSrcName(), line: m.provLine(), key: "wasm", target: needsCell(mod + "::" + fn), hash: w.hash})
		m.prov = addProv(m.prov, id)
		return d, nil
	}
}
