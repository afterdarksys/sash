package connect

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/afterdarksys/sash/internal/codes"
	"github.com/afterdarksys/sash/internal/lang"
	"github.com/afterdarksys/sash/internal/wsclient"
)

// sash connect [--root] <host>: resolve the host, ask for a ticket, open
// the terminal WebSocket, and carry the person's terminal to the host's
// sash and back. "sah/3" is this client's name for the control plane's
// existing terminal protocol, sent in User-Agent; nothing is negotiated,
// and a server that does not behave as the protocol says is refused.
//
// Threats: see api.go for the token and the two calls, tokenfile.go for
// the token file, and filter.go for the terminal stream. Here: a hostile
// control plane or host writing instructions or terminal sequences into
// what the client prints (the client prints only its own fixed lines; a
// server's code is shown only when it is a plain token, with this build's
// own meaning for it, and the server's message, fix or any other text is
// never printed; the host's name is printed only when it matches the
// control plane's hostname rule, and the id is escaped and cut; the token
// and the ticket are replaced in every server string before it is
// judged); a downgrade from a root session to a weaker one (--root asks
// for terminal:root and nothing else, and a refusal ends the run: there
// is no second request with another purpose); a server that stalls
// before or after the upgrade (every wait before the session starts is
// bounded); frames of any size (a message over 1 MiB ends the session),
// or frames that break the WebSocket rules (the session ends with
// protocol); and a terminal left in raw mode or in a mode the host set
// (it is restored, after resetTail, on every way out, SIGTERM, SIGHUP,
// SIGQUIT and a panic too, and while SIGTSTP has the client stopped;
// raw mode is entered again when it continues). The client holds no grant and no key, records nothing, and
// never speaks to an agent. It does not protect what the screen shows,
// which is the host's, nor a workstation whose user or root is hostile.

// Fixed lines. Each is in the connect area of package codes.
const (
	msgUsage         = "usage: sash connect [--root] <host>"
	msgNoTerminal    = "connect needs a terminal"
	msgBadURL        = "connect: invalid SYSTEMAPI_URL"
	msgBadHost       = "connect: invalid host name"
	msgTokenRejected = "connect: token_file_rejected"
	msgNoLogin       = "connect: no login; a person logs in with the systemapi CLI"
	msgRequestFailed = "connect: request_failed"
	msgRefused       = "connect refused: "
	msgNotSah3       = "connect: the server does not speak sah/3"
	msgClosed        = "connect: closed"
	msgInterrupted   = "connect: interrupted"
	msgEnded         = "session ended: "
	msgLost          = "session ended: connection lost"
	msgProtocol      = "session ended: protocol"
	msgSession       = "connect: session on "
	msgCredential    = "<credential>"
)

const (
	purposeOpen = "terminal:open"
	purposeRoot = "terminal:root"
	// readLimit is the largest message the client reads.
	readLimit = 1 << 20
	// inputMax is the most raw bytes one pty_input frame carries.
	inputMax = 16 << 10
	// resizeEvery is the least time between two pty_resize frames.
	resizeEvery = 100 * time.Millisecond
	nameMax     = 253
)

// sigResize is the window-size signal, sigStop the stop signal from the
// terminal or kill (SIGTSTP), and sigQuit SIGQUIT, where there are such.
var sigResize, sigStop, sigQuit os.Signal

// Console is the person's terminal.
type Console struct {
	In       io.Reader
	Out, Err io.Writer
	// IsTerm is true when stdin, stdout and stderr are all terminals.
	IsTerm func() bool
	// Size is the terminal's rows and columns.
	Size func() (rows, cols int)
	// Raw enters raw mode; restore puts the mode back, and acts once.
	Raw func() (restore func() error, err error)
	// Signals delivers interrupt, terminate, hangup, quit, stop and
	// window-size signals until stop is called.
	Signals func() (ch <-chan os.Signal, stop func())
	// Stop stops the process as SIGTSTP asks and returns once it is
	// continued (at once, if the process could not be stopped).
	Stop func()
}

// Result is how a run ended. Signal is set when SIGTERM or SIGHUP ended
// it: the caller then ends the process as that signal says.
type Result struct {
	Code       int
	Signal     os.Signal
	RestoreErr error
}

