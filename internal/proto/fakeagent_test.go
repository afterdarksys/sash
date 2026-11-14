package proto

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/afterdarksys/sash/internal/parent"
)

// fakeAgent is the agent side of the mux contract, for tests: hello, ids
// strictly increasing, at most max outstanding (more is mux_over_limit),
// slots workers (a request past them is queued and cancellable), cancel,
// and whole-frame writes under one mutex. Before a hello it serves today's
// serial protocol.
type fakeAgent struct {
	conn   net.Conn
	max    int
	slots  chan struct{}
	handle func(v parent.Verb) (parent.Result, error)

	writeMu sync.Mutex
	mu      sync.Mutex
	lastID  uint64
	open    map[uint64]*fakeReq // outstanding: queued or started
	frames  [][]byte
	maxSeen int
	wg      sync.WaitGroup
	err     error // a protocol violation by the client
}

type fakeReq struct {
	started bool
	cancel  chan struct{}
}

func newFakeAgent(conn net.Conn, max, slots int, h func(parent.Verb) (parent.Result, error)) *fakeAgent {
	return &fakeAgent{conn: conn, max: max, slots: make(chan struct{}, slots), handle: h, open: map[uint64]*fakeReq{}}
}

func (a *fakeAgent) write(v any) {
	body, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	a.writeRaw(body)
}

func (a *fakeAgent) writeRaw(body []byte) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	buf := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(buf, uint32(len(body)))
	copy(buf[4:], body)
	if _, err := a.conn.Write(buf); err != nil {
		a.fail(err)
	}
}

func (a *fakeAgent) fail(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err == nil {
		a.err = err
	}
}

func (a *fakeAgent) result(id uint64, res parent.Result, callErr error) map[string]any {
	out := map[string]any{"id": id, "kind": "bytes", "exit": res.Exit}
	if res.Kind == parent.KindRows {
		out["kind"] = "rows"
		out["rows"], out["cols"] = res.Rows, res.Cols
	}
	if len(res.Bytes) > 0 {
		out["bytes"] = base64.StdEncoding.EncodeToString(res.Bytes)
	}
	if callErr != nil {
		out = map[string]any{"id": id, "error": callErr.Error()}
	}
	return out
}

// serve runs until the connection closes, then waits for its workers.
func (a *fakeAgent) serve() {
	defer a.wg.Wait()
	mux := false
	for {
		body, err := readBody(a.conn)
		if err != nil {
			return
		}
		a.mu.Lock()
		a.frames = append(a.frames, body)
		a.mu.Unlock()
		if !mux {
			var h helloReq
			dec := json.NewDecoder(bytes.NewReader(body))
			dec.DisallowUnknownFields()
			if dec.Decode(&h) == nil && h.Mux.V == 1 {
				mux = true
				a.write(helloResp{Mux: &helloBody{V: 1, Max: a.max, Have: []string{"mode"}}})
				continue
			}
			var req request
			if err := json.Unmarshal(body, &req); err != nil {
				a.write(response{Error: "frame body"})
				continue
			}
			res, callErr := a.handle(verbOf(req))
			resp := response{Kind: "bytes", Exit: res.Exit}
			if len(res.Bytes) > 0 {
				resp.Bytes = base64.StdEncoding.EncodeToString(res.Bytes)
			}
			if callErr != nil {
				resp.Error = callErr.Error()
			}
			a.write(resp)
			continue
		}
		var c muxCancel
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if dec.Decode(&c) == nil {
			a.cancel(c.Cancel)
			continue
		}
		var req muxRequest
		dec = json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			a.fail(err)
			a.write(map[string]any{"id": 0, "error": "mux_protocol"})
			return
		}
		a.mu.Lock()
		if req.ID <= a.lastID {
			a.mu.Unlock()
			a.fail(errors.New("id not increasing"))
			a.write(map[string]any{"id": 0, "error": "mux_protocol"})
			return
		}
		a.lastID = req.ID
		if len(a.open) >= a.max {
			a.mu.Unlock()
			a.fail(errors.New("over limit"))
			a.write(map[string]any{"id": req.ID, "error": "mux_over_limit"})
			continue
		}
		r := &fakeReq{cancel: make(chan struct{})}
		a.open[req.ID] = r
		a.maxSeen = max(a.maxSeen, len(a.open))
		a.mu.Unlock()
		a.wg.Add(1)
		go a.work(req, r)
	}
}

