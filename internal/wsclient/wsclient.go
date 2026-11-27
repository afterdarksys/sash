// Package wsclient is the one place sash speaks WebSocket. It confines
// github.com/coder/websocket: no other package of sash imports that
// module (a test holds this), and only sash connect uses this one.
//
// Threats: a server, or anything on the path, that redirects the upgrade
// to another origin (never followed: a 3xx is a failed handshake), stalls
// the handshake (bounded by HandshakeTimeout), sends a frame of any size
// (a message over ReadLimit ends the connection), or negotiates
// compression to inflate one (never offered). TLS is the caller's
// config: Dial refuses one with a minimum below TLS 1.3 and any scheme but
// wss, and this package never turns verification off (a test greps for
// the field's name). No proxy is
// read from the environment, no cookie is kept, and no subprotocol is
// offered. It does not authenticate the server beyond TLS and does not
// judge what a frame holds: that is the caller's.
package wsclient

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

// Kind is a message's type.
type Kind int

const (
	Text Kind = iota + 1
	Binary
)

// Options are what Dial needs. All are required but Header.
type Options struct {
	TLS              *tls.Config
	Header           http.Header
	HandshakeTimeout time.Duration
	ReadLimit        int64
}

// HandshakeError is an upgrade the server answered with an HTTP status
// other than 101. It carries the status and nothing the server wrote.
type HandshakeError struct{ Status int }

func (e *HandshakeError) Error() string { return "wsclient: handshake refused" }

// Fixed errors: none carries a URL, a header, or a server's text.
var (
	ErrConfig    = errors.New("wsclient: refused configuration")
	ErrHandshake = errors.New("wsclient: handshake failed")
	ErrTooLarge  = errors.New("wsclient: message over the read limit")
	ErrClosed    = errors.New("wsclient: connection closed")
	ErrProtocol  = errors.New("wsclient: the server broke the WebSocket rules")
)

// Conn is one client connection.
type Conn struct {
	c     *websocket.Conn
	limit int64
}

// Dial opens a wss connection. The URL may hold a credential (a ticket in
// its query), so no error returned here carries it.
func Dial(ctx context.Context, rawURL string, o Options) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "wss" || u.Host == "" || u.User != nil {
		return nil, ErrConfig
	}
	if o.TLS == nil || o.TLS.MinVersion < tls.VersionTLS13 || o.HandshakeTimeout <= 0 || o.ReadLimit <= 0 {
		return nil, ErrConfig
	}
	cfg := o.TLS.Clone()
	cfg.NextProtos = []string{"http/1.1"}
	dialer := &net.Dialer{Timeout: o.HandshakeTimeout}
	tr := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		TLSClientConfig:        cfg,
		TLSHandshakeTimeout:    o.HandshakeTimeout,
		ResponseHeaderTimeout:  o.HandshakeTimeout,
		MaxResponseHeaderBytes: 64 << 10,
		DisableCompression:     true,
		DisableKeepAlives:      true,
	}
	client := &http.Client{
		Transport: tr,
		// A redirect is never followed: the 3xx is the answer.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	hctx, cancel := context.WithTimeout(ctx, o.HandshakeTimeout)
	defer cancel()
	c, resp, err := websocket.Dial(hctx, u.String(), &websocket.DialOptions{
		HTTPClient:      client,
		HTTPHeader:      o.Header.Clone(),
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		tr.CloseIdleConnections()
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			return nil, &HandshakeError{Status: resp.StatusCode}
		}
		return nil, ErrHandshake
	}
	c.SetReadLimit(o.ReadLimit)
	return &Conn{c: c, limit: o.ReadLimit}, nil
}

// Read is the next text or binary message. Pings are answered inside it.
// A message over the read limit ends the connection with ErrTooLarge; a
// frame the module refuses (reserved bits, a reserved opcode, a bad
// control frame, a continuation with no message) and a text message that
// is not UTF-8 are ErrProtocol; a close frame or a broken connection is
// ErrClosed; a context that ends returns its error. The module accepts a
// masked frame from the server and hands on its payload as it came: that
// violation is not seen here.
func (c *Conn) Read(ctx context.Context) (Kind, []byte, error) {
	typ, data, err := c.c.Read(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		if errors.Is(err, websocket.ErrMessageTooBig) {
			return 0, nil, ErrTooLarge
		}
		if transportEnded(err) {
			return 0, nil, ErrClosed
		}
		return 0, nil, ErrProtocol
	}
	if typ == websocket.MessageBinary {
		return Binary, data, nil
	}
	if !utf8.Valid(data) {
		return 0, nil, ErrProtocol
	}
	return Text, data, nil
}

// transportEnded is a read error that is the connection ending: a close
// frame, the end of the stream, a network or TLS error, or a connection
// already closed here. The module reports a frame it refuses with an
// error of none of these kinds, which Read calls ErrProtocol.
func transportEnded(err error) bool {
	if websocket.CloseStatus(err) != -1 {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var errno syscall.Errno
	return errors.As(err, &errno)
}

// WriteText sends one text message.
func (c *Conn) WriteText(ctx context.Context, p []byte) error {
	if err := c.c.Write(ctx, websocket.MessageText, p); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrClosed
	}
	return nil
}

// Close ends the connection at once, without waiting on the peer.
func (c *Conn) Close() error {
	if err := c.c.CloseNow(); err != nil && !errors.Is(err, net.ErrClosed) {
		return ErrClosed
	}
	return nil
}
