package wsclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/wsclient/wstest"
)

func pool(srv *httptest.Server) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(srv.Certificate())
	return p
}

func opts(srv *httptest.Server) Options {
	return Options{
		TLS:              &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool(srv)},
		HandshakeTimeout: 2 * time.Second,
		ReadLimit:        1 << 20,
	}
}

func wss(srv *httptest.Server, path string) string {
	return "wss" + strings.TrimPrefix(srv.URL, "https") + path
}

// A dial, a text message out, a binary and a text message back; the
// upgrade offers no subprotocol, no compression, no Origin and no cookie.
func TestDialReadWrite(t *testing.T) {
	var seen http.Header
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		c, err := wstest.Accept(w, r, 64<<10)
		if err != nil {
			return
		}
		ctx := r.Context()
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		if err := c.WriteBinary(ctx, append([]byte("bin:"), data...)); err != nil {
			return
		}
		if err := c.WriteText(ctx, []byte("text")); err != nil {
			return
		}
		if err := c.Close(1000); err != nil {
			return
		}
	}))
	defer srv.Close()
	o := opts(srv)
	o.Header = http.Header{"User-Agent": {"sash-connect/test sah/3"}}
	ctx := context.Background()
	c, err := Dial(ctx, wss(srv, "/x?ticket=stk_secretsecret"), o)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WriteText(ctx, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if k, data, err := c.Read(ctx); err != nil || k != Binary || string(data) != "bin:hi" {
		t.Fatalf("read %v %q %v", k, data, err)
	}
	if k, data, err := c.Read(ctx); err != nil || k != Text || string(data) != "text" {
		t.Fatalf("read %v %q %v", k, data, err)
	}
	if _, _, err := c.Read(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("after the close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"Sec-Websocket-Protocol", "Sec-Websocket-Extensions", "Origin", "Cookie", "Authorization"} {
		if v := seen.Get(h); v != "" {
			t.Errorf("the upgrade carried %s: %q", h, v)
		}
	}
	if seen.Get("User-Agent") != "sash-connect/test sah/3" {
		t.Errorf("User-Agent %q", seen.Get("User-Agent"))
	}
}

// A redirect at the upgrade is never followed: the dial fails with the
// status, the second server receives nothing, and the error holds no URL.
func TestDialNeverFollowsARedirect(t *testing.T) {
	var hits atomic.Int32
	second := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer second.Close()
	for _, status := range []int{301, 302, 303, 307, 308} {
		first := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, second.URL+"/api?"+r.URL.RawQuery, status)
		}))
		o := opts(first)
		o.TLS.RootCAs.AddCert(second.Certificate())
		_, err := Dial(context.Background(), wss(first, "/x?ticket=stk_secretsecret"), o)
		var he *HandshakeError
		if !errors.As(err, &he) || he.Status != status {
			t.Errorf("%d: %v", status, err)
		}
		if err != nil && strings.Contains(err.Error(), "stk_") {
			t.Errorf("the error holds the URL: %v", err)
		}
		first.Close()
	}
	if hits.Load() != 0 {
		t.Errorf("the redirect target received %d requests", hits.Load())
	}
}

