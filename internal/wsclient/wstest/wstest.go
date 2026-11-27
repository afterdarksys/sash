// Package wstest is the server side of a WebSocket for tests of sash
// connect: the same module wsclient confines, used only from _test files
// (a test in wsclient holds that no other file imports it).
package wstest

import (
	"context"
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
