package proto

// The concurrent half of the verb pipe: docs/plans/2026-11-14-sash-mux-pipe.md
// sections 1, 2, and 4.
//
// Threats: a response routed to the wrong call (an unknown, repeated, or
// id-0 response, or one after the client broke), interleaved frames, an
// agent that answers hello with anything but the two exact shapes, an id
// space that wraps, and a call that waits forever on a dead pipe. Ids are
// allocated here, strictly increasing, under the write lock, so they reach
// the agent in order, and are never reused; exhaustion breaks the client.
// A response for an id not outstanding is never routed to another waiter:
// it breaks the client. Broken is set once; every outstanding call and
// every later call fails with ErrBroken. Every frame goes out whole under
// writeMu. Cancel never abandons a call: the agent answers every id, and
// the call waits for that answer, so "maybe ran" stays visible.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/afterdarksys/sash/internal/parent"
)

// ErrBroken is a verb pipe that can no longer be trusted: a framing error,
// an unknown response id, an over-limit or protocol reply, a malformed
// hello, or id exhaustion. Every call fails closed.
var ErrBroken = errors.New("verb pipe broken")

const (
	muxVersion = 1
	// muxMaxLimit is the largest in-flight limit sash offers and accepts.
	muxMaxLimit = 16
	// muxIDMax is the last request id; id 0 is connection-level.
	muxIDMax = 1<<32 - 1
)

// helloOld is the exact answer of an agent without mux: its strict
// decoder refuses the unknown field and keeps serving.
var helloOld = []byte(`{"error":"frame body"}`)

type muxState struct {
	// helloMu orders Concurrency calls; done and limit are its result.
	helloMu   sync.Mutex
	helloDone bool
	limit     int
	muxOn     atomic.Bool
	// writeMu covers id allocation and every whole-frame write.
	writeMu sync.Mutex
	nextID  atomic.Uint64 // the last id allocated
	idLimit uint64        // test seam; 0 is muxIDMax
	sem     chan struct{}
	pmu     sync.Mutex
	pending map[uint64]chan muxAnswer
	broken  error
}

type muxAnswer struct {
	resp response
	err  error
}

type helloReq struct {
	Mux helloBody `json:"mux"`
}

type helloBody struct {
	V    int      `json:"v"`
	Max  int      `json:"max"`
	Need []string `json:"need,omitempty"`
	Have []string `json:"have,omitempty"`
}

type helloResp struct {
	Mux *helloBody `json:"mux"`
}

type muxRequest struct {
	ID   uint64   `json:"id"`
	Key  string   `json:"key"`
	Argv []string `json:"argv"`
	Path string   `json:"path"`
	Body string   `json:"body"`
	Mode string   `json:"mode,omitempty"`
}

type muxResponse struct {
	ID    *uint64      `json:"id"`
	Kind  string       `json:"kind"`
	Bytes string       `json:"bytes"`
	Rows  []parent.Row `json:"rows"`
	Cols  []string     `json:"cols"`
	Exit  int          `json:"exit"`
	Error string       `json:"error"`
}

type muxCancel struct {
	Cancel uint64 `json:"cancel"`
}

// Concurrency is the effective N for n concurrent callers: min(n, M) once
// mux is on, or 1 when the agent has no mux. The first call sends hello
// (section 1); a session that never calls it never sends one, so its wire
// stays as before. A malformed answer breaks the client.
func (c *Client) Concurrency(n int) (int, error) {
	if n < 1 {
		return 0, fmt.Errorf("concurrency %d", n)
	}
	c.helloMu.Lock()
	defer c.helloMu.Unlock()
	if !c.helloDone {
		if err := c.hello(); err != nil {
			return 0, err
		}
	}
	if err := c.brokenErr(); err != nil {
		return 0, err
	}
	if !c.muxOn.Load() {
		return 1, nil
	}
	return min(n, c.limit), nil
}

// hello runs under mu, so no serial call is outstanding.
func (c *Client) hello() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.helloDone = true
	if err := c.brokenErr(); err != nil {
		return err
	}
	if err := c.writeWhole(helloReq{Mux: helloBody{V: muxVersion, Max: muxMaxLimit, Need: []string{"mode"}}}); err != nil {
		return c.breakWith(err)
	}
	body, err := readBody(c.RW)
	if err != nil {
		return c.breakWith(err)
	}
	if bytes.Equal(body, helloOld) {
		return nil
	}
	var r helloResp
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil || dec.More() || r.Mux == nil || r.Mux.V != muxVersion ||
		r.Mux.Max < 1 || r.Mux.Max > muxMaxLimit || len(r.Mux.Need) != 0 || !hasMode(r.Mux.Have) {
		return c.breakWith(errors.New("malformed hello"))
	}
	c.limit = r.Mux.Max
	c.sem = make(chan struct{}, c.limit)
	c.pmu.Lock()
	c.pending = map[uint64]chan muxAnswer{}
	c.pmu.Unlock()
	c.muxOn.Store(true)
	go c.readLoop()
	return nil
}

func hasMode(have []string) bool {
	for _, h := range have {
		if h == "mode" {
			return true
		}
	}
	return false
}

