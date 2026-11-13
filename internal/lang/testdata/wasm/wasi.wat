(module
  ;; Any import is refused before the module is instantiated.
  (import "wasi_snapshot_preview1" "fd_write" (func (param i32 i32 i32 i32) (result i32)))
  (memory (export "memory") 1 256)
  (func (export "sash_alloc") (param i32) (result i32) i32.const 1024)
  (func (export "echo") (param i32 i32) (result i64)
    local.get 0 i64.extend_i32_u i64.const 32 i64.shl
    local.get 1 i64.extend_i32_u i64.or))
