(module
  (memory (export "memory") 1 256)
  (func (export "sash_alloc") (param i32) (result i32) i32.const 1024)
  ;; 100,000,000 i64 locals in one declaration: refused, wasm_limits.
  (func (export "echo") (param i32 i32) (result i64) (local i64 (; x 100000000 ;))
    local.get 0 i64.extend_i32_u i64.const 32 i64.shl
    local.get 1 i64.extend_i32_u i64.or))
