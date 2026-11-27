package connect

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/line"
	"github.com/afterdarksys/sash/internal/wsclient/wstest"
)

// testToken is a login token that is also a plain code token, so a server
// that echoes it as a code is caught.
const (
	testToken  = "sashtesttoken_0123456789abcdef"
	testHostID = "0a1b2c3d-0000-4000-8000-000000000001"
)

// TestMain runs the tests, or, as the child of a pty test, sash connect
// itself with the fake control plane's certificate as its only root.
//
// With SASH_CONNECT_TEST_SYSTEM_ROOTS=1 instead, the child trusts the
// platform's roots, as sash connect itself does.
func TestMain(m *testing.M) {
	path := os.Getenv("SASH_CONNECT_TEST_ROOTS")
	if path != "" || os.Getenv("SASH_CONNECT_TEST_SYSTEM_ROOTS") == "1" {
		if path != "" {
			b, err := os.ReadFile(path)
			if err != nil {
				os.Exit(97)
			}
			testRoots = x509.NewCertPool()
			if !testRoots.AppendCertsFromPEM(b) {
				os.Exit(97)
			}
		}
		con := OSConsole(os.Stdin, os.Stdout, os.Stderr)
		if os.Getenv("SASH_CONNECT_TEST_PANIC") == "1" {
			con.Out = &panicOnce{w: con.Out, at: "BOOM"}
		}
		res := Run(os.Args[1:], os.Getenv, con, "test")
		if res.Signal != nil {
			line.EndBySignal(res.Signal, res.RestoreErr)
		}
		os.Exit(res.Code)
	}
	os.Exit(m.Run())
}

// seenReq is one request the fake control plane received.
type seenReq struct {
	Method, Path, Query string
	Header              http.Header
	Body                string
}

// fakeCP is the control plane: resolve, ws-ticket, and the terminal
// WebSocket, with tickets that are single use, 60 s, and bound to a host
// and a purpose, as the real one's are.
type fakeCP struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	seen     []seenReq
	tickets  map[string]string
	issued   []string
	purposes []string
	upgrades int

	// Replaceable behaviour; nil is the default.
	resolve  func(w http.ResponseWriter, r *http.Request)
	ticket   func(w http.ResponseWriter, r *http.Request) bool
	upgrade  func(w http.ResponseWriter, r *http.Request) bool
	terminal func(ctx context.Context, c *wstest.Conn, r *http.Request)
	hostname any
}

func newFakeCP(t *testing.T) *fakeCP {
	t.Helper()
	cp := &fakeCP{t: t, tickets: map[string]string{}, hostname: "web1"}
	cp.srv = httptest.NewTLSServer(http.HandlerFunc(cp.serve))
	t.Cleanup(cp.srv.Close)
	testRoots = x509.NewCertPool()
	testRoots.AddCert(cp.srv.Certificate())
	return cp
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return
	}
}

