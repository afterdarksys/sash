(module
  (memory (export "memory") 1 256)
  (func (export "sash_alloc") (param i32) (result i32) i32.const 1024)
  (func (export "bad") (param i32 i32) (result i64) i64.const 8796093022213)      ;; 2048<<32 | 5
  (func (export "nan") (param i32 i32) (result i64) i64.const 9019431321603)      ;; 2100<<32 | 3
  (func (export "notutf8") (param i32 i32) (result i64) i64.const 9448928051203)  ;; 2200<<32 | 3
  (data (i32.const 2048) "{oops")
  (data (i32.const 2100) "NaN")
  (data (i32.const 2200) "\22\ff\22"))
