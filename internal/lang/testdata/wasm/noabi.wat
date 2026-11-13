(module
  ;; No sash_alloc: refused with wasm_abi when loaded.
  (memory (export "memory") 1)
  (func (export "echo") (param i32 i32) (result i64)
    local.get 0 i64.extend_i32_u i64.const 32 i64.shl
    local.get 1 i64.extend_i32_u i64.or))
