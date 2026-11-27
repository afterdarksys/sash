package lang

import (
	"strconv"
	"strings"
	"testing"
)

func runExpr(t *testing.T, src string) Outcome {
	t.Helper()
	return Run(src, exprGrant(), &rowsParent{}, Options{})
}

// checkOut runs each source and wants status 0 and exactly that stdout line.
func checkOut(t *testing.T, cases map[string]string) {
	t.Helper()
	for src, want := range cases {
		out := runExpr(t, src)
		if out.Status != 0 || strings.TrimSuffix(out.Stdout, "\n") != want {
			t.Errorf("%s: status %d out %q err %q want %q", src, out.Status, out.Stdout, out.Stderr, want)
		}
	}
}

// checkRefused runs each source and wants exit 2 with the message in stderr.
func checkRefused(t *testing.T, cases map[string]string) {
	t.Helper()
	for src, msg := range cases {
		out := runExpr(t, src)
		if out.Status != exitType || !strings.Contains(out.Stderr, msg) {
			t.Errorf("%s: status %d out %q err %q want %q", src, out.Status, out.Stdout, out.Stderr, msg)
		}
	}
}

func TestIntWithBase(t *testing.T) {
	checkOut(t, map[string]string{
		`echo [ int("ff", 16) ] [ int("0xff", 16) ] [ int("0XFF", 16) ] [ int("FF", 16) ]`: "255 255 255 255",
		`echo [ int("-0b101", 2) ] [ int("0B11", 2) ] [ int("+101", 2) ]`:                  "-5 3 5",
		`echo [ int("0o17", 8) ] [ int("-0O17", 8) ] [ int("777", 8) ]`:                    "15 -15 511",
		`echo [ int("42", 10) ] [ int("-42", 10) ] [ int("007", 10) ]`:                     "42 -42 7",
		// 0b is digits in base 16: only the base's own prefix is a prefix.
		`echo [ int("0b101", 16) ]`: "45313",
		`echo [ int("-9223372036854775808", 10) ] [ int("7fffffffffffffff", 16) ]`: "-9223372036854775808 9223372036854775807",
		`echo [ int("-0x8000000000000000", 16) ]`:                                  "-9223372036854775808",
		// One argument is unchanged.
		`echo [ int("42") ] [ int(3.9) ] [ int(true) ] [ int(-7) ]`: "42 3 1 -7",
	})
	checkRefused(t, map[string]string{
		`echo [ int("0x10", 2) ]`:                  "type int(text)",
		`echo [ int("0b10", 8) ]`:                  "type int(text)",
		`echo [ int("0x10", 10) ]`:                 "type int(text)",
		`echo [ int("9223372036854775808", 10) ]`:  "type expr overflow",
		`echo [ int("-9223372036854775809", 10) ]`: "type expr overflow",
		`echo [ int("10000000000000000", 16) ]`:    "type expr overflow",
		`echo [ int("1_0", 10) ]`:                  "type int(text)",
		`echo [ int("0x_10", 16) ]`:                "type int(text)",
		`echo [ int("0x10") ]`:                     "type int(string)",
		`echo [ int("ff") ]`:                       "type int(string)",
		`echo [ int("", 10) ]`:                     "type int(text)",
		`echo [ int("0x", 16) ]`:                   "type int(text)",
		`echo [ int("-", 10) ]`:                    "type int(text)",
		`echo [ int("--5", 10) ]`:                  "type int(text)",
		`echo [ int("-+5", 10) ]`:                  "type int(text)",
		`echo [ int("-0x-5", 16) ]`:                "type int(text)",
		`echo [ int(" 5", 10) ]`:                   "type int(text)",
		`echo [ int("12", 2) ]`:                    "type int(text)",
		`echo [ int("ff", 3) ]`:                    "type int base",
		`echo [ int("ff", 36) ]`:                   "type int base",
		`echo [ int("ff", 0) ]`:                    "type int base",
		`echo [ int(5, 10) ]`:                      "type int(number, number)",
		`echo [ int("5", 1.5) ]`:                   "type int(string, number)",
		`echo [ int("5", "x") ]`:                   "type int(string, string)",
		`echo [ int(repeat("1", 1025), 10) ]`:      "type int(text)",
		`echo [ int() ]`:                           "type expr usage int",
		`echo [ int("1", 2, 3) ]`:                  "type expr usage int",
	})
}

