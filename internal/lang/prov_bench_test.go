package lang

import (
	"fmt"
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
)

// benchBindings is a 10k-binding script: lets, expressions over earlier
// names, and verb results.
func benchBindings() string {
	var b strings.Builder
	b.WriteString("let a0 = (ps)\n")
	for i := 1; i < 10000; i++ {
		switch i % 4 {
		case 0:
			fmt.Fprintf(&b, "let a%d = (ps)\n", i)
		case 1:
			fmt.Fprintf(&b, "let a%d = [ len($a%d) + 1 ]\n", i, i-1)
		case 2:
			fmt.Fprintf(&b, "let a%d = $a%d\n", i, i-1)
		default:
			fmt.Fprintf(&b, "let a%d = \"x$a%d\"\n", i, i-2)
		}
	}
	return b.String()
}

// BenchmarkBindings10k guards provenance's cost: it must stay within 20%
// of the same script without it. Measured when provenance landed (i9,
// 20 runs, alternating builds): 31.8 ms without, 35.6 ms with (+12%),
// 257,910 and 260,439 allocations.
func BenchmarkBindings10k(b *testing.B) {
	src := benchBindings()
	g := grant.Grant{V: "sash/1", Entitlements: []string{"ps"}}
	for i := 0; i < b.N; i++ {
		st := NewState()
		st.loopMax = loopMaxDefault
		st.verbMax = 100000
		if out := Run(src, g, &rowsParent{}, Options{State: st}); out.Status != 0 {
			b.Fatalf("%+v", out.Stderr)
		}
	}
}