func (cp *fakeCP) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return
	}
	cp.mu.Lock()
	cp.seen = append(cp.seen, seenReq{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), string(body)})
	cp.mu.Unlock()
	authed := r.Header.Get("Authorization") == "Bearer "+testToken
	switch {
	case r.URL.Path == "/api/v1/hosts/resolve" && r.Method == http.MethodGet:
		if cp.resolve != nil {
			cp.resolve(w, r)
			return
		}
		if !authed {
			writeJSON(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		writeJSON(w, 200, map[string]any{"host_id": testHostID, "hostname": cp.hostname, "tenant": "t1", "status": "online", "health": map[string]any{"new_field": 1}})
	case r.URL.Path == "/api/v1/ws-ticket" && r.Method == http.MethodPost:
		var req struct {
			HostID  string `json:"host_id"`
			Purpose string `json:"purpose"`
		}
		if json.Unmarshal(body, &req) != nil {
			writeJSON(w, 400, map[string]string{"error": "bad_purpose"})
			return
		}
		cp.mu.Lock()
		cp.purposes = append(cp.purposes, req.Purpose)
		n := len(cp.purposes)
		cp.mu.Unlock()
		if cp.ticket != nil && cp.ticket(w, r) {
			return
		}
		if !authed {
			writeJSON(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		if req.Purpose != "terminal:open" && req.Purpose != "terminal:root" {
			writeJSON(w, 400, map[string]string{"error": "bad_purpose"})
			return
		}
		tk := fmt.Sprintf("stk_fakeTicket-%04d_abcdefgh", n)
		cp.mu.Lock()
		cp.tickets[tk] = req.HostID + "|" + req.Purpose
		cp.issued = append(cp.issued, tk)
		cp.mu.Unlock()
		writeJSON(w, 200, map[string]any{"ticket": tk, "expires_in": 60, "host_id": req.HostID, "purpose": req.Purpose})
	case strings.HasPrefix(r.URL.Path, "/api/v1/systems/terminal/"):
		cp.mu.Lock()
		cp.upgrades++
		cp.mu.Unlock()
		if cp.upgrade != nil && cp.upgrade(w, r) {
			return
		}
		q := r.URL.Query()
		if q.Has("api_key") {
			w.WriteHeader(401)
			return
		}
		c, err := wstest.Accept(w, r, 64<<10)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		hostID := strings.TrimPrefix(r.URL.Path, "/api/v1/systems/terminal/")
		cp.mu.Lock()
		bound, ok := cp.tickets[q.Get("ticket")]
		delete(cp.tickets, q.Get("ticket"))
		cp.mu.Unlock()
		if !ok || bound != hostID+"|"+q.Get("purpose") {
			sendFrame(ctx, c, `{"type":"pty_status","payload":{"error":"ticket_invalid"}}`)
			closeWith(c, 1011)
			return
		}
		if cp.terminal != nil {
			cp.terminal(ctx, c, r)
		}
		closeWith(c, 1000)
	default:
		writeJSON(w, 404, map[string]string{"error": "not_found"})
	}
}

func sendFrame(ctx context.Context, c *wstest.Conn, text string) {
	if err := c.WriteText(ctx, []byte(text)); err != nil {
		return
	}
}

func sendBytes(ctx context.Context, c *wstest.Conn, b []byte) {
	if err := c.WriteBinary(ctx, b); err != nil {
		return
	}
}

func closeWith(c *wstest.Conn, code int) {
	if err := c.Close(code); err != nil {
		return
	}
}

// start sends the two status frames of a session that opens.
func start(ctx context.Context, c *wstest.Conn) {
	sendFrame(ctx, c, `{"type":"pty_status","payload":{"status":"starting"}}`)
	sendFrame(ctx, c, `{"type":"pty_status","payload":{"status":"started","session_id":"s1"}}`)
}

// clientFrame is one text frame the client sent.
type clientFrame struct {
	Type    string `json:"type"`
	DataB64 string `json:"data_b64"`
	Rows    int    `json:"rows"`
	Cols    int    `json:"cols"`
}

// readClient reads the client's next frame; ok is false when the
// connection ended.
func readClient(ctx context.Context, c *wstest.Conn) (clientFrame, bool) {
	binary, data, err := c.Read(ctx)
	if err != nil || binary {
		return clientFrame{}, false
	}
	var f clientFrame
	if json.Unmarshal(data, &f) != nil {
		return clientFrame{Type: "not json"}, true
	}
	return f, true
}

// inputUntil reads pty_input frames until what they carry holds want.
func inputUntil(ctx context.Context, c *wstest.Conn, want string) (string, bool) {
	var got []byte
	for !strings.Contains(string(got), want) {
		f, ok := readClient(ctx, c)
		if !ok {
			return string(got), false
		}
		if f.Type != "pty_input" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(f.DataB64)
		if err != nil {
			return string(got), false
		}
		got = append(got, b...)
	}
	return string(got), true
}

func (cp *fakeCP) requests() []seenReq {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return append([]seenReq(nil), cp.seen...)
}

func (cp *fakeCP) pem(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "roots.pem")
	b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cp.srv.Certificate().Raw})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// tokenFile writes the login token as the systemapi CLI's login does.
func tokenFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// safeBuf is a buffer two goroutines may use.
type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fakeCon is a terminal: keys are written to keys, the screen is out.
type fakeCon struct {
	keys     *io.PipeWriter
	in       *io.PipeReader
	out, err safeBuf
	notTerm  bool
	rows     atomic.Int32
	cols     atomic.Int32
	sig      chan os.Signal
	raws     atomic.Int32
	restores atomic.Int32
	stops    atomic.Int32
}

func newFakeCon() *fakeCon {
	c := &fakeCon{sig: make(chan os.Signal, 64)}
	c.in, c.keys = io.Pipe()
	c.rows.Store(40)
	c.cols.Store(120)
	return c
}

func (c *fakeCon) console() Console {
	return Console{
		In: c.in, Out: &c.out, Err: &c.err,
		IsTerm: func() bool { return !c.notTerm },
		Size:   func() (int, int) { return int(c.rows.Load()), int(c.cols.Load()) },
		Raw: func() (func() error, error) {
			c.raws.Add(1)
			var once sync.Once
			return func() error { once.Do(func() { c.restores.Add(1) }); return nil }, nil
		},
		Signals: func() (<-chan os.Signal, func()) { return c.sig, func() {} },
		Stop:    func() { c.stops.Add(1) },
	}
}

// env is the environment of one run; it records every name asked for.
type env struct {
	mu    sync.Mutex
	vars  map[string]string
	asked []string
}

func (e *env) get(name string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.asked = append(e.asked, name)
	return e.vars[name]
}

func goodEnv(t *testing.T, cp *fakeCP) *env {
	return &env{vars: map[string]string{"SYSTEMAPI_URL": cp.srv.URL, "SYSTEMAPI_TOKEN_FILE": tokenFile(t)}}
}

// running is a run in flight.
type running struct {
	done chan Result
}

func begin(con *fakeCon, e *env, args ...string) *running {
	r := &running{done: make(chan Result, 1)}
	go func() { r.done <- Run(args, e.get, con.console(), "v1.2.3") }()
	return r
}

func (r *running) wait(t *testing.T) Result {
	t.Helper()
	select {
	case res := <-r.done:
		return res
	case <-time.After(15 * time.Second):
		t.Fatal("sash connect did not end")
		return Result{}
	}
}

var stkRe = regexp.MustCompile(`stk_[A-Za-z0-9_-]{8,}`)

// checkClean is run after every test's run: the token and the tickets are
// in nothing the client printed and in no URL; the upgrade carried no
// credential header; the bearer went only to the two calls; and raw mode
// was entered at most once and restored as often as entered.
func checkClean(t *testing.T, cp *fakeCP, con *fakeCon) {
	t.Helper()
	printed := con.out.String() + con.err.String()
	if strings.Contains(printed, testToken) {
		t.Errorf("the token was printed: %q", printed)
	}
	if stkRe.MatchString(printed) {
		t.Errorf("a ticket was printed: %q", printed)
	}
	if cp != nil {
		for _, q := range cp.requests() {
			if strings.Contains(q.Path+"?"+q.Query, testToken) {
				t.Errorf("the token is in a URL: %s %s", q.Method, q.Path)
			}
			if strings.Contains(q.Query, "api_key") {
				t.Errorf("api_key in a query: %s", q.Path)
			}
			terminal := strings.HasPrefix(q.Path, "/api/v1/systems/terminal/")
			call := q.Path == "/api/v1/hosts/resolve" || q.Path == "/api/v1/ws-ticket"
			if auth := q.Header.Get("Authorization"); (auth != "") != call || (call && auth != "Bearer "+testToken) {
				t.Errorf("%s %s: Authorization %v", q.Method, q.Path, auth != "")
			}
			for _, h := range []string{"X-Api-Key", "Cookie", "Origin", "Sec-Websocket-Protocol", "Sec-Websocket-Extensions"} {
				if q.Header.Get(h) != "" {
					t.Errorf("%s %s carried %s", q.Method, q.Path, h)
				}
			}
			if ua := q.Header.Get("User-Agent"); ua != "sash-connect/v1.2.3 sah/3" && ua != "sash-connect/test sah/3" {
				t.Errorf("%s: User-Agent %q", q.Path, ua)
			}
			if terminal && strings.Contains(q.Header.Get("User-Agent"), testToken) {
				t.Errorf("the token is in the upgrade")
			}
		}
	}
	// Raw mode is entered once, and once more after each stop.
	if raws, restores := con.raws.Load(), con.restores.Load(); raws > 1+con.stops.Load() || raws != restores {
		t.Errorf("raw mode entered %d times, restored %d", raws, restores)
	}
}

// panicOnce panics the first time a write holds at, and passes every
// write on otherwise: a fault in the session loop while raw mode is on.
type panicOnce struct {
	w     io.Writer
	at    string
	fired atomic.Bool
}

func (p *panicOnce) Write(b []byte) (int, error) {
	if strings.Contains(string(b), p.at) && p.fired.CompareAndSwap(false, true) {
		panic("test fault")
	}
	return p.w.Write(b)
}