func TestRadix(t *testing.T) {
	checkOut(t, map[string]string{
		`echo [ radix(255, 16) ] [ radix(5, 2) ] [ radix(-255, 16) ] [ radix(35, 36) ] [ radix(0, 2) ]`: "ff 101 -ff z 0",
		`echo [ radix(int("-9223372036854775808", 10), 2) ]`:                                            "-1" + strings.Repeat("0", 63),
		`echo [ radix("8", 8) ]`: "10",
	})
	checkRefused(t, map[string]string{
		`echo [ radix(255, 1) ]`:    "type radix base",
		`echo [ radix(255, 37) ]`:   "type radix base",
		`echo [ radix(255, -16) ]`:  "type radix base",
		`echo [ radix(1.5, 2) ]`:    "type radix(number, number)",
		`echo [ radix("x", 2) ]`:    "type radix(string, number)",
		`echo [ radix(1) ]`:         "type expr usage radix",
		`echo [ radix(1, 2, 3) ]`:   "type expr usage radix",
		`echo [ hex(255) ]`:         "type text(number)",
		`echo [ radix(255, 2.0) ]`:  "type radix(number, number)",
		`echo [ radix(255, null) ]`: "type radix(number, null)",
	})
}

// radix agrees with strconv for every base 2 to 36, and int reads back
// what radix wrote in each base int takes.
func TestRadixIntRoundTrip(t *testing.T) {
	values := []int64{0, 1, -1, 2, 7, 8, 15, 16, 35, 36, 255, -256, 1 << 31, -(1 << 40), 123456789012345, 9223372036854775807, -9223372036854775808}
	for base := 2; base <= 36; base++ {
		var src, want []string
		for _, v := range values {
			src = append(src, "[ radix(int(\""+strconv.FormatInt(v, 10)+"\", 10), "+strconv.Itoa(base)+") ]")
			want = append(want, strconv.FormatInt(v, base))
		}
		checkOut(t, map[string]string{"echo " + strings.Join(src, " "): strings.Join(want, " ")})
	}
	for _, base := range []string{"2", "8", "10", "16"} {
		var src []string
		for _, v := range values {
			n := "int(\"" + strconv.FormatInt(v, 10) + "\", 10)"
			src = append(src, "[ int(radix("+n+", "+base+"), "+base+") == "+n+" ]")
		}
		checkOut(t, map[string]string{"echo " + strings.Join(src, " "): strings.TrimSpace(strings.Repeat("true ", len(values)))})
	}
}

func TestUnhex(t *testing.T) {
	checkOut(t, map[string]string{
		`echo [ unhex("48656C6c6f") ] [ unhex("") == "" ] [ unhex("e4b896") ]`: "Hello true \u4e16",
		// Multibyte runes and a NUL survive the round trip, byte for byte.
		"let s = [ \"h\u00e9 \u4e16\U0001F600\" + unhex(\"00\") + \"z\" ]\necho [ unhex(hex($s)) == $s ] [ len(unhex(hex($s))) ] [ hex(unhex(\"00ff\")) ]": "true 7 00ff",
	})
	checkRefused(t, map[string]string{
		`echo [ unhex("abc") ]`:    "type unhex",
		`echo [ unhex("zz") ]`:     "type unhex",
		`echo [ unhex("0x41") ]`:   "type unhex",
		`echo [ unhex(" 41") ]`:    "type unhex",
		`echo [ unhex(41) ]`:       "type unhex(number)",
		`echo [ unhex() ]`:         "type expr usage unhex",
		`echo [ unhex("41", "") ]`: "type expr usage unhex",
	})
}

