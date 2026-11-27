package lang

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/bits"
	"net/netip"
	"strconv"
	"strings"
)

// Number bases, bitwise and IP address functions (contract
// 2026-11-27-sash-expr-num-net). Pure and in process like the rest of the
// table. Parsing is strconv and net/netip only; an input over parseMax is
// refused before it is parsed, since a number or an address is never that
// long.

const parseMax = 1024

// fnIntBase is int(s, base): s read in base 2, 8, 10 or 16, with an
// optional sign, then an optional prefix that must match the base, then
// digits. An underscore is refused (strconv allows one only for base 0).
func fnIntBase(a []any) (any, error) {
	s, ok := a[0].(string)
	base, ok2 := intArg(a[1])
	if !ok || !ok2 {
		return nil, fnErr("int", a...)
	}
	prefix := map[int64]string{2: "0b", 8: "0o", 10: "", 16: "0x"}
	p, known := prefix[base]
	if !known {
		return nil, fmt.Errorf("type int base")
	}
	if len(s) > parseMax {
		return nil, fmt.Errorf("type int(text)")
	}
	sign, digits := "", s
	if digits != "" && (digits[0] == '-' || digits[0] == '+') {
		sign, digits = digits[:1], digits[1:]
	}
	if p != "" && len(digits) >= 2 && strings.EqualFold(digits[:2], p) {
		digits = digits[2:]
	}
	if digits == "" || digits[0] == '-' || digits[0] == '+' {
		return nil, fmt.Errorf("type int(text)")
	}
	n, err := strconv.ParseInt(sign+digits, int(base), 64)
	if errors.Is(err, strconv.ErrRange) {
		return nil, fmt.Errorf("type expr overflow")
	}
	if err != nil {
		return nil, fmt.Errorf("type int(text)")
	}
	return n, nil
}

// fnRadix is radix(n, base): an int as text in base 2 to 36, lowercase,
// with no prefix.
func fnRadix(a []any, c *evalCtx) (any, error) {
	n, ok := intArg(a[0])
	base, ok2 := intArg(a[1])
	if !ok || !ok2 {
		return nil, fnErr("radix", a...)
	}
	if base < 2 || base > 36 {
		return nil, fmt.Errorf("type radix base")
	}
	if err := charge(c, radixLen(n, base)); err != nil {
		return nil, err
	}
	return strconv.FormatInt(n, int(base)), nil
}

// radixLen is the length radix(n, base) writes, known before it is built.
func radixLen(n, base int64) int {
	u, l := uint64(n), 1
	if n < 0 {
		u, l = ^uint64(n)+1, 2
	}
	for ; u >= uint64(base); u /= uint64(base) {
		l++
	}
	return l
}

// fnUnhex is the inverse of hex: an even number of hex digits, either
// case, to the string of those bytes.
func fnUnhex(a []any, c *evalCtx) (any, error) {
	s, ok := a[0].(string)
	if !ok {
		return nil, fnErr("unhex", a[0])
	}
	if err := charge(c, len(s)/2+64); err != nil {
		return nil, err
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("type unhex")
	}
	return string(b), nil
}

// ints reads every argument of a bitwise function as an int64. A float
// is refused, never truncated.
func ints(name string, a []any) ([]int64, error) {
	out := make([]int64, len(a))
	for i, d := range a {
		n, ok := number(d)
		if !ok {
			return nil, fnErr(name, a...)
		}
		v, isInt := n.(int64)
		if !isInt {
			return nil, fmt.Errorf("type %s(float)", name)
		}
		out[i] = v
	}
	return out, nil
}

// bitFn wraps a bitwise operation over int64 arguments.
func bitFn(name string, f func(x []int64) (int64, error)) func([]any, *evalCtx) (any, error) {
	return func(a []any, _ *evalCtx) (any, error) {
		x, err := ints(name, a)
		if err != nil {
			return nil, err
		}
		return f(x)
	}
}

// shift is a shift count, 0 to 63; anything else is refused.
func shift(n int64) (uint, error) {
	if n < 0 || n > 63 {
		return 0, fmt.Errorf("type shift")
	}
	return uint(n), nil
}

func fnShl(x []int64) (int64, error) {
	n, err := shift(x[1])
	if err != nil {
		return 0, err
	}
	return int64(uint64(x[0]) << n), nil
}

func fnShr(x []int64) (int64, error) {
	n, err := shift(x[1])
	if err != nil {
		return 0, err
	}
	return x[0] >> n, nil
}

func fnPopcount(x []int64) (int64, error) {
	return int64(bits.OnesCount64(uint64(x[0]))), nil
}

// parseAddr reads an address for the IP function name. A zone is refused,
// any % at all, before parsing: it names an interface on one machine and
// means nothing to sash.
func parseAddr(name string, d any) (netip.Addr, error) {
	s, ok := d.(string)
	if !ok {
		return netip.Addr{}, fnErr(name, d)
	}
	if len(s) > parseMax {
		return netip.Addr{}, fmt.Errorf("type %s(text)", name)
	}
	if strings.Contains(s, "%") {
		return netip.Addr{}, fmt.Errorf("type ip zone")
	}
	ip, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("type ip")
	}
	return ip, nil
}

// parsePrefix reads a prefix for the IP function name, masked.
func parsePrefix(name string, d any) (netip.Prefix, error) {
	s, ok := d.(string)
	if !ok {
		return netip.Prefix{}, fnErr(name, d)
	}
	if len(s) > parseMax {
		return netip.Prefix{}, fmt.Errorf("type %s(text)", name)
	}
	if strings.Contains(s, "%") {
		return netip.Prefix{}, fmt.Errorf("type ip zone")
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("type cidr")
	}
	return p.Masked(), nil
}

