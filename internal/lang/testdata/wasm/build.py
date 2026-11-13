#!/usr/bin/env python3
"""Hand-assembles the test modules in this directory.

No wat2wasm is installed here, so each module is written byte by byte
from the WebAssembly 2.0 binary format; the .wat next to each .wasm is the
same module in text form, for review. Run: python3 build.py
"""

def uleb(n):
    out = bytearray()
    while True:
        b = n & 0x7F
        n >>= 7
        if n:
            out.append(b | 0x80)
        else:
            out.append(b)
            return bytes(out)

def sleb(n):
    out = bytearray()
    while True:
        b = n & 0x7F
        n >>= 7
        if (n == 0 and not b & 0x40) or (n == -1 and b & 0x40):
            out.append(b)
            return bytes(out)
        out.append(b | 0x80)

def vec(items):
    return uleb(len(items)) + b"".join(items)

def name(s):
    return uleb(len(s)) + s.encode()

def section(sid, body):
    return bytes([sid]) + uleb(len(body)) + body

I32, I64 = 0x7F, 0x7E

def functype(params, results):
    return b"\x60" + vec([bytes([p]) for p in params]) + vec([bytes([r]) for r in results])

def body(code):
    b = vec([]) + code + b"\x0b"
    return uleb(len(b)) + b

def i32(n): return b"\x41" + sleb(n)
def i64(n): return b"\x42" + sleb(n)
def lget(i): return b"\x20" + uleb(i)

# Types: 0 (i32)->i32 sash_alloc, 1 (i32,i32)->i64 a call, 2 wasi fd_write.
TYPES = section(1, vec([functype([I32], [I32]), functype([I32, I32], [I64]), functype([I32] * 4, [I32])]))
MEMORY = section(5, vec([b"\x00" + uleb(1)]))  # min 1 page, no max
ALLOC = i32(1024)  # a fixed buffer: the tests pass small arguments
PACK = lget(0) + b"\xad" + i64(32) + b"\x86" + lget(1) + b"\xad" + b"\x84"  # ptr<<32 | len

def module(funcs, imports=b"", nimports=0, data=b"", tables=b""):
    """funcs: list of (export name, type index, code)."""
    out = b"\x00asm\x01\x00\x00\x00" + TYPES
    if imports:
        out += section(2, imports)
    out += section(3, vec([uleb(t) for _, t, _ in funcs]))
    if tables:
        out += section(4, tables)
    out += MEMORY
    exports = [name("memory") + b"\x02" + uleb(0)]
    for i, (n, _, _) in enumerate(funcs):
        exports.append(name(n) + b"\x00" + uleb(nimports + i))
    out += section(7, vec(exports))
    out += section(10, vec([body(c) for _, _, c in funcs]))
    if data:
        out += section(11, data)
    return out

def seg(offset, text):
    return b"\x00" + i32(offset) + b"\x0b" + vec([bytes([c]) for c in text])

modules = {
    "echo": module([("sash_alloc", 0, ALLOC), ("echo", 1, PACK)]),
    "loop": module([("sash_alloc", 0, ALLOC), ("spin", 1, b"\x03\x40\x0c\x00\x0b\x00")]),
    "grow": module([("sash_alloc", 0, ALLOC),
                    ("grow", 1, i32(300) + b"\x40\x00" + i32(-1) + b"\x46" + b"\x04\x40\x00\x0b" + i64(0))]),
    "wasi": module([("sash_alloc", 0, ALLOC), ("echo", 1, PACK)],
                   imports=vec([name("wasi_snapshot_preview1") + name("fd_write") + b"\x00" + uleb(2)]), nimports=1),
    "huge": module([("sash_alloc", 0, ALLOC), ("huge", 1, i64(9 * 1024 * 1024)), ("oob", 1, i64(100000))]),
    "data": module([("sash_alloc", 0, ALLOC),
                    ("bad", 1, i64((2048 << 32) | 5)),
                    ("nan", 1, i64((2100 << 32) | 3)),
                    ("notutf8", 1, i64((2200 << 32) | 3))],
                   data=vec([seg(2048, b"{oops"), seg(2100, b"NaN"), seg(2200, b'"\xff"')])),
    "noabi": module([("echo", 1, PACK)]),
    # funcref tables: 0x70, then limits 0x00 min or 0x01 min max.
    "table": module([("sash_alloc", 0, ALLOC), ("echo", 1, PACK)], tables=vec([b"\x70\x00" + uleb(10)])),
    "bigtable": module([("sash_alloc", 0, ALLOC), ("echo", 1, PACK)], tables=vec([b"\x70\x00" + uleb(100_000_000)])),
    "tablemax": module([("sash_alloc", 0, ALLOC), ("echo", 1, PACK)], tables=vec([b"\x70\x01" + uleb(1) + uleb(20000)])),
    # A custom section whose size is 2^64-11 in a full 10-byte LEB128:
    # the size must be checked in 64 bits against what is left.
    "overflow": b"\x00asm\x01\x00\x00\x00" + b"\x00" + uleb(2**64 - 11) + b"\x00" * 16,
}

if __name__ == "__main__":
    import hashlib, os
    here = os.path.dirname(os.path.abspath(__file__))
    for n, b in sorted(modules.items()):
        with open(os.path.join(here, n + ".wasm"), "wb") as f:
            f.write(b)
        print(n, len(b), hashlib.sha256(b).hexdigest())
