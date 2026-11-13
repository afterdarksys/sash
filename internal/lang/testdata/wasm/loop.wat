(module
  (memory (export "memory") 1 256)
  (func (export "sash_alloc") (param i32) (result i32) i32.const 1024)
  ;; Never returns: only the context deadline stops it.
  (func (export "spin") (param i32 i32) (result i64)
    (loop br 0)
    unreachable))
