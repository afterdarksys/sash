(module
  (memory (export "memory") 1 256)
  (func (export "sash_alloc") (param i32) (result i32) i32.const 1024)
  ;; Grows past the 256-page cap; memory.grow returns -1, then it traps.
  (func (export "grow") (param i32 i32) (result i64)
    i32.const 300 memory.grow i32.const -1 i32.eq
    (if (then unreachable))
    i64.const 0))
