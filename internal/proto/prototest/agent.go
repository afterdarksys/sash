// Package prototest is a fake systemapi agent for tests: the verb pipe's
// serial protocol, and the mux protocol of
// docs/plans/2026-11-14-sash-mux-pipe.md (hello, ids, at most Max
// outstanding, cancel of a queued request, whole-frame writes). It is an
// implementation of its own, independent of proto's, so a test of the
// client against it checks both sides of the wire.
package prototest

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/afterdarksys/sash/internal/parent"
)

const maxFrame = 8 << 20

// Agent serves one connection. Max is its mux limit M; 0 is an agent
// without mux, which answers hello (and any unknown field, mode
// included) with {"error":"frame body"}. Hold, when set, may return a
// gate for a request: the request stays queued (and cancellable) until the
// gate closes. Handle answers a started request.
type Agent struct {
	Max    int
	Hold   func(v parent.Verb) <-chan struct{}
	Handle func(v parent.Verb) (parent.Result, error)

	writeMu sync.Mutex
	mu      sync.Mutex
	mux     bool
	lastID  uint64
	open    map[uint64]*req
	running int
	maxRun  int
	reqs    int
	hellos  int
	cancels []uint64
	ran     []string
	err     error
	wg      sync.WaitGroup
}

type req struct {
	started bool
	cancel  chan struct{}
}

type wireReq struct {
	ID   *uint64  `json:"id,omitempty"`
	Key  string   `json:"key"`
	Argv []string `json:"argv"`
	Path string   `json:"path"`
	Body string   `json:"body"`
	Mode string   `json:"mode,omitempty"`
}

type oldReq struct {
	Key  string   `json:"key"`
	Argv []string `json:"argv"`
	Path string   `json:"path"`
	Body string   `json:"body"`
}

// Serve runs until conn closes, then waits for every started request.
func (a *Agent) Serve(conn net.Conn) {
	defer a.wg.Wait()
	a.mu.Lock()
	a.open = map[uint64]*req{}
	a.mu.Unlock()
	for {
		body, err := readFrame(conn)
		if err != nil {
			return
		}
		a.mu.Lock()
		mux := a.mux
		a.mu.Unlock()
		if !mux {
			a.serial(conn, body)
			continue
		}
		var c struct {
			Cancel uint64 `json:"cancel"`
		}
		if strict(body, &c) == nil {
			a.cancel(c.Cancel)
			continue
		}
		var r wireReq
		if strict(body, &r) != nil || r.ID == nil {
			a.fail(errors.New("frame shape"))
			a.write(conn, map[string]any{"id": 0, "error": "mux_protocol"})
			return
		}
		id := *r.ID
		a.mu.Lock()
		if id <= a.lastID || id > 1<<32-1 {
			a.mu.Unlock()
			a.fail(errors.New("id not increasing"))
			a.write(conn, map[string]any{"id": 0, "error": "mux_protocol"})
			return
		}
		a.lastID = id
		if len(a.open) >= a.Max {
			a.mu.Unlock()
			a.fail(errors.New("over limit"))
			a.write(conn, map[string]any{"id": id, "error": "mux_over_limit"})
			continue
		}
		q := &req{cancel: make(chan struct{})}
		a.open[id] = q
		a.reqs++
		a.mu.Unlock()
		a.wg.Add(1)
		go a.work(conn, id, verbOf(r), q)
	}
}

func (a *Agent) serial(conn net.Conn, body []byte) {
	if a.Max > 0 {
		var h struct {
			Mux struct {
				V    int      `json:"v"`
				Max  int      `json:"max"`
				Need []string `json:"need"`
			} `json:"mux"`
		}
		if strict(body, &h) == nil && h.Mux.V == 1 {
			a.mu.Lock()
			a.mux = true
			a.hellos++
			a.mu.Unlock()
			a.write(conn, map[string]any{"mux": map[string]any{"v": 1, "max": min(a.Max, h.Mux.Max), "have": []string{"mode"}}})
			return
		}
	}
	var v parent.Verb
	if a.Max > 0 {
		var r wireReq
		if strict(body, &r) != nil || r.ID != nil {
			a.write(conn, map[string]string{"error": "frame body"})
			return
		}
		v = verbOf(r)
	} else {
		var r oldReq
		if strict(body, &r) != nil {
			if bytes.Contains(body, []byte(`"mux"`)) {
				a.mu.Lock()
				a.hellos++
				a.mu.Unlock()
			}
			a.write(conn, map[string]string{"error": "frame body"})
			return
		}
		v = verbOf(wireReq{Key: r.Key, Argv: r.Argv, Path: r.Path, Body: r.Body})
	}
	a.mu.Lock()
	a.reqs++
	a.mu.Unlock()
	if err := writeRule(v); err != nil {
		a.write(conn, result(nil, parent.Result{}, err))
		return
	}
	a.mu.Lock()
	a.running++
	a.maxRun = max(a.maxRun, a.running)
	a.ran = append(a.ran, label(v))
	a.mu.Unlock()
	res, err := a.Handle(v)
	a.mu.Lock()
	a.running--
	a.mu.Unlock()
	a.write(conn, result(nil, res, err))
}

