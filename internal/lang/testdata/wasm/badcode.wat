(module
  ;; Passes sash's own checks; wazero refuses it (i32 where i64 is due).
  (memory (export "memory") 1 256)
  (func (export "sash_alloc") (param i32) (result i32) i32.const 1024)
  (func (export "echo") (param i32 i32) (result i64) i32.const 0))