// fnIsIP never errors, so a where over host data can filter with it.
func fnIsIP(a []any, _ *evalCtx) (any, error) {
	_, err := parseAddr("is_ip", a[0])
	return err == nil, nil
}

// addrFn wraps an IP function of one address.
func addrFn(name string, f func(netip.Addr, *evalCtx) (any, error)) func([]any, *evalCtx) (any, error) {
	return func(a []any, c *evalCtx) (any, error) {
		ip, err := parseAddr(name, a[0])
		if err != nil {
			return nil, err
		}
		return f(ip, c)
	}
}

// fnIP is the canonical text: v6 compressed lowercase, a v4-mapped v6
// kept mapped.
func fnIP(ip netip.Addr, c *evalCtx) (any, error) {
	return addrText(c, ip)
}

// addrTextMax bounds an address's text (a v6 address is at most 39
// bytes, a mapped one 22), charged before the text is built.
const addrTextMax = 64

func addrText(c *evalCtx, ip netip.Addr) (any, error) {
	if err := charge(c, addrTextMax); err != nil {
		return nil, err
	}
	return ip.String(), nil
}

func fnIPVersion(ip netip.Addr, _ *evalCtx) (any, error) {
	if ip.Is4() {
		return int64(4), nil
	}
	return int64(6), nil
}

// reservedNets are special-purpose ranges (IANA's registries) that are
// none of netip's other classes and are not globally routable.
var reservedNets = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("3fff::/20"),
}

func isReserved(ip netip.Addr) bool {
	for _, p := range reservedNets {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// fnIPClass checks netip's predicates in a fixed order on the unmapped
// address, then the reserved table; anything that is none of them is
// global, which says only that it may be publicly routable.
func fnIPClass(ip netip.Addr, c *evalCtx) (any, error) {
	ip = ip.Unmap()
	class := "global"
	switch {
	case ip.IsUnspecified():
		class = "unspecified"
	case ip.IsLoopback():
		class = "loopback"
	case ip.IsPrivate():
		class = "private"
	case ip.IsLinkLocalUnicast():
		class = "link_local"
	case ip.IsMulticast():
		class = "multicast"
	case isReserved(ip):
		class = "reserved"
	}
	return charged(c, class)
}

func fnIPNext(ip netip.Addr, c *evalCtx) (any, error) {
	return stepped(ip, ip.Next(), c)
}

func fnIPPrev(ip netip.Addr, c *evalCtx) (any, error) {
	return stepped(ip, ip.Prev(), c)
}

// stepped is ip_next or ip_prev's result: the zero Addr is past the end
// of the family, and a mapped address stepping out of ::ffff:0:0/96 is
// past the end of the v4 range it stands for.
func stepped(from, to netip.Addr, c *evalCtx) (any, error) {
	if !to.IsValid() || (from.Is4In6() && !to.Is4In6()) {
		return nil, fmt.Errorf("type ip range")
	}
	return addrText(c, to)
}

// unmapPrefix turns a v4-mapped v6 prefix that lies inside ::ffff:0:0/96
// into the v4 prefix it means.
func unmapPrefix(p netip.Prefix) netip.Prefix {
	if p.Addr().Is4In6() && p.Bits() >= 96 {
		return netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
	}
	return p
}

// fnIPIn is true when the address is in the prefix as written, when a v4
// address's mapped form (::ffff:a.b.c.d) is in a v6 prefix, or when the
// unmapped address is in the unmapped prefix. So containment never
// shrinks as a prefix widens: a v4 or mapped address is in every v6
// prefix that holds its mapped form and in the v4 prefix it maps to. A
// v6 address that is not mapped is in no v4 prefix.
func fnIPIn(a []any, _ *evalCtx) (any, error) {
	ip, err := parseAddr("ip_in", a[0])
	if err != nil {
		return nil, err
	}
	p, err := parsePrefix("ip_in", a[1])
	if err != nil {
		return nil, err
	}
	if ip.Is4() && p.Addr().Is6() && p.Contains(netip.AddrFrom16(ip.As16())) {
		return true, nil
	}
	return p.Contains(ip) || unmapPrefix(p).Contains(ip.Unmap()), nil
}

// cidrCost is the fixed charge for cidr's map of six short fields.
const cidrCost = 256

// fnCIDR describes a prefix. size is the address count when it fits in
// an int64, else null; nothing proportional to the size is built.
func fnCIDR(a []any, c *evalCtx) (any, error) {
	p, err := parsePrefix("cidr", a[0])
	if err != nil {
		return nil, err
	}
	if err := charge(c, cidrCost); err != nil {
		return nil, err
	}
	first := p.Addr()
	b := first.AsSlice()
	for i := p.Bits(); i < first.BitLen(); i++ {
		b[i/8] |= 0x80 >> (i % 8)
	}
	last, _ := netip.AddrFromSlice(b)
	var size any
	if host := first.BitLen() - p.Bits(); host < 63 {
		size = int64(1) << host
	}
	o := newOmap()
	o.set("network", p.String())
	o.set("prefix", int64(p.Bits()))
	o.set("bits", int64(first.BitLen()))
	o.set("first", first.String())
	o.set("last", last.String())
	o.set("size", size)
	return o, nil
}