// writeRule is the agent's own check on a file.write verb
// (systemapi-agent sash_verb.go, sashDoFileWrite): exactly argv [">",
// path] for the verb's path, else denied, before any handler runs.
func writeRule(v parent.Verb) error {
	if v.Key != "file.write" {
		return nil
	}
	if v.Path == "" || len(v.Argv) != 2 || v.Argv[0] != ">" || v.Argv[1] != v.Path {
		return errors.New("denied")
	}
	return nil
}

func (a *Agent) work(conn net.Conn, id uint64, v parent.Verb, q *req) {
	defer a.wg.Done()
	if a.Hold != nil {
		if gate := a.Hold(v); gate != nil {
			select {
			case <-gate:
			case <-q.cancel:
				a.mu.Lock()
				delete(a.open, id)
				a.mu.Unlock()
				a.write(conn, map[string]any{"id": id, "error": "cancelled"})
				return
			}
		}
	}
	if err := writeRule(v); err != nil {
		a.mu.Lock()
		delete(a.open, id)
		a.mu.Unlock()
		a.write(conn, result(&id, parent.Result{}, err))
		return
	}
	a.mu.Lock()
	q.started = true
	a.running++
	a.maxRun = max(a.maxRun, a.running)
	a.ran = append(a.ran, label(v))
	a.mu.Unlock()
	res, err := a.Handle(v)
	a.mu.Lock()
	a.running--
	delete(a.open, id)
	a.mu.Unlock()
	a.write(conn, result(&id, res, err))
}

func (a *Agent) cancel(id uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id == 0 || id > a.lastID {
		if a.err == nil {
			a.err = errors.New("cancel for an id never issued")
		}
		return
	}
	a.cancels = append(a.cancels, id)
	if q, ok := a.open[id]; ok && !q.started {
		select {
		case <-q.cancel:
		default:
			close(q.cancel)
		}
	}
}

func (a *Agent) fail(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err == nil {
		a.err = err
	}
}

func (a *Agent) write(conn net.Conn, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		a.fail(err)
		return
	}
	buf := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(buf, uint32(len(body)))
	copy(buf[4:], body)
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if _, err := conn.Write(buf); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		a.fail(err)
	}
}

// Stats is what the agent saw.
type Stats struct {
	// Requests is every verb request; MaxRunning the most handlers
	// running at once; Hellos the hellos received; Cancels the ids
	// cancelled; Ran the "<key> <target>" of each started request.
	Requests, MaxRunning, Hellos, Running, Open int
	Cancels                                     []uint64
	Ran                                         []string
	Err                                         error
}

// Stats is a snapshot.
func (a *Agent) Stats() Stats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return Stats{Requests: a.reqs, MaxRunning: a.maxRun, Hellos: a.hellos, Running: a.running, Open: len(a.open),
		Cancels: append([]uint64(nil), a.cancels...), Ran: append([]string(nil), a.ran...), Err: a.err}
}

func label(v parent.Verb) string {
	target := v.Path
	if target == "" && len(v.Argv) > 2 {
		target = v.Argv[2]
	}
	return v.Key + " " + target
}

func result(id *uint64, res parent.Result, callErr error) map[string]any {
	out := map[string]any{"kind": "bytes", "exit": res.Exit}
	if res.Kind == parent.KindRows {
		out["kind"], out["rows"], out["cols"] = "rows", res.Rows, res.Cols
	}
	if len(res.Bytes) > 0 {
		out["bytes"] = base64.StdEncoding.EncodeToString(res.Bytes)
	}
	if callErr != nil {
		out = map[string]any{"error": callErr.Error()}
	}
	if id != nil {
		out["id"] = *id
	}
	return out
}

func verbOf(r wireReq) parent.Verb {
	v := parent.Verb{Key: r.Key, Argv: r.Argv, Path: r.Path, Mode: r.Mode}
	if r.Body != "" {
		if b, err := base64.StdEncoding.DecodeString(r.Body); err == nil {
			v.Body = b
		}
	}
	return v
}

func strict(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing")
	}
	return nil
}

func readFrame(r io.Reader) ([]byte, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(n[:])
	if size > maxFrame {
		return nil, errors.New("frame too large")
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}