// run is one sash connect.
type run struct {
	con     Console
	token   string
	ticket  string
	filter  Filter
	restore func() error
	// ignored counts text frames of a type the client does not know,
	// after the session started.
	ignored int
}

// say prints one line on stderr. It is the only way the client prints,
// and the whole line goes through TermLine: nothing a server sent reaches
// the terminal as it came, and nothing in it can start a second line.
func (r *run) say(line string) {
	if _, err := io.WriteString(r.con.Err, lang.TermLine(line)+"\n"); err != nil {
		return
	}
}

// scrub replaces the token and the ticket in a string a server sent.
func (r *run) scrub(s string) string {
	for _, secret := range []string{r.token, r.ticket} {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, msgCredential)
		}
	}
	return s
}

// listed is a server's code as the client prints it: the code and this
// build's meaning for it, the code alone when this build does not list
// it, or "unlisted" when it is not a plain token. The server's own words
// are never part of it.
func (r *run) listed(code string) string {
	code = r.scrub(code)
	if !codeRe.MatchString(code) {
		return "unlisted"
	}
	if c, ok := codes.Connect(code); ok {
		return code + ": " + c.Meaning
	}
	return code + " (not in this sash's list)"
}

// name is a host name or id a server sent, with the credentials replaced
// and cut to nameMax bytes. say escapes it with the rest of its line.
func (r *run) name(s string) string {
	s = r.scrub(s)
	if len(s) > nameMax {
		s = s[:nameMax]
	}
	return s
}

func (r *run) refused(code string) Result {
	r.say(msgRefused + r.listed(code))
	return Result{Code: 126}
}

func (r *run) failed() Result {
	r.say(msgRequestFailed)
	return Result{Code: 1}
}

// parseArgs takes exactly [--root] <host>. A host that begins with - is a
// flag this mode does not have.
func parseArgs(args []string) (host string, root, ok bool) {
	if len(args) == 2 && args[0] == "--root" {
		root, args = true, args[1:]
	}
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return "", false, false
	}
	return args[0], root, true
}

