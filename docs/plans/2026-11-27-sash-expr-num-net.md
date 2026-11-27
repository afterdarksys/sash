# sash: number bases, bitwise and IP address functions in expressions

Status: contract, 2026-11-27. Asked for by Ryan ("hex, binary, ipv4/6 utility functions").

## 0. What exists, and what this adds

Expressions (`[ ]`) have pure functions in one Go table (`internal/lang/funcs.go`,
`exprFuncs`), listed by `builtin` and in `skills/sash/reference/language.md`. Today:
`hex(s)` encodes a string's bytes, `base64`/`unbase64`, `sha256`; `int(x)` reads only a
decimal number. There is nothing for other number bases, bitwise work, or IP addresses.

This adds pure functions only. No verb, no host call, no entitlement key, no new syntax,
no new operator: `&`, `|`, `^`, `<<`, `>>` stay out of the grammar (`|` and `>` already
mean pipe and redirect outside `[ ]`, and one meaning per character is easier to read and
to lint). Every function follows the table's existing rules: argument counts in the
table, a wrong type is `type <name>(<types>)` (`fnErr`), every result is charged against
the evaluation budget before it is built, nothing reads the clock, the network, or a file.

Implementation uses only the Go standard library: `strconv`, `encoding/hex`, `math/bits`,
`net/netip`. Never `net.ParseIP` (it accepts forms netip refuses, and its v4-in-v6 rules
differ).

## 1. Number bases

| call | result |
| --- | --- |
| `int(s, base)` | `s` read as an integer in `base` (2, 8, 10 or 16; anything else is exit 2 `type int base`). An optional sign, then an optional prefix that must match the base (`0x`/`0X` for 16, `0b`/`0B` for 2, `0o`/`0O` for 8), then digits; `_` separators are refused. Out of int64 range is `type expr overflow`. A string that does not parse is `type int(text)`. |
| `int(x)` | unchanged: one argument still reads only a decimal number, so `int("0x10")` stays an error. Existing scripts keep their meaning. |
| `radix(n, base)` | an int as text in `base` (2 to 36), lowercase, no prefix, `-` for a negative. `radix(255, 16)` is `"ff"`, `radix(5, 2)` is `"101"`. |
| `unhex(s)` | the inverse of `hex`: an even number of hex digits (either case) to the string of those bytes, as `unbase64` returns its bytes; anything else is exit 2 `type unhex`. |