func TestBitwise(t *testing.T) {
	checkOut(t, map[string]string{
		`echo [ band(12, 10) ] [ bor(12, 10) ] [ bxor(12, 10) ] [ bnot(0) ] [ bnot(5) ]`: "8 14 6 -1 -6",
		`echo [ band(-1, 255) ] [ bor("3", 4) ] [ bxor(-1, 0) ]`:                         "255 7 -1",
		`echo [ shl(1, 4) ] [ shl(1, 63) ] [ shl(3, 63) ] [ shl(5, 0) ]`:                 "16 -9223372036854775808 -9223372036854775808 5",
		// shr is arithmetic: the sign bit is copied in.
		`echo [ shr(16, 4) ] [ shr(-16, 2) ] [ shr(-1, 63) ] [ shr(int("-9223372036854775808", 10), 63) ] [ shr(7, 63) ]`: "1 -4 -1 -1 0",
		`echo [ popcount(0) ] [ popcount(255) ] [ popcount(-1) ] [ popcount(int("-9223372036854775808", 10)) ]`:           "0 8 64 1",
	})
	var src []string
	for _, v := range []string{"0", "1", "-1", "12345", "-98765", "9223372036854775807", `int("-9223372036854775808", 10)`} {
		src = append(src, "[ bxor("+v+", "+v+") == 0 && bnot(bnot("+v+")) == "+v+" && band("+v+", bnot("+v+")) == 0 && bor("+v+", bnot("+v+")) == -1 ]")
	}
	checkOut(t, map[string]string{"echo " + strings.Join(src, " "): strings.TrimSpace(strings.Repeat("true ", len(src)))})
	checkRefused(t, map[string]string{
		`echo [ shl(1, 64) ]`:       "type shift",
		`echo [ shr(1, 64) ]`:       "type shift",
		`echo [ shl(1, -1) ]`:       "type shift",
		`echo [ shr(1, -1) ]`:       "type shift",
		`echo [ band(1.5, 1) ]`:     "type band(float)",
		`echo [ bor(1, 2.0) ]`:      "type bor(float)",
		`echo [ bxor("1.5", 1) ]`:   "type bxor(float)",
		`echo [ bnot(0.5) ]`:        "type bnot(float)",
		`echo [ shl(1, 2.0) ]`:      "type shl(float)",
		`echo [ shr(4.0, 1) ]`:      "type shr(float)",
		`echo [ popcount(1.0) ]`:    "type popcount(float)",
		`echo [ band("a", 1) ]`:     "type band(string, number)",
		`echo [ bnot([1]) ]`:        "type bnot(list)",
		`echo [ popcount(true) ]`:   "type popcount(bool)",
		`echo [ band(1) ]`:          "type expr usage band",
		`echo [ bnot(1, 2) ]`:       "type expr usage bnot",
		`echo [ shl(1) ]`:           "type expr usage shl",
		`echo [ popcount() ]`:       "type expr usage popcount",
		`echo [ bxor(1, 2, 3) ]`:    "type expr usage bxor",
		`echo [ shr(1, 2, 3) ]`:     "type expr usage shr",
		`echo [ bor() ]`:            "type expr usage bor",
		`echo [ band(null, null) ]`: "type band(null, null)",
	})
}

