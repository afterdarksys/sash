;; Not expressible as text: the header, then a custom section (id 0) whose
;; size is 2^64-11 written as a full 10-byte LEB128, then 16 zero bytes.
;; See build.py. The size must be refused, never wrapped into an index.
