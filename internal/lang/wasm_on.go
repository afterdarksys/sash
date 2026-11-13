//go:build sashwasm

package lang

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// wasmBuilt is true: this build runs wasm modules.
const wasmBuilt = true

// One runtime and an in-memory compile cache for the process, keyed by
// module hash. Nothing is cached on disk (a host write outside the grant).
var (
	wasmOnce     sync.Once
	wasmRT       wazero.Runtime
	wasmMu       sync.Mutex
	wasmCompiled = map[[32]byte]wazero.CompiledModule{}
)

func wasmRuntime() wazero.Runtime {
	wasmOnce.Do(func() { wasmRT = newWasmRuntime(context.Background()) })
	return wasmRT
}

// wasmCompile compiles the bytes (already size- and hash-checked) and
// refuses a module that imports anything, by wazero's own listing and by
// the import section itself, before any instance exists. It also needs
// the ABI exports: memory and sash_alloc.
func wasmCompile(body []byte, h [32]byte) (any, error) {
	wasmMu.Lock()
	defer wasmMu.Unlock()
	if c, ok := wasmCompiled[h]; ok {
		return c, nil
	}
	if n := wasmImportCount(body); n != 0 {
		if n < 0 {
			return nil, errors.New("wasm_compile")
		}
		return nil, errors.New("wasm_import")
	}
	c, err := wasmRuntime().CompileModule(context.Background(), body)
	if err != nil {
		return nil, errors.New("wasm_compile")
	}
	if len(c.ImportedFunctions()) > 0 || len(c.ImportedMemories()) > 0 {
		if cerr := c.Close(context.Background()); cerr != nil {
			return nil, errors.Join(errors.New("wasm_import"), cerr)
		}
		return nil, errors.New("wasm_import")
	}
	alloc, ok := c.ExportedFunctions()["sash_alloc"]
	_, hasMem := c.ExportedMemories()["memory"]
	if !ok || !hasMem || !sig(alloc, []api.ValueType{api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}) {
		if cerr := c.Close(context.Background()); cerr != nil {
			return nil, errors.Join(errors.New("wasm_abi"), cerr)
		}
		return nil, errors.New("wasm_abi")
	}
	wasmCompiled[h] = c
	return c, nil
}

func sig(f api.FunctionDefinition, params, results []api.ValueType) bool {
	eq := func(a, b []api.ValueType) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	return eq(f.ParamTypes(), params) && eq(f.ResultTypes(), results)
}

// wasmRun calls fn on a fresh instance: in goes to memory from
// sash_alloc, fn(ptr, len) returns ptr<<32|len, and the result is copied
// out before the instance is closed. The context ends at deadline, which
// aborts a running loop. Any trap, timeout, memory-limit error, or
// out-of-bounds result is wasm_trap; a wrong export is wasm_abi.
func wasmRun(w *wasmModule, fn string, in []byte, deadline time.Time) ([]byte, error) {
	c, ok := w.impl.(wazero.CompiledModule)
	if !ok {
		return nil, errWasmUnsupported
	}
	def, ok := c.ExportedFunctions()[fn]
	if !ok || !sig(def, []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}) {
		return nil, errors.New("wasm_abi")
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	// No start function, no name (each call is its own instance), and the
	// default config: no clock, no randomness, no filesystem, stdin EOF.
	inst, err := wasmRuntime().InstantiateModule(ctx, c, wazero.NewModuleConfig().WithStartFunctions().WithName(""))
	if err != nil {
		return nil, errWasmTrap
	}
	out, err := wasmInvoke(ctx, inst, fn, in)
	if cerr := inst.Close(context.Background()); cerr != nil && err == nil {
		err = errWasmTrap
	}
	return out, err
}

func wasmInvoke(ctx context.Context, inst api.Module, fn string, in []byte) ([]byte, error) {
	mem := inst.Memory()
	alloc := inst.ExportedFunction("sash_alloc")
	f := inst.ExportedFunction(fn)
	if mem == nil || alloc == nil || f == nil {
		return nil, errors.New("wasm_abi")
	}
	r, err := alloc.Call(ctx, uint64(len(in)))
	if err != nil || len(r) != 1 {
		return nil, errWasmTrap
	}
	ptr := uint32(r[0])
	if !mem.Write(ptr, in) {
		return nil, errWasmTrap
	}
	r, err = f.Call(ctx, uint64(ptr), uint64(len(in)))
	if err != nil || len(r) != 1 {
		return nil, errWasmTrap
	}
	optr, olen := uint32(r[0]>>32), uint32(r[0])
	if olen > wasmResultMax {
		return nil, errors.New("type wasm result too large")
	}
	b, ok := mem.Read(optr, olen)
	if !ok {
		return nil, errWasmTrap
	}
	return append([]byte(nil), b...), nil
}