func TestIPParse(t *testing.T) {
	checkOut(t, map[string]string{
		`echo [ is_ip("10.0.0.1") ] [ is_ip("::1") ] [ is_ip("::ffff:1.2.3.4") ] [ is_ip("2001:DB8::1") ]`: "true true true true",
		`echo [ is_ip("fe80::1%eth0") ] [ is_ip("010.1.1.1") ] [ is_ip("1.2.3") ] [ is_ip("10.0.0.0/8") ]`: "false false false false",
		`echo [ is_ip(5) ] [ is_ip(null) ] [ is_ip([1]) ] [ is_ip({}) ] [ is_ip(repeat("1", 2000)) ]`:      "false false false false false",
		`echo [ ip("2001:DB8::1") ] [ ip("2001:0db8:0000:0000:0000:0000:0000:0001") ] [ ip("10.0.0.1") ]`:  "2001:db8::1 2001:db8::1 10.0.0.1",
		`echo [ ip("::FFFF:1.2.3.4") ] [ ip("::ffff:0102:0304") ] [ ip("0:0:0:0:0:0:0:0") ]`:               "::ffff:1.2.3.4 ::ffff:1.2.3.4 ::",
		`echo [ ip_version("10.0.0.1") ] [ ip_version("::1") ] [ ip_version("::ffff:1.2.3.4") ]`:           "4 6 6",
	})
	checkRefused(t, map[string]string{
		`echo [ ip("fe80::1%eth0") ]`:                 "type ip zone",
		`echo [ ip("1.2.3.4%eth0") ]`:                 "type ip zone",
		`echo [ ip("fe80::1%") ]`:                     "type ip zone",
		`echo [ ip_version("1.2.3.4%eth0") ]`:         "type ip zone",
		`echo [ ip_version("fe80::1%25eth0") ]`:       "type ip zone",
		`echo [ ip_class("fe80::1%1") ]`:              "type ip zone",
		`echo [ ip_next("fe80::1%eth0") ]`:            "type ip zone",
		`echo [ ip_in("fe80::1%eth0", "fe80::/10") ]`: "type ip zone",
		`echo [ ip_in("fe80::1", "fe80::%eth0/10") ]`: "type ip zone",
		`echo [ cidr("fe80::%eth0/64") ]`:             "type ip zone",
		`echo [ ip("010.1.1.1") ]`:                    "type ip",
		`echo [ ip("10.0.0.01") ]`:                    "type ip",
		`echo [ ip("1.2.3") ]`:                        "type ip",
		`echo [ ip("256.0.0.1") ]`:                    "type ip",
		`echo [ ip("") ]`:                             "type ip",
		`echo [ ip(" 10.0.0.1") ]`:                    "type ip",
		`echo [ ip_version("x") ]`:                    "type ip",
		`echo [ ip(1) ]`:                              "type ip(number)",
		`echo [ ip_version(null) ]`:                   "type ip_version(null)",
		`echo [ ip(repeat("1", 1025)) ]`:              "type ip(text)",
		`echo [ ip() ]`:                               "type expr usage ip",
		`echo [ ip("a", "b") ]`:                       "type expr usage ip",
		`echo [ is_ip() ]`:                            "type expr usage is_ip",
		`echo [ is_ip(1, 2) ]`:                        "type expr usage is_ip",
		`echo [ ip_version() ]`:                       "type expr usage ip_version",
	})
}

func TestIPIn(t *testing.T) {
	checkOut(t, map[string]string{
		`echo [ ip_in("10.1.2.3", "10.0.0.0/8") ] [ ip_in("11.0.0.1", "10.0.0.0/8") ] [ ip_in("10.1.2.3", "10.1.2.3/8") ]`: "true false true",
		// Both sides are unmapped first.
		`echo [ ip_in("::ffff:10.0.0.1", "10.0.0.0/8") ] [ ip_in("10.0.0.1", "::ffff:10.0.0.0/104") ] [ ip_in("::ffff:11.0.0.1", "10.0.0.0/8") ]`: "true true false",
		// Across families is false, not an error.
		`echo [ ip_in("10.0.0.1", "::/0") ] [ ip_in("::1", "0.0.0.0/0") ] [ ip_in("::ffff:10.0.0.1", "::/0") ]`: "false false true",
		// Containment is monotone for a mapped address: every v6 prefix
		// that holds it as written, down to ::/0, holds it.
		`echo [ ip_in("::ffff:127.0.0.1", "::ffff:0:0/96") ] [ ip_in("::ffff:127.0.0.1", "::ffff:0:0/95") ] [ ip_in("::ffff:127.0.0.1", "::/8") ] [ ip_in("::ffff:127.0.0.1", "::/0") ] [ ip_in("::ffff:127.0.0.1", "127.0.0.0/8") ]`: "true true true true true",
		// A v4 address is in no v6 prefix but a mapped one of /96 or longer.
		`echo [ ip_in("127.0.0.1", "::/0") ] [ ip_in("127.0.0.1", "::ffff:0:0/95") ] [ ip_in("127.0.0.1", "::ffff:0:0/96") ] [ ip_in("127.0.0.1", "::ffff:127.0.0.0/104") ]`: "false false true true",
		`echo [ ip_in("2001:db8::5", "2001:db8::/32") ] [ ip_in("2001:db9::5", "2001:db8::/32") ] [ ip_in("0.0.0.0", "0.0.0.0/0") ]`:                                         "true false true",
	})
	checkRefused(t, map[string]string{
		`echo [ ip_in("010.0.0.1", "10.0.0.0/8") ]`:     "type ip",
		`echo [ ip_in("10.0.0.1", "010.0.0.0/8") ]`:     "type cidr",
		`echo [ ip_in("10.0.0.1", "10.0.0.0") ]`:        "type cidr",
		`echo [ ip_in("10.0.0.1", "10.0.0.0/33") ]`:     "type cidr",
		`echo [ ip_in(1, "10.0.0.0/8") ]`:               "type ip_in(number)",
		`echo [ ip_in("10.0.0.1", 8) ]`:                 "type ip_in(number)",
		`echo [ ip_in("10.0.0.1", repeat("1", 1025)) ]`: "type ip_in(text)",
		`echo [ ip_in("10.0.0.1") ]`:                    "type expr usage ip_in",
		`echo [ ip_in("a", "b", "c") ]`:                 "type expr usage ip_in",
	})
}