// agentName is the User-Agent: the client's name, its version, and the
// protocol name.
func agentName(version string) string {
	clean := make([]byte, 0, len(version))
	for i := 0; i < len(version) && i < 64; i++ {
		c := version[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '+' || c == '_' {
			clean = append(clean, c)
		}
	}
	if len(clean) == 0 {
		clean = []byte("unknown")
	}
	return "sash-connect/" + string(clean) + " sah/3"
}

// Run is sash connect. args are the arguments after the word connect;
// getenv reads SYSTEMAPI_URL and SYSTEMAPI_TOKEN_FILE and nothing else.
func Run(args []string, getenv func(string) string, con Console, version string) Result {
	r := &run{con: con}
	// A panic puts the terminal back, reset tail and all, before it goes
	// on: nothing leaves the terminal raw.
	defer func() {
		if p := recover(); p != nil {
			if rerr := r.restoreTerm(); rerr != nil {
				r.say("sash: terminal not restored")
			}
			panic(p)
		}
	}()
	host, root, ok := parseArgs(args)
	if !ok {
		r.say(msgUsage)
		return Result{Code: 2}
	}
	if !con.IsTerm() {
		r.say(msgNoTerminal)
		return Result{Code: 2}
	}
	if !hostRe.MatchString(host) {
		r.say(msgBadHost)
		return Result{Code: 2}
	}
	base, ok := origin(getenv("SYSTEMAPI_URL"))
	if !ok {
		r.say(msgBadURL)
		return Result{Code: 2}
	}
	token, state := readToken(getenv("SYSTEMAPI_TOKEN_FILE"))
	switch state {
	case tokenNone:
		r.say(msgNoLogin)
		return Result{Code: 2}
	case tokenRejected:
		r.say(msgTokenRejected)
		return Result{Code: 2}
	}
	r.token = token

	// Until the socket is open, a signal ends the calls in flight.
	sigs, stopSignals := con.Signals()
	defer stopSignals()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var (
		early   os.Signal
		earlyMu sync.Mutex
		handed  = make(chan struct{})
		watched = make(chan struct{})
	)
	go func() {
		defer close(watched)
		for {
			select {
			case sig := <-sigs:
				if sig == sigResize {
					continue
				}
				if sig == sigStop {
					// Raw mode is not on yet: stop as a program does.
					con.Stop()
					continue
				}
				earlyMu.Lock()
				early = sig
				earlyMu.Unlock()
				cancel()
				return
			case <-handed:
				return
			}
		}
	}()
	interrupted := func() (Result, bool) {
		earlyMu.Lock()
		defer earlyMu.Unlock()
		if early == nil {
			return Result{}, false
		}
		return r.bySignal(early), true
	}
	handOver := func() {
		close(handed)
		<-watched
	}

	client := newAPI(base, token, agentName(version))
	found, how, code := client.resolve(ctx, host)
	if how == answerOK {
		purpose := purposeOpen
		if root {
			// A refusal of terminal:root ends the run. Nothing below
			// asks again with another purpose.
			purpose = purposeRoot
		}
		var ticket string
		ticket, how, code = client.ticket(ctx, found.HostID, purpose)
		if how == answerOK {
			r.ticket = ticket
			rows, cols := con.Size()
			conn, err := wsclient.Dial(ctx, terminalURL(base, found.HostID, ticket, purpose, clampSize(rows), clampSize(cols)), wsclient.Options{
				TLS: tlsConfig(base.Hostname()),
				// The upgrade carries no credential: the ticket in the
				// URL is all it needs.
				Header:           http.Header{"User-Agent": {agentName(version)}},
				HandshakeTimeout: handshakeTimeout,
				ReadLimit:        readLimit,
			})
			handOver()
			if res, was := interrupted(); was {
				if err == nil {
					r.closeConn(conn)
				}
				return res
			}
			if err != nil {
				var refusal *wsclient.HandshakeError
				if errors.As(err, &refusal) && (refusal.Status < 300 || refusal.Status >= 400) {
					// The upgrade was answered, and not with 101.
					r.say(msgNotSah3)
					return Result{Code: 1}
				}
				return r.failed()
			}
			return r.session(ctx, conn, sigs, found, rows, cols)
		}
	}
	handOver()
	if res, was := interrupted(); was {
		return res
	}
	if how == answerRefused {
		return r.refused(code)
	}
	return r.failed()
}

func clampSize(n int) int {
	if n < 1 {
		return 1
	}
	if n > 1000 {
		return 1000
	}
	return n
}

// bySignal is the end a signal asks for: the terminal is restored first.
// SIGQUIT ends with 131 and no core dump or stack trace.
func (r *run) bySignal(sig os.Signal) Result {
	rerr := r.restoreTerm()
	if sig == os.Interrupt {
		r.say(msgInterrupted)
		return r.done(Result{Code: 130}, rerr)
	}
	if sig == sigQuit {
		return r.done(Result{Code: 131}, rerr)
	}
	code := 143
	if sig == syscall.SIGHUP {
		code = 129
	}
	return Result{Code: code, Signal: sig, RestoreErr: rerr}
}

// resetTail undoes every persistent mode the filter lets a host set, on
// every way out of a session, whether or not the host set it: G0 back to
// ASCII (ESC ( 0 draws letters as boxes), the keypad numeric (ESC =),
// cursor keys normal (CSI ? 1 h), autowrap on (CSI ? 7 l), cursor blink
// off (CSI ? 12 h), the scroll region the whole screen (CSI r, between a
// cursor save and restore because it homes the cursor), colour and style
// plain, and the cursor shown. The alternate screen is left after it when
// the host entered it, and bracketed paste (CSI ? 2004) is turned off by
// the raw-mode guard (package line). Every other mode a host could set is
// dropped by the filter.
const resetTail = "\x1b(B" + "\x1b>" + "\x1b[?1l" + "\x1b[?7h" + "\x1b[?12l" + "\x1b7\x1b[r\x1b8" + "\x1b[0m" + "\x1b[?25h"

// restoreTerm puts the terminal back, when raw mode was entered: the
// reset tail, then the mode. The restore itself acts once; calling this
// again is harmless.
func (r *run) restoreTerm() error {
	if r.restore == nil {
		return nil
	}
	tail := resetTail
	if r.filter.AltScreen {
		tail += "\x1b[?1049l"
		r.filter.AltScreen = false
	}
	_, werr := io.WriteString(r.con.Out, tail)
	rerr := r.restore()
	if rerr == nil {
		rerr = werr
	}
	r.restore = nil
	return rerr
}

// suspend is SIGTSTP: the terminal is put back (reset tail and mode), the
// process stops, and once it is continued raw mode is entered again. It
// is false when raw mode cannot be entered again: the session then ends,
// closed, rather than run on a terminal in the wrong mode.
func (r *run) suspend() bool {
	wasRaw := r.restore != nil
	if wasRaw {
		if err := r.restoreTerm(); err != nil {
			r.say("sash: terminal not restored")
		}
	}
	r.con.Stop()
	if !wasRaw {
		return true
	}
	restore, err := r.con.Raw()
	if err != nil {
		return false
	}
	r.restore = restore
	return true
}

// done says when the terminal could not be restored.
func (r *run) done(res Result, restoreErr error) Result {
	if restoreErr != nil {
		r.say("sash: terminal not restored")
		res.RestoreErr = restoreErr
	}
	return res
}

func (r *run) closeConn(conn *wsclient.Conn) {
	if err := conn.Close(); err != nil {
		// The connection is gone either way; there is nothing to say.
		return
	}
}

// frame is one message from the server, or the end of the connection.
type frame struct {
	kind wsclient.Kind
	data []byte
	err  error
}

// serverFrame is a text frame of the terminal protocol.
type serverFrame struct {
	Type    string `json:"type"`
	Payload struct {
		Status *string         `json:"status"`
		Error  json.RawMessage `json:"error"`
		Code   json.RawMessage `json:"code"`
		Reason json.RawMessage `json:"reason"`
	} `json:"payload"`
}

// text is a JSON string, or "" with false when raw is anything else.
func text(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// session runs the open terminal until it ends.
func (r *run) session(ctx context.Context, conn *wsclient.Conn, sigs <-chan os.Signal, host resolved, rows, cols int) Result {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer r.closeConn(conn)

	frames := make(chan frame, 16)
	go func() {
		for {
			kind, data, err := conn.Read(ctx)
			select {
			case frames <- frame{kind, data, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	end := func(res Result, line string) Result {
		cancel()
		r.closeConn(conn)
		rerr := r.restoreTerm()
		if line != "" {
			r.say(line)
		}
		return r.done(res, rerr)
	}
	send := func(v any) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return false
		}
		wctx, done := context.WithTimeout(ctx, writeTimeout)
		defer done()
		return conn.WriteText(wctx, b) == nil
	}

	var (
		started   bool
		gotStatus bool
		input     chan []byte
		inputEnd  chan struct{}
		keys      escaper
		lastSize  = [2]int{clampSize(rows), clampSize(cols)}
		lastSent  time.Time
		resizeDue <-chan time.Time
	)
	// One deadline from the upgrade bounds the wait for started: the
	// first frame is due within firstFrameTimeout, and started within
	// startTimeout. A starting frame moves the wait to that deadline once;
	// more starting frames do not extend it.
	startBy := time.Now().Add(startTimeout)
	wait := time.NewTimer(firstFrameTimeout)
	defer wait.Stop()
	resize := func() bool {
		rw, cl := r.con.Size()
		now := [2]int{clampSize(rw), clampSize(cl)}
		if now == lastSize {
			return true
		}
		lastSize, lastSent = now, time.Now()
		return send(map[string]any{"type": "pty_resize", "rows": now[0], "cols": now[1]})
	}

	for {
		select {
		case <-wait.C:
			// The server stalled after the upgrade.
			return end(Result{Code: 1}, msgRequestFailed)

		case sig := <-sigs:
			if sig == sigResize {
				if !started || resizeDue != nil {
					continue
				}
				if since := time.Since(lastSent); since < resizeEvery {
					resizeDue = time.After(resizeEvery - since)
					continue
				}
				if !resize() {
					return end(Result{Code: 1}, msgLost)
				}
				continue
			}
			if sig == sigStop {
				if !r.suspend() {
					return end(Result{Code: 2}, msgNoTerminal)
				}
				// The window may have changed while stopped.
				if started && !resize() {
					return end(Result{Code: 1}, msgLost)
				}
				continue
			}
			cancel()
			r.closeConn(conn)
			return r.bySignal(sig)

		case <-resizeDue:
			resizeDue = nil
			if !resize() {
				return end(Result{Code: 1}, msgLost)
			}

		case p := <-input:
			out, quit := keys.feed(p)
			if quit {
				return end(Result{Code: 1}, msgClosed)
			}
			for len(out) > 0 {
				n := min(len(out), inputMax)
				if !send(map[string]string{"type": "pty_input", "data_b64": base64.StdEncoding.EncodeToString(out[:n])}) {
					return end(Result{Code: 1}, msgLost)
				}
				out = out[n:]
			}

		case <-inputEnd:
			inputEnd = nil
			if !send(map[string]string{"type": "pty_eof"}) {
				return end(Result{Code: 1}, msgLost)
			}

		case f := <-frames:
			if f.err != nil {
				switch {
				case errors.Is(f.err, wsclient.ErrTooLarge), errors.Is(f.err, wsclient.ErrProtocol):
					return end(Result{Code: 1}, msgProtocol)
				case !gotStatus:
					return end(Result{Code: 1}, msgNotSah3)
				}
				return end(Result{Code: 1}, msgLost)
			}
			if f.kind == wsclient.Binary {
				if !started {
					return end(Result{Code: 1}, msgNotSah3)
				}
				if _, err := r.con.Out.Write(r.filter.Write(f.data)); err != nil {
					return end(Result{Code: 1}, msgClosed)
				}
				continue
			}
			var msg serverFrame
			known := json.Unmarshal(f.data, &msg) == nil
			switch {
			case known && msg.Type == "pty_status" && len(msg.Payload.Error) > 0:
				code, ok := text(msg.Payload.Error)
				if !ok {
					code = ""
				}
				return end(Result{Code: 126}, msgRefused+r.listed(code))
			case known && msg.Type == "pty_status" && msg.Payload.Status != nil && *msg.Payload.Status == "starting":
				if !started && !gotStatus {
					wait.Reset(time.Until(startBy))
				}
				gotStatus = true
			case known && msg.Type == "pty_status" && msg.Payload.Status != nil && *msg.Payload.Status == "started":
				gotStatus = true
				if started {
					continue
				}
				started = true
				wait.Stop()
				// The host's name is printed only when it is a name the
				// control plane's own rule allows; otherwise the id alone.
				label := r.name(host.HostID)
				if hostnameRe.MatchString(host.Hostname) {
					label = r.name(host.Hostname) + " (" + label + ")"
				}
				r.say(msgSession + label)
				restore, err := r.con.Raw()
				if err != nil {
					return end(Result{Code: 2}, msgNoTerminal)
				}
				r.restore = restore
				input, inputEnd = make(chan []byte), make(chan struct{})
				go readInput(ctx, r.con.In, input, inputEnd)
			case known && msg.Type == "pty_exit" && (started || gotStatus):
				var code int
				if json.Unmarshal(msg.Payload.Code, &code) == nil && code >= 0 && code <= 255 {
					return end(Result{Code: code}, "")
				}
				reason, ok := text(msg.Payload.Reason)
				if !ok {
					reason = ""
				}
				return end(Result{Code: 1}, msgEnded+r.listed(reason))
			case !started:
				// The first frames were not the terminal protocol's.
				return end(Result{Code: 1}, msgNotSah3)
			default:
				r.ignored++
			}
		}
	}
}

// readInput carries the person's keys to the session loop, at most
// inputMax bytes at a time, and reports the end of input once.
func readInput(ctx context.Context, in io.Reader, input chan<- []byte, ended chan<- struct{}) {
	buf := make([]byte, inputMax)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			select {
			case input <- append([]byte(nil), buf[:n]...):
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			select {
			case ended <- struct{}{}:
			case <-ctx.Done():
			}
			return
		}
	}
}

// escaper finds the local escape in the person's keys: at the start of a
// line, ~. closes the session here and ~~ sends one ~.
type escaper struct {
	midLine bool
	tilde   bool
}

func (e *escaper) feed(p []byte) (out []byte, quit bool) {
	out = make([]byte, 0, len(p)+1)
	for _, b := range p {
		if e.tilde {
			e.tilde = false
			switch b {
			case '.':
				return out, true
			case '~':
				out = append(out, '~')
				e.midLine = true
				continue
			}
			out = append(out, '~')
			e.midLine = true
		}
		if !e.midLine && b == '~' {
			e.tilde = true
			continue
		}
		out = append(out, b)
		e.midLine = b != '\r' && b != '\n'
	}
	return out, false
}
