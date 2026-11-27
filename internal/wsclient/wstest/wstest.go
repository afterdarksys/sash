// Package wstest is the server side of a WebSocket for tests of sash
// connect: the same module wsclient confines, used only from _test files
// (a test in wsclient holds that no other file imports it).
package wstest

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"net"
	"net/http"

	"github.com/coder/websocket"
)

// Conn is one accepted connection.
type Conn struct{ c *websocket.Conn }

// Accept upgrades a request. Compression is off, as the control plane's
// terminal server has it, and a client frame over limit ends the
// connection, as its maxPayload does.
func Accept(w http.ResponseWriter, r *http.Request, limit int64) (*Conn, error) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(limit)
	return &Conn{c: c}, nil
}

// Read is the next message and whether it is binary.
func (c *Conn) Read(ctx context.Context) (binary bool, data []byte, err error) {
	typ, data, err := c.c.Read(ctx)
	return typ == websocket.MessageBinary, data, err
}

// WriteText sends a text message.
func (c *Conn) WriteText(ctx context.Context, p []byte) error {
	return c.c.Write(ctx, websocket.MessageText, p)
}

// WriteBinary sends a binary message.
func (c *Conn) WriteBinary(ctx context.Context, p []byte) error {
	return c.c.Write(ctx, websocket.MessageBinary, p)
}

// Close sends a close frame with code and waits briefly for the peer's.
func (c *Conn) Close(code int) error {
	return c.c.Close(websocket.StatusCode(code), "")
}

// Drop ends the connection with no close frame.
func (c *Conn) Drop() error { return c.c.CloseNow() }

// RawAccept answers the upgrade by hand and hands back the connection
// itself, so a test can write frames the module would never send: a
// server that breaks the WebSocket rules.
func RawAccept(w http.ResponseWriter, r *http.Request) (net.Conn, error) {
	h, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("wstest: cannot hijack")
	}
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	conn, rw, err := h.Hijack()
	if err != nil {
		return nil, err
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n"
	if _, err := rw.WriteString(resp); err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	if err := rw.Flush(); err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	return conn, nil
}

// RawFrame is one unmasked frame with a payload under 126 bytes: first is
// the first byte (FIN, RSV bits and opcode).
func RawFrame(first byte, payload []byte) []byte {
	return append([]byte{first, byte(len(payload))}, payload...)
}