func TestCIDR(t *testing.T) {
	checkOut(t, map[string]string{
		`echo [ cidr("10.1.2.3/8") ]`:      `{"network":"10.0.0.0/8","prefix":8,"bits":32,"first":"10.0.0.0","last":"10.255.255.255","size":16777216}`,
		`echo [ cidr("0.0.0.0/0") ]`:       `{"network":"0.0.0.0/0","prefix":0,"bits":32,"first":"0.0.0.0","last":"255.255.255.255","size":4294967296}`,
		`echo [ cidr("10.0.0.7/32") ]`:     `{"network":"10.0.0.7/32","prefix":32,"bits":32,"first":"10.0.0.7","last":"10.0.0.7","size":1}`,
		`echo [ cidr("::/0") ]`:            `{"network":"::/0","prefix":0,"bits":128,"first":"::","last":"ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff","size":null}`,
		`echo [ cidr("2001:DB8::1/128") ]`: `{"network":"2001:db8::1/128","prefix":128,"bits":128,"first":"2001:db8::1","last":"2001:db8::1","size":1}`,
		`echo [ cidr("2001:db8::/64").last ] [ cidr("2001:db8::/65").size ] [ cidr("2001:db8::/66").size ]`: "2001:db8::ffff:ffff:ffff:ffff null 4611686018427387904",
		`echo [ cidr("10.0.0.0/23").last ] [ cidr("192.168.1.77/29").first ]`:                               "10.0.1.255 192.168.1.72",
	})
	checkRefused(t, map[string]string{
		`echo [ cidr("10.0.0.0/33") ]`:     "type cidr",
		`echo [ cidr("10.0.0.0") ]`:        "type cidr",
		`echo [ cidr("010.0.0.0/8") ]`:     "type cidr",
		`echo [ cidr("::/129") ]`:          "type cidr",
		`echo [ cidr(8) ]`:                 "type cidr(number)",
		`echo [ cidr(repeat("1", 1025)) ]`: "type cidr(text)",
		`echo [ cidr() ]`:                  "type expr usage cidr",
		`echo [ cidr("10.0.0.0/8", "x") ]`: "type expr usage cidr",
	})
}

func TestIPClass(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0": "unspecified", "127.0.0.1": "loopback", "10.1.2.3": "private", "172.16.0.1": "private",
		"192.168.1.1": "private", "169.254.1.1": "link_local", "224.0.0.1": "multicast", "239.1.1.1": "multicast",
		"8.8.8.8": "global", "1.1.1.1": "global", "2606:4700::1111": "global",
		// The reserved table, one address per entry.
		"0.1.2.3": "reserved", "100.100.1.1": "reserved", "192.0.0.8": "reserved", "192.0.2.1": "reserved",
		"198.18.0.1": "reserved", "198.51.100.1": "reserved", "203.0.113.1": "reserved", "240.0.0.1": "reserved",
		"255.255.255.255": "reserved", "::1.2.3.4": "reserved", "64:ff9b::1.2.3.4": "reserved", "64:ff9b:1::1": "reserved",
		"100::1": "reserved", "2001::1": "reserved", "2001:db8::1": "reserved", "2002::1": "reserved",
		"fec0::1": "reserved", "3fff::1": "reserved", "::ffff:198.51.100.1": "reserved", "::ffff:100.64.0.1": "reserved",
		"::": "unspecified", "::1": "loopback", "fd00::1": "private", "fc00::1": "private", "fe80::1": "link_local",
		"ff02::1": "multicast", "ff0e::1": "multicast", "2001:4860:4860::8888": "global",
		"::ffff:127.0.0.1": "loopback", "::ffff:10.0.0.1": "private", "::ffff:0.0.0.0": "unspecified",
	}
	for addr, want := range cases {
		checkOut(t, map[string]string{`echo [ ip_class("` + addr + `") ]`: want})
	}
	checkRefused(t, map[string]string{
		`echo [ ip_class("x") ]`:      "type ip",
		`echo [ ip_class(1) ]`:        "type ip_class(number)",
		`echo [ ip_class() ]`:         "type expr usage ip_class",
		`echo [ ip_class("a", "b") ]`: "type expr usage ip_class",
	})
}

