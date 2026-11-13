//go:build !sashwasm

package lang

import "time"

// wasmBuilt is false: this build has no wasm runtime, and a wasm
// statement or call is exit 2 wasm_unsupported.
const wasmBuilt = false

func wasmCompile([]byte, [32]byte) (any, error) { return nil, errWasmUnsupported }

func wasmRun(*wasmModule, string, []byte, time.Time) ([]byte, error) {
	return nil, errWasmUnsupported
}
