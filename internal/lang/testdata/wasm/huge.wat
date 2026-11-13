(module
  (memory (export "memory") 1)
  (func (export "sash_alloc") (param i32) (result i32) i32.const 1024)
  ;; A 9 MiB result: past the 8 MiB cap, refused before it is read.
  (func (export "huge") (param i32 i32) (result i64) i64.const 9437184)
  ;; 100000 bytes from 0 in a 64 KiB memory: out of bounds, a trap.
  (func (export "oob") (param i32 i32) (result i64) i64.const 100000))