func TestIPNextPrev(t *testing.T) {
	checkOut(t, map[string]string{
		`echo [ ip_next("10.0.0.255") ] [ ip_prev("10.0.1.0") ] [ ip_next("::") ] [ ip_prev("::1") ] [ ip_next("2001:db8::ffff") ]`: "10.0.1.0 10.0.0.255 ::1 :: 2001:db8::1:0",
		`echo [ ip_next("254.255.255.255") ] [ ip_prev("255.255.255.255") ] [ ip_next("::ffff:1.2.3.4") ]`:                          "255.0.0.0 255.255.255.254 ::ffff:1.2.3.5",
	})
	checkRefused(t, map[string]string{
		`echo [ ip_next("255.255.255.255") ]`:                         "type ip range",
		`echo [ ip_prev("0.0.0.0") ]`:                                 "type ip range",
		`echo [ ip_next("ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff") ]`: "type ip range",
		`echo [ ip_prev("::") ]`:                                      "type ip range",
		// A mapped address does not step out of the v4 range it stands for.
		`echo [ ip_next("::ffff:255.255.255.255") ]`: "type ip range",
		`echo [ ip_prev("::ffff:0.0.0.0") ]`:         "type ip range",
		`echo [ ip_next(1) ]`:                        "type ip_next(number)",
		`echo [ ip_prev("x") ]`:                      "type ip",
		`echo [ ip_next() ]`:                         "type expr usage ip_next",
		`echo [ ip_prev("a", "b") ]`:                 "type expr usage ip_prev",
	})
}

// radix, unhex and cidr spend the budget for what they build: radix its
// output, unhex half its input plus 64, cidr a fixed cost.
func TestNumNetCharges(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []any
		cost int
	}{
		{"radix", []any{int64(-1 << 63), int64(2)}, 65},
		{"radix", []any{int64(255), int64(16)}, 2},
		{"radix", []any{int64(-255), int64(16)}, 3},
		{"radix", []any{int64(0), int64(36)}, 1},
		{"radix", []any{int64(36), int64(36)}, 2},
		{"radix", []any{int64(1<<63 - 1), int64(2)}, 63},
		{"ip", []any{"10.0.0.1"}, addrTextMax},
		{"ip_next", []any{"10.0.0.1"}, addrTextMax},
		{"ip_prev", []any{"10.0.0.1"}, addrTextMax},
		{"unhex", []any{"41424344"}, 4 + 64},
		{"cidr", []any{"10.0.0.0/8"}, cidrCost},
	} {
		c := &evalCtx{budget: 1000}
		if _, err := exprFuncs[tc.name].fn(tc.args, c); err != nil || c.budget != 1000-tc.cost {
			t.Errorf("%s%v: budget %d err %v, want %d spent", tc.name, tc.args, c.budget, err, tc.cost)
		}
		c = &evalCtx{budget: tc.cost - 1}
		if _, err := exprFuncs[tc.name].fn(tc.args, c); err == nil || !strings.Contains(err.Error(), "too large") {
			t.Errorf("%s%v with too little budget: %v", tc.name, tc.args, err)
		}
	}
}