// Refusals: another status, a server that never answers the upgrade, a
// certificate that does not verify, a TLS 1.2 server, and a message over
// the read limit.
func TestDialRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("status", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
		defer srv.Close()
		_, err := Dial(ctx, wss(srv, "/"), opts(srv))
		var he *HandshakeError
		if !errors.As(err, &he) || he.Status != 403 {
			t.Fatalf("%v", err)
		}
	})
	t.Run("stall before the upgrade", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
		defer srv.Close()
		defer close(release)
		o := opts(srv)
		o.HandshakeTimeout = 300 * time.Millisecond
		start := time.Now()
		_, err := Dial(ctx, wss(srv, "/"), o)
		if !errors.Is(err, ErrHandshake) || time.Since(start) > 3*time.Second {
			t.Fatalf("%v after %v", err, time.Since(start))
		}
	})
	t.Run("unknown certificate", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer srv.Close()
		o := opts(srv)
		o.TLS.RootCAs = x509.NewCertPool()
		if _, err := Dial(ctx, wss(srv, "/"), o); !errors.Is(err, ErrHandshake) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("TLS 1.2 server", func(t *testing.T) {
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		srv.TLS = &tls.Config{MaxVersion: tls.VersionTLS12}
		srv.StartTLS()
		defer srv.Close()
		if _, err := Dial(ctx, wss(srv, "/"), opts(srv)); !errors.Is(err, ErrHandshake) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("oversized message", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := wstest.Accept(w, r, 64<<10)
			if err != nil {
				return
			}
			if err := c.WriteBinary(r.Context(), make([]byte, 5000)); err != nil {
				return
			}
			<-r.Context().Done()
		}))
		defer srv.Close()
		o := opts(srv)
		o.ReadLimit = 4096
		c, err := Dial(ctx, wss(srv, "/"), o)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if _, _, err := c.Read(ctx); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a read is bounded by its context", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := wstest.Accept(w, r, 64<<10); err != nil {
				return
			}
			<-r.Context().Done()
		}))
		defer srv.Close()
		c, err := Dial(ctx, wss(srv, "/"), opts(srv))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		rctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		if _, _, err := c.Read(rctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%v", err)
		}
	})
}

// The configuration Dial refuses before any packet.
func TestDialRefusedConfig(t *testing.T) {
	good := func() Options {
		return Options{TLS: &tls.Config{MinVersion: tls.VersionTLS13}, HandshakeTimeout: time.Second, ReadLimit: 1}
	}
	for name, c := range map[string]struct {
		url string
		o   func() Options
	}{
		"ws scheme":      {"ws://127.0.0.1:1/", good},
		"http scheme":    {"http://127.0.0.1:1/", good},
		"https scheme":   {"https://127.0.0.1:1/", good},
		"userinfo":       {"wss://u:p@127.0.0.1:1/", good},
		"no host":        {"wss:///x", good},
		"no TLS config":  {"wss://127.0.0.1:1/", func() Options { o := good(); o.TLS = nil; return o }},
		"TLS 1.2":        {"wss://127.0.0.1:1/", func() Options { o := good(); o.TLS.MinVersion = tls.VersionTLS12; return o }},
		"TLS unset":      {"wss://127.0.0.1:1/", func() Options { o := good(); o.TLS.MinVersion = 0; return o }},
		"no timeout":     {"wss://127.0.0.1:1/", func() Options { o := good(); o.HandshakeTimeout = 0; return o }},
		"no read limit":  {"wss://127.0.0.1:1/", func() Options { o := good(); o.ReadLimit = 0; return o }},
		"unparsable URL": {"wss://[::1/", good},
	} {
		if _, err := Dial(context.Background(), c.url, c.o()); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The module is confined: only this package and wstest import
// github.com/coder/websocket, only _test files import wstest, only
// internal/connect imports this package, and neither this package nor
// connect names the field that turns certificate verification off.
func TestModuleIsConfined(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	skipField := "Insecure" + "SkipVerify"
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		inWS := strings.HasPrefix(rel, "internal/wsclient/")
		inConnect := strings.HasPrefix(rel, "internal/connect/")
		isTest := strings.HasSuffix(rel, "_test.go")
		if inWS || inConnect {
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(src), skipField) {
				t.Errorf("%s names %s", rel, skipField)
			}
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			switch {
			case strings.HasPrefix(p, "github.com/coder/websocket") && !inWS:
				t.Errorf("%s imports %s", rel, p)
			case strings.HasSuffix(p, "internal/wsclient/wstest") && !isTest:
				t.Errorf("%s imports wstest outside a test", rel)
			case strings.HasSuffix(p, "internal/wsclient") && !inConnect && !inWS:
				t.Errorf("%s imports wsclient", rel)
			case strings.Contains(p, "afterdarksys/adssh"):
				t.Errorf("%s imports adssh", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
