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

def body(code, decls=()):
    b = vec([uleb(n) + bytes([t]) for n, t in decls]) + code + b"\x0b"
    return uleb(len(b)) + b

def i32(n): return b"\x41" + sleb(n)
def i64(n): return b"\x42" + sleb(n)
def lget(i): return b"\x20" + uleb(i)

# Types: 0 (i32)->i32 sash_alloc, 1 (i32,i32)->i64 a call, 2 wasi fd_write.
TYPES = section(1, vec([functype([I32], [I32]), functype([I32, I32], [I64]), functype([I32] * 4, [I32])]))
P1000 = functype([I64] * 1000, [I64])  # type 3 where a module adds it
P1001 = functype([I64] * 1001, [I64])
MEMORY = section(5, vec([b"\x01" + uleb(1) + uleb(256)]))  # min 1 page, max 256 (required)
ALLOC = i32(1024)  # a fixed buffer: the tests pass small arguments
PACK = lget(0) + b"\xad" + i64(32) + b"\x86" + lget(1) + b"\xad" + b"\x84"  # ptr<<32 | len

def module(funcs, imports=b"", nimports=0, data=b"", tables=b"", memory=None, locals=None, types=None):
    """funcs: list of (export name, type index, code)."""
    out = b"\x00asm\x01\x00\x00\x00" + (TYPES if types is None else types)
    if imports:
        out += section(2, imports)
    out += section(3, vec([uleb(t) for _, t, _ in funcs]))
    if tables:
        out += section(4, tables)
    out += MEMORY if memory is None else memory
    exports = [name("memory") + b"\x02" + uleb(0)]
    for i, (n, _, _) in enumerate(funcs):
        exports.append(name(n) + b"\x00" + uleb(nimports + i))
    out += section(7, vec(exports))
    locals = locals or {}
    out += section(10, vec([body(c, locals.get(k, ())) for k, (_, _, c) in enumerate(funcs)]))
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
    "table": module([("sash_alloc", 0, ALLOC), ("echo", 1, PACK)], tables=vec([b"\x70\x01" + uleb(10) + uleb(10)])),
    "bigtable": module([("sash_alloc", 0, ALLOC), ("echo", 1, PACK)], tables=vec([b"\x70\x00" + uleb(100_000_000)])),
    "tablemax": module([("sash_alloc", 0, ALLOC), ("echo", 1, PACK)], tables=vec([b"\x70\x01" + uleb(1) + uleb(20000)])),
    # table.grow by 100M on a table declared max 10: wazero refuses the
    # growth (returns -1) and the module traps.
    "tablegrow": module([("sash_alloc", 0, ALLOC),
                         ("grow", 1, b"\xd0\x70" + i32(100_000_000) + b"\xfc\x0f\x00" + i32(-1) + b"\x46" + b"\x04\x40\x00\x0b" + i64(0))],
                        tables=vec([b"\x70\x01" + uleb(1) + uleb(10)])),
    # The same table with no max: refused, table.grow could reach anything.
    "tablenomax": module([("sash_alloc", 0, ALLOC), ("echo", 1, PACK)], tables=vec([b"\x70\x00" + uleb(1)])),
    # A memory with no declared max: refused.
    "memnomax": module([("sash_alloc", 0, ALLOC), ("echo", 1, PACK)], memory=section(5, vec([b"\x00" + uleb(1)]))),
    # One function declaring 100M i64 locals: refused before compile.
    "locals": module([("sash_alloc", 0, ALLOC), ("echo", 1, PACK)], locals={1: [(100_000_000, I64)]}),
    # 50,001 locals over two declarations in one function: past the
    # per-function cap.
    "locals2": module([("sash_alloc", 0, ALLOC), ("echo", 1, PACK)], locals={1: [(25_000, I64), (25_001, I32)]}),
    # A type with 1,001 params: past the per-type cap.
    "params": module([("sash_alloc", 0, ALLOC), ("echo", 1, PACK), ("p", 3, i64(0))],
                     types=section(1, vec([functype([I32], [I32]), functype([I32, I32], [I64]), functype([I32] * 4, [I32]), P1001]))),
    # 1,000 params plus 49,001 locals in one function: params count as
    # locals, so 50,001 is past the per-function cap.
    "paramslocals": module([("sash_alloc", 0, ALLOC), ("echo", 1, PACK), ("p", 3, i64(0))],
                           types=section(1, vec([functype([I32], [I32]), functype([I32, I32], [I64]), functype([I32] * 4, [I32]), P1000])),
                           locals={2: [(49_001, I64)]}),
    # 101 functions of 1,000 params: 101,000, past the module cap.
    "manyparams": module([("sash_alloc", 0, ALLOC), ("echo", 1, PACK)] + [("p%d" % k, 3, i64(0)) for k in range(101)],
                         types=section(1, vec([functype([I32], [I32]), functype([I32, I32], [I64]), functype([I32] * 4, [I32]), P1000]))),
    # 300 KiB of data: past the 256 KiB module cap.
    "big": module([("sash_alloc", 0, ALLOC), ("echo", 1, PACK)], data=vec([seg(4096, b"x" * (300 << 10))])),
    # Three functions of 40,000 locals: each under the per-function cap,
    # 120,000 in all, past the module cap.
    "locals3": module([("sash_alloc", 0, ALLOC), ("echo", 1, PACK), ("e2", 1, PACK), ("e3", 1, PACK)],
                      locals={1: [(40_000, I64)], 2: [(40_000, I64)], 3: [(40_000, I64)]}),
    # 10,001 functions: past the function cap.
    "manyfuncs": module([("sash_alloc", 0, ALLOC), ("echo", 1, PACK)] + [("f%d" % k, 1, i64(0)) for k in range(9_999)]),
    # Valid to sash's checks, invalid to wazero (returns i32, not i64).
    "badcode": module([("sash_alloc", 0, ALLOC), ("echo", 1, i32(0))]),
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
