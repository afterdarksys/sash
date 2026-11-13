(module
  ;; sash_alloc, echo, and 9,999 more: 10,001 functions, refused, wasm_limits.
  (memory (export "memory") 1 256)
  (func (export "sash_alloc") (param i32) (result i32) i32.const 1024)
  (func (export "echo") (param i32 i32) (result i64) ...)
  (func (export "f0") (param i32 i32) (result i64) i64.const 0)
  ;; ... through f9998
)