`hex(s)` keeps its meaning (a string's bytes). `hex(255)` stays a type error: write
`radix(255, 16)`. One name, one meaning.

## 2. Bitwise

All on int64; a float argument is `type <name>(float)`, never truncated.

| call | result |
| --- | --- |
| `band(a, b)` `bor(a, b)` `bxor(a, b)` | and, or, xor |
| `bnot(a)` | complement |
| `shl(a, n)` `shr(a, n)` | shift left, arithmetic shift right; `n` is 0 to 63, anything else is exit 2 `type shift` |
| `popcount(a)` | number of set bits of the 64-bit two's complement |

Shifting a bit out is not an error (it is what a shift does); there is no overflow check
on `shl`, and that is stated in the docs.

## 3. IP addresses

Inputs are text. Parsing is `netip.ParseAddr` / `netip.ParsePrefix`: dotted v4 with no
leading zeros (`010.1.1.1` is refused, not read as octal), v6 in any RFC 4291 form,
v4-mapped v6 (`::ffff:1.2.3.4`). A zone (`fe80::1%eth0`) is refused everywhere
(`type ip zone`): a zone names an interface on one machine and means nothing to sash.

| call | result |
| --- | --- |
| `is_ip(s)` | `true` when `s` parses as an address, else `false`, for any input type. The one function here that never errors, so a `where` over host data can filter. |
| `ip(s)` | the canonical text (v6 compressed lowercase; v4-mapped v6 stays mapped, `::ffff:1.2.3.4`); exit 2 `type ip` when it does not parse. |
| `ip_version(s)` | `4` or `6` (a v4-mapped v6 is `6`). |
| `ip_in(addr, prefix)` | `true` when the address is in the prefix. Both sides are unmapped first, so `::ffff:10.0.0.1` is in `10.0.0.0/8`. A v4 address and a v6 prefix (or the reverse, after unmapping) is `false`, not an error. |
| `cidr(s)` | a map: `network` (the masked prefix as text, so `10.1.2.3/8` gives `10.0.0.0/8`), `prefix` (int), `bits` (32 or 128), `first` and `last` (text), `size` (the address count as an int when it fits in int64, else `null`). A prefix that does not parse is exit 2 `type cidr`. |
| `ip_class(s)` | one of `unspecified`, `loopback`, `private`, `link_local`, `multicast`, `global`, by netip's predicates checked in that order (an unmapped address); `private` is netip's `IsPrivate` (RFC 1918 and fc00::/7). |
| `ip_next(s)` `ip_prev(s)` | the next and previous address; past the end of the family is exit 2 `type ip range`. |

No range expansion (`hosts(prefix)` that lists every address) in this round: a /8 is 16M
entries. `[ range(...) ]` style limits would apply if it is added later.

## 4. Secrets, answers, provenance

Unchanged rules, stated so tests pin them: these are expression functions, so a result
computed from a secret is a secret, and a result computed from a `term prompt` answer
still carries the answer (it selects no subcommand). `unhex` of a secret's hex is the
secret again, and stays secret. `is_ip($secret)` returns a bool that is secret, as every
expression over a secret is. Nothing here weakens a rule; nothing here needs a new one.

Known limit (already in features.md, repeat it in the new rows): an encoding of a secret
made with `hex`/`radix` is not the secret's text for the file-mode and recording checks.

## 5. Budget and limits

- `radix` charges its output length; `unhex` charges `len(s)/2 + 64`; `cidr` a fixed map
  cost; others a scalar.
- Inputs over 1 KiB to the IP and base parsers are `type <name>(text)` before parsing (an
  address or a number is never that long).

## 6. Catalog, docs, lint, needs

- New error texts go in `internal/codes` only if the catalog test requires it; they
  follow the `type ...` family the other functions use.
- `language.md`: two new rows in the function table (`number`, `net`), the
  `int(s, base)` form under `convert`, `unhex` under `encode`, and one runnable example per
  group that the skill test runs against the fake host (`TestSkillExamplesAreSash`).
- `--needs` and `--lint`: nothing new (pure functions add no key and no flow rule).
- `builtin` lists the new names; `builtin_test`'s table/doc agreement test covers them.

## 7. Tests

- Each function: normal cases, edge cases, wrong type, wrong count.
- `int`: `int("ff", 16)`, `int("0xff", 16)`, `int("-0b101", 2)`, `int("0x10", 2)` refused,
  `int("9223372036854775808", 10)` overflow, `int("1_0", 10)` refused, `int("0x10")`
  (one argument) still refused.
- `radix`/`int` round trip over a spread of values and every base 2..36 for `radix`.
- `unhex(hex(s)) == s` for text with multibyte runes and a NUL; odd length and a non-hex
  digit refused.
- Bitwise: identities (`bxor(a, a) == 0`, `bnot(bnot(a)) == a`), `shr` of a negative is
  arithmetic, shift 64 refused, float refused.
- IP: v4, v6, mapped, zone refused, leading zero refused, `ip("2001:DB8::1")` canonical,
  `ip_in` across families and mapped, `cidr("10.1.2.3/8")`, `/0` and `/128` sizes
  (`size` null for v6 /0), `ip_class` of each class for both families, `ip_next` at
  `255.255.255.255` refused.
- Secret and answer: `[ unhex($s) ]` of a secret prints `***`; an answer through `ip()`
  is still refused as a subcommand.
- Mutations: drop the zone refusal; read leading-zero v4 as decimal; let `int` one-arg
  accept `0x`; make `ip_in` skip unmapping; skip the budget charge in `radix`. Each must
  fail a test.

## 8. Threats

- Host data fed to parsers: bounded input (1 KiB), netip and strconv only, no regex
  backtracking, no allocation proportional to a prefix size.
- Ambiguous address forms (octal-looking v4, zones, v4-mapped) resolved one documented
  way, so `ip_in` cannot be fooled by `010.0.0.1` or `::ffff:` into a different answer
  than a reader expects.
- No new path for a secret or an answer: §4.

## 9. Open questions for Ryan

1. Names: `radix(n, base)` versus `fmt_int(n, base)` or a `hex(n)` overload. Recommended:
   `radix`, with `hex` kept for bytes only.
2. Bitwise as functions (recommended, no grammar change) versus infix operators inside
   `[ ]` only.
3. `ip_class` as one function (recommended) versus `is_private`, `is_loopback`, ... booleans.
4. Add `hosts(prefix, max)` to expand a small prefix into a list now, capped at 4096?
   Recommended: not in this round.