func (a *fakeAgent) work(req muxRequest, r *fakeReq) {
	defer a.wg.Done()
	select {
	case a.slots <- struct{}{}:
	case <-r.cancel:
		a.mu.Lock()
		delete(a.open, req.ID)
		a.mu.Unlock()
		a.write(map[string]any{"id": req.ID, "error": "cancelled"})
		return
	}
	a.mu.Lock()
	r.started = true
	a.mu.Unlock()
	res, callErr := a.handle(verbOf(request{Key: req.Key, Argv: req.Argv, Path: req.Path, Body: req.Body, Mode: req.Mode}))
	<-a.slots
	// Answered and no longer outstanding before the frame goes out, so
	// the client may reuse the slot as soon as it reads it.
	a.mu.Lock()
	delete(a.open, req.ID)
	a.mu.Unlock()
	a.write(a.result(req.ID, res, callErr))
}

// cancel drops a queued request; a started or answered one is ignored; an
// id never issued is a protocol error.
func (a *fakeAgent) cancel(id uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id == 0 || id > a.lastID {
		if a.err == nil {
			a.err = errors.New("cancel for an id never issued")
		}
		return
	}
	if r, ok := a.open[id]; ok && !r.started {
		select {
		case <-r.cancel:
		default:
			close(r.cancel)
		}
	}
}

func verbOf(req request) parent.Verb {
	v := parent.Verb{Key: req.Key, Argv: req.Argv, Path: req.Path, Mode: req.Mode}
	if req.Body != "" {
		b, err := base64.StdEncoding.DecodeString(req.Body)
		if err == nil {
			v.Body = b
		}
	}
	return v
}

// oldAgent is an agent before mux: a strict decoder that answers any
// unknown field with {"error":"frame body"} and keeps serving.
func oldAgent(conn net.Conn, frames *[][]byte, mu *sync.Mutex, h func(parent.Verb) (parent.Result, error)) {
	for {
		body, err := readBody(conn)
		if err != nil {
			return
		}
		mu.Lock()
		*frames = append(*frames, body)
		mu.Unlock()
		// The frame before file.write mode: no mode field.
		var req struct {
			Key  string   `json:"key"`
			Argv []string `json:"argv"`
			Path string   `json:"path"`
			Body string   `json:"body"`
		}
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			if err := Write(conn, struct {
				Error string `json:"error"`
			}{"frame body"}); err != nil {
				return
			}
			continue
		}
		res, callErr := h(verbOf(request{Key: req.Key, Argv: req.Argv, Path: req.Path, Body: req.Body}))
		resp := response{Kind: "bytes", Exit: res.Exit}
		if len(res.Bytes) > 0 {
			resp.Bytes = base64.StdEncoding.EncodeToString(res.Bytes)
		}
		if callErr != nil {
			resp.Error = callErr.Error()
		}
		if err := Write(conn, resp); err != nil {
			return
		}
	}
}

// rawAgent is a scripted agent: the test reads and writes its frames.
type rawAgent struct {
	t    *testing.T
	conn net.Conn
}

func (r rawAgent) read() []byte {
	r.t.Helper()
	body, err := readBody(r.conn)
	if err != nil {
		r.t.Fatalf("agent read: %v", err)
	}
	return body
}

func (r rawAgent) send(s string) {
	r.t.Helper()
	buf := make([]byte, 4+len(s))
	binary.BigEndian.PutUint32(buf, uint32(len(s)))
	copy(buf[4:], s)
	if _, err := r.conn.Write(buf); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		r.t.Fatalf("agent write: %v", err)
	}
}

func echoPath(v parent.Verb) (parent.Result, error) {
	return parent.Result{Kind: parent.KindBytes, Bytes: []byte(v.Path)}, nil
}
