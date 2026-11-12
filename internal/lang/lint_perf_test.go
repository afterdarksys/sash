//go:build !race

package lang

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// lintFill repeats line(i) for i counting down until the next would pass
// the 1 MiB expansion cap, between head and tail(n), n the last i.
func lintFill(head string, line func(i int) string, tail func(n int) string) string {
	var lines []string
	size := len(head) + 64
	for i := 1; ; i++ {
		l := line(i)
		if size+len(l) > expandMax-64 {
			break
		}
		size += len(l)
		lines = append(lines, l)
	}
	var b strings.Builder
	b.WriteString(head)
	for i := len(lines) - 1; i >= 0; i-- {
		b.WriteString(lines[i])
	}
	b.WriteString(tail(len(lines)))
	return b.String()
}

// Hostile inputs at the 1 MiB cap finish in under a second each, for
// lint and needs: the taint is a worklist, catch edges go through one
// node per try, fn bodies are scanned once per depth, and { } nesting
// past 64 is a parse error. Timing tests do not run under -race.
func TestLintHostileInputsAreFast(t *testing.T) {
	nest := func(n int, inner string) string {
		return strings.Repeat("try { ", n) + inner + strings.Repeat(" } catch e { echo $e }", n)
	}
	legal := nest(blockDepthMax, "echo $s")
	var refs strings.Builder
	for i := 0; refs.Len() < expandMax-8000; i++ {
		fmt.Fprintf(&refs, " $v%d", i)
	}
	cases := []struct {
		name string
		src  string
		code int
	}{
		{"let chain in reverse", lintFill("declare -s a0 = x\n", func(i int) string { return fmt.Sprintf("let a%d = $a%d\n", i, i-1) }, func(n int) string { return fmt.Sprintf("echo $a%d\n", n) }), 1},
		{"fn chain in reverse", lintFill("declare -s p0 = x\nf1 $p0\n", func(i int) string { return fmt.Sprintf("fn f%d(p%d) { f%d $p%d }\n", i, i, i+1, i) }, func(n int) string { return fmt.Sprintf("fn f%d(p%d) { echo $p%d }\n", n+1, n+1, n+1) }), 1},
		{"try nested past the cap", nest(35000, "ps"), exitType},
		{"try nested 64 deep, repeated", lintFill("declare -s s = x\n", func(int) string { return legal + "\n" }, func(int) string { return "" }), 1},
		{"try nested 64 deep around many reads", "declare -s v0 = x\n" + nest(blockDepthMax, "echo"+refs.String()), 1},
	}
	for _, c := range cases {
		if len(c.src) > expandMax {
			t.Fatalf("%s: %d bytes", c.name, len(c.src))
		}
		start := time.Now()
		out := Lint(c.src)
		lint := time.Since(start)
		start = time.Now()
		Needs(c.src, NeedsOptions{})
		needs := time.Since(start)
		t.Logf("%s (%d KiB): lint %v, needs %v", c.name, len(c.src)>>10, lint, needs)
		if out.Status != c.code || (c.code == 1 && !strings.Contains(out.Stdout, "secret-to-sink")) {
			t.Errorf("%s: status %d %q", c.name, out.Status, out.Stderr)
		}
		if lint > time.Second || needs > time.Second {
			t.Errorf("%s: lint %v needs %v, over 1 s", c.name, lint, needs)
		}
	}
}