// A result computed from a secret is a secret: unhex of a secret's hex
// prints ***, and so does every new function over it.
func TestNumNetSecrets(t *testing.T) {
	st := NewState()
	out := Run(`declare -s x = [ "736563726574" ]
declare -s a = [ "10.0.0.1" ]
echo [ unhex($x) ]
echo [ unhex($x) ] [ radix(int($x, 16), 36) ] [ is_ip($a) ] [ ip($a) ] [ cidr($a + "/8") ] [ band(int($x, 16), 255) ]
let r = [ unhex($x) ]
echo $r`, exprGrant(), &rowsParent{}, Options{State: st})
	if out.Status != 0 || out.Stdout != "***\n*** *** *** *** *** ***\n***\n" || st.SecretCount() != 3 {
		t.Fatalf("%+v secrets %d", out, st.SecretCount())
	}
	if all := out.Stdout + out.Stderr; strings.Contains(all, "secret") || strings.Contains(all, "10.0.0") {
		t.Fatalf("leaked: %q", all)
	}
	// The secret's own text is still refused at a sink after unhex.
	out = Run(`declare -s x = [ "736563726574" ]
cat [ "/srv/" + unhex($x) ]`, exprGrant(), &rowsParent{}, Options{})
	if out.Status == 0 || !strings.Contains(out.Stderr, "a secret cannot leave sash") {
		t.Fatalf("sink: %+v", out)
	}
}

// An answer through ip() still carries the answer: it selects no
// subcommand, and as an argument it runs.
func TestNumNetAnswerSelectsNoSubcommand(t *testing.T) {
	for _, src := range []string{
		"let x = (term prompt \"q\")\nlet y = [ ip($x) ]\nunit $y nginx\n",
		"let x = (term prompt \"q\")\nunit [ ip_class($x) ] nginx\n",
	} {
		p := &recParent{}
		out := Run(src, termGrant(), p, Options{Term: &fakeTerm{answers: []string{"10.0.0.1"}}})
		if out.Status != exitType || out.Stderr != "command_from_value: a term prompt answer selects no subcommand\n" || len(p.calls) != 0 {
			t.Errorf("%q: %+v calls %v", src, out, p.calls)
		}
	}
	p := &recParent{}
	out := Run("let x = (term prompt \"q\")\nlet y = [ ip($x) ]\nunit restart $y\n", termGrant(), p, Options{Term: &fakeTerm{answers: []string{"10.0.0.1"}}})
	if out.Status != 0 || len(p.calls) != 1 || p.calls[0].Key != "unit.restart" {
		t.Errorf("as an argument: %+v calls %v", out, p.calls)
	}
}

// The 1 KiB cap: an input of exactly 1024 bytes reaches the parser, one
// byte more is refused before it.
func TestParseCapBoundary(t *testing.T) {
	checkOut(t, map[string]string{
		// + on two decimal strings adds them, so the padding is built
		// with replace: "000...01", 1024 bytes.
		`echo [ int(replace(pad("1", -1024), " ", "0"), 10) ] [ int("0x" + repeat("0", 1021) + "f", 16) ] [ int("0b" + repeat("0", 1021) + "1", 2) ]`: "1 15 1",
	})
	for src, want := range map[string]string{
		`echo [ int(replace(pad("1", -1025), " ", "0"), 10) ]`:           "type int(text)\n",
		`echo [ int("0x" + repeat("0", 1022) + "f", 16) ]`:               "type int(text)\n",
		`echo [ ip(repeat("1", 1024)) ]`:                                 "type ip\n",
		`echo [ ip(repeat("1", 1025)) ]`:                                 "type ip(text)\n",
		`echo [ ip_in("10.0.0.1", repeat("1", 1024)) ]`:                  "type cidr\n",
		`echo [ ip_in("10.0.0.1", repeat("1", 1025)) ]`:                  "type ip_in(text)\n",
		`echo [ cidr(repeat("1", 1024)) ]`:                               "type cidr\n",
		`echo [ cidr(repeat("1", 1025)) ]`:                               "type cidr(text)\n",
		`echo [ is_ip(repeat("1", 1024)) ] [ is_ip(repeat("1", 1025)) ]`: "",
	} {
		out := runExpr(t, src)
		if want == "" {
			if out.Status != 0 || out.Stdout != "false false\n" {
				t.Errorf("%s: %+v", src, out)
			}
			continue
		}
		if out.Status != exitType || out.Stderr != want {
			t.Errorf("%s: status %d err %q want %q", src, out.Status, out.Stderr, want)
		}
	}
}
