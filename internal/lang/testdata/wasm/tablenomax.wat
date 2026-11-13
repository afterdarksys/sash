(module
  (table 1 funcref)  ;; no max: refused, wasm_limits
  (memory (export "memory") 1 256)
  (func (export "sash_alloc") (param i32) (result i32) i32.const 1024)
  (func (export "grow") (param i32 i32) (result i64)
    ref.null func i32.const 100000000 table.grow 0
    i32.const -1 i32.eq (if (then unreachable))
    i64.const 0))
