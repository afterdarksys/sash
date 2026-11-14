// Package proto is the verb pipe between sash and its parent.
// Frames are a big-endian uint32 length and one JSON object.
// A length above MaxFrame is rejected before the body is read.
// The agent re-checks the grant before it runs a helper. This package does not.
package proto

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/afterdarksys/sash/internal/parent"
)

// MaxFrame is the largest verb body. It matches the agent's 8 MiB result cap.
const MaxFrame = 8 << 20

// Client is the sash end of the verb pipe.
type Client struct {
	RW io.ReadWriter
	mu sync.Mutex
}

type request struct {
	Key  string   `json:"key"`
	Argv []string `json:"argv"`
	Path string   `json:"path"`
	Body string   `json:"body"`
	Mode string   `json:"mode,omitempty"`
}

type response struct {
	Kind  string       `json:"kind"`
	Bytes string       `json:"bytes"`
	Rows  []parent.Row `json:"rows"`
	Cols  []string     `json:"cols"`
	Exit  int          `json:"exit"`
	Error string       `json:"error"`
}

// Do sends one verb and waits for the parent's result.
func (c *Client) Do(v parent.Verb) (parent.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	req := request{Key: v.Key, Argv: v.Argv, Path: v.Path, Mode: v.Mode}
	if len(v.Body) > 0 {
		req.Body = base64.StdEncoding.EncodeToString(v.Body)
	}
	if err := Write(c.RW, req); err != nil {
		return parent.Result{}, err
	}
	var resp response
	if err := Read(c.RW, &resp); err != nil {
		return parent.Result{}, err
	}
	if resp.Error != "" {
		// An agent older than file.write mode refuses the unknown field
		// as a bad frame. That is never retried without the mode: the
		// write would land with the default mode.
		if v.Mode != "" && resp.Error == "frame body" {
			return parent.Result{}, parent.ErrModeUnsupported
		}
		return parent.Result{}, errors.New(resp.Error)
	}
	out := parent.Result{Rows: resp.Rows, Cols: resp.Cols, Exit: resp.Exit}
	switch resp.Kind {
	case "bytes", "":
		out.Kind = parent.KindBytes
	case "rows":
		out.Kind = parent.KindRows
	default:
		return parent.Result{}, fmt.Errorf("frame kind")
	}
	if resp.Bytes != "" {
		b, err := base64.StdEncoding.DecodeString(resp.Bytes)
		if err != nil {
			return parent.Result{}, fmt.Errorf("frame body")
		}
		out.Bytes = b
	}
	return out, nil
}

// Serve reads verbs until the pipe closes and writes one result each.
func Serve(rw io.ReadWriter, h func(parent.Verb) (parent.Result, error)) error {
	for {
		var req request
		err := Read(rw, &req)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		verb := parent.Verb{Key: req.Key, Argv: req.Argv, Path: req.Path, Mode: req.Mode}
		if req.Body != "" {
			b, decErr := base64.StdEncoding.DecodeString(req.Body)
			if decErr != nil {
				if err := Write(rw, response{Error: "frame body"}); err != nil {
					return err
				}
				continue
			}
			verb.Body = b
		}
		res, callErr := h(verb)
		resp := response{Cols: res.Cols, Rows: res.Rows, Exit: res.Exit}
		if res.Kind == parent.KindRows {
			resp.Kind = "rows"
		} else {
			resp.Kind = "bytes"
		}
		if len(res.Bytes) > 0 {
			resp.Bytes = base64.StdEncoding.EncodeToString(res.Bytes)
		}
		if callErr != nil {
			resp.Error = callErr.Error()
		}
		if err := Write(rw, resp); err != nil {
			return err
		}
	}
}

// Write sends one JSON frame.
func Write(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(body) > MaxFrame {
		return errors.New("frame too large")
	}
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(body)))
	if _, err := w.Write(n[:]); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// Read receives one JSON frame. A declared length above MaxFrame is an error
// and the body is left unread.
func Read(r io.Reader, v any) error {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	size := binary.BigEndian.Uint32(n[:])
	if size > MaxFrame {
		return errors.New("frame too large")
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