// DoContext is Do under ctx. With mux on, when ctx ends first it sends
// {"cancel":id} and still waits for the agent's answer: cancelled when
// nothing ran, else the verb's own result. Serial, it checks ctx only
// before the call, since a started serial verb cannot be cancelled.
func (c *Client) DoContext(ctx context.Context, v parent.Verb) (parent.Result, error) {
	if !c.muxOn.Load() {
		if err := ctx.Err(); err != nil {
			return parent.Result{}, err
		}
		return c.Do(v)
	}
	return c.doMux(ctx, v)
}

func (c *Client) doMux(ctx context.Context, v parent.Verb) (parent.Result, error) {
	if err := c.brokenErr(); err != nil {
		return parent.Result{}, err
	}
	// A waiting call blocks here, before it has an id.
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return parent.Result{}, ctx.Err()
	}
	defer func() { <-c.sem }()
	req := muxRequest{Key: v.Key, Argv: v.Argv, Path: v.Path, Mode: v.Mode}
	if len(v.Body) > 0 {
		req.Body = base64.StdEncoding.EncodeToString(v.Body)
	}
	id, ch, err := c.send(req)
	if err != nil {
		return parent.Result{}, err
	}
	var a muxAnswer
	select {
	case a = <-ch:
	case <-ctx.Done():
		// The agent answers every id; wait for it, so a verb that
		// already started is never reported as not run.
		if err := c.cancel(id); err != nil {
			return parent.Result{}, err
		}
		a = <-ch
	}
	if a.err != nil {
		return parent.Result{}, a.err
	}
	switch a.resp.Error {
	case "cancelled":
		return parent.Result{}, parent.ErrCancelled
	case "outcome_unknown":
		return parent.Result{}, parent.ErrOutcomeUnknown
	}
	return fromResponse(v, a.resp)
}

// send allocates the next id, registers it, and writes the request, all
// under writeMu, so ids reach the agent strictly increasing.
func (c *Client) send(req muxRequest) (uint64, chan muxAnswer, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	max := c.idLimit
	if max == 0 {
		max = muxIDMax
	}
	if c.nextID.Load() >= max {
		return 0, nil, c.breakWith(errors.New("request ids exhausted"))
	}
	id := c.nextID.Add(1)
	ch := make(chan muxAnswer, 1)
	c.pmu.Lock()
	if c.broken != nil {
		err := c.broken
		c.pmu.Unlock()
		return 0, nil, err
	}
	c.pending[id] = ch
	c.pmu.Unlock()
	req.ID = id
	if err := c.writeFrameLocked(req); err != nil {
		return 0, nil, c.breakWith(err)
	}
	return id, ch, nil
}

func (c *Client) cancel(id uint64) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.writeFrameLocked(muxCancel{Cancel: id}); err != nil {
		return c.breakWith(err)
	}
	return nil
}

// readLoop is the one reader once mux is on. Any frame it cannot route to
// exactly one outstanding id breaks the client.
func (c *Client) readLoop() {
	for {
		body, err := readBody(c.RW)
		if err != nil {
			c.breakWith(err)
			return
		}
		var r muxResponse
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&r); err != nil || dec.More() || r.ID == nil {
			c.breakWith(errors.New("frame"))
			return
		}
		if *r.ID == 0 || r.Error == "mux_over_limit" || r.Error == "mux_protocol" {
			c.breakWith(fmt.Errorf("agent: %s", needsShort(r.Error)))
			return
		}
		c.pmu.Lock()
		ch, ok := c.pending[*r.ID]
		delete(c.pending, *r.ID)
		c.pmu.Unlock()
		if !ok {
			c.breakWith(errors.New("response for an id not outstanding"))
			return
		}
		ch <- muxAnswer{resp: response{Kind: r.Kind, Bytes: r.Bytes, Rows: r.Rows, Cols: r.Cols, Exit: r.Exit, Error: r.Error}}
	}
}

// needsShort keeps an agent error code short in a message.
func needsShort(s string) string {
	if len(s) > 64 {
		return s[:64]
	}
	return s
}

// breakWith sets broken once and fails every outstanding call. It returns
// the broken error.
func (c *Client) breakWith(cause error) error {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	if c.broken == nil {
		c.broken = fmt.Errorf("%w: %v", ErrBroken, cause)
		for id, ch := range c.pending {
			ch <- muxAnswer{err: c.broken}
			delete(c.pending, id)
		}
	}
	return c.broken
}

func (c *Client) brokenErr() error {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	return c.broken
}

func (c *Client) writeWhole(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.writeFrameLocked(v)
}

// writeFrameLocked writes one frame, header and body, in one Write.
func (c *Client) writeFrameLocked(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(body) > MaxFrame {
		return errors.New("frame too large")
	}
	buf := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(buf, uint32(len(body)))
	copy(buf[4:], body)
	_, err = c.RW.Write(buf)
	return err
}

// readBody reads one frame's body. A declared length above MaxFrame is an
// error and the body is left unread.
func readBody(r io.Reader) ([]byte, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	size := binary.BigEndian.Uint32(n[:])
	if size > MaxFrame {
		return nil, errors.New("frame too large")
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

var _ parent.ContextParent = (*Client)(nil)
