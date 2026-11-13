//go:build sashwasm

package lang

// The wazero runtime for wasm modules (docs/plans/2026-11-12-sash-v6-tier3.md
// §1). Only a build with the sashwasm tag links wazero; a build without
// it refuses wasm with wasm_unsupported. The tag cannot be "wasm": that
// name is the GOARCH=wasm build constraint, and -tags wasm on another
// architecture pulls the standard library's wasm files into the build.

import (
	"context"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// wasmMemoryPages caps one instance's memory: 256 pages is 16 MiB.
const wasmMemoryPages = 256

// newWasmRuntime is the interpreter runtime: no compiler (no generated
// native code), memory capped, calls aborted when their context ends, and
// the core 2.0 features (no threads). It instantiates nothing: no WASI and
// no host module, so a module's imports can never resolve.
func newWasmRuntime(ctx context.Context) wazero.Runtime {
	cfg := wazero.NewRuntimeConfigInterpreter().
		WithMemoryLimitPages(wasmMemoryPages).
		WithCloseOnContextDone(true).
		WithCoreFeatures(api.CoreFeaturesV2)
	return wazero.NewRuntimeWithConfig(ctx, cfg)
}
