//go:build !sashwasm

package lang

import (
	"strings"
	"testing"

	"github.com/afterdarksys/sash/internal/grant"
)

// Without the sashwasm tag, a wasm statement or call is exit 2
// wasm_unsupported, and nothing is read.
func TestWasmUnsupported(t *testing.T) {
	g := grant.Grant{V: "sash/1", Entitlements: []string{"file.read"}, FileRead: []string{"/srv"}}
	for _, src := range []string{
		"wasm m = /srv/x.wasm sha256 " + wasmHash0 + "\necho ran",
		"echo [ m::f(1) ]",
	} {
		p := &countParent{}
		out := Run(src, g, p, Options{})
		if out.Status != exitType || !strings.Contains(out.Stderr, "wasm_unsupported") || p.n != 0 || strings.Contains(out.Stdout, "ran") {
			t.Errorf("%q: %+v calls %d", src, out, p.n)
		}
	}
}
