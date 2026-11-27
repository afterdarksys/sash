package connect

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The two HTTPS calls before the terminal opens: resolve the host, then
// ask for a ticket.
//
// Threats: theft of the login token through a URL, a redirect, a proxy, a
// cookie, a log or an error text; a man in the middle; and a control plane
// that answers with something large, slow, or shaped to be printed. The
// token travels only as the Authorization header of these two calls, to
// the https origin the person configured: no redirect is followed (a 3xx
// is request_failed, and the redirect target receives nothing), no proxy
// is read from the environment, no cookie is kept. TLS is 1.3 at least
// with the platform's roots and the URL's server name; there is no flag,
// environment variable or build tag that loosens it, and SSL_CERT_FILE
// and SSL_CERT_DIR do not change the roots (roots_*.go). A call is bounded by
// httpTimeout and its answer by bodyMax. Nothing a call returns is an
// error text: only a kind (failed), or a refusal code that the caller
// prints through this build's own list. It does not protect a person who
// points SYSTEMAPI_URL at the wrong https origin: that origin receives the
// token.

const bodyMax = 64 << 10

// Timeouts; tests shorten them.
var (
	httpTimeout       = 20 * time.Second
	handshakeTimeout  = 20 * time.Second
	firstFrameTimeout = 20 * time.Second
	startTimeout      = 90 * time.Second
	writeTimeout      = 20 * time.Second
)

// testRoots replaces the system's roots in this package's tests. Nothing
// outside the package can set it, and no flag or variable reaches it.
var testRoots *x509.CertPool

var (
	hostRe   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,253}$`)
	hostIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	ticketRe = regexp.MustCompile(`^stk_[A-Za-z0-9_-]{8,508}$`)
	codeRe   = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	// hostnameRe is the control plane's own rule for a hostname at
	// enrollment (HOSTNAME_RE, controlplane/sshkey.js): a name that does
	// not match it did not come from an honest control plane, and is not
	// printed.
	hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
)

// origin is the control plane's address: https, a host, and nothing else.
func origin(raw string) (*url.URL, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return nil, false
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, false
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return nil, false
		}
	}
	return &url.URL{Scheme: "https", Host: u.Host}, true
}

// platformRoots is the roots every connection trusts, loaded once, the
// platform's own and nothing the environment names (roots_*.go). nil is
// the platform verifier on darwin.
var platformRoots = sync.OnceValue(loadPlatformRoots)

func tlsConfig(serverName string) *tls.Config {
	roots := testRoots
	if roots == nil {
		roots = platformRoots()
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, ServerName: serverName, RootCAs: roots}
}

// api is one run's client for the two calls.
type api struct {
	base   *url.URL
	token  string
	agent  string
	client *http.Client
}

func newAPI(base *url.URL, token, agent string) *api {
	dialer := &net.Dialer{Timeout: httpTimeout}
	tr := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		TLSClientConfig:        tlsConfig(base.Hostname()),
		TLSHandshakeTimeout:    httpTimeout,
		ResponseHeaderTimeout:  httpTimeout,
		MaxResponseHeaderBytes: bodyMax,
		ForceAttemptHTTP2:      true,
		DisableKeepAlives:      true,
	}
	return &api{base: base, token: token, agent: agent, client: &http.Client{
		Transport: tr,
		Timeout:   httpTimeout,
		// A redirect is never followed: the 3xx is the answer, and it is
		// a failure.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// answer is how a call ended: ok with the body decoded, refused with the
// server's code (not yet judged), or failed.
type answer int

const (
	answerOK answer = iota
	answerRefused
	answerFailed
)

// call makes one request and decodes a 200 into out. A status that is not
// 200 and carries {"error": "<text>"} is a refusal; everything else (the
// network, TLS, a timeout, a redirect, another shape) is a failure.
func (a *api) call(ctx context.Context, method, path string, query url.Values, body, out any) (answer, string) {
	u := *a.base
	u.Path = path
	u.RawQuery = query.Encode()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return answerFailed, ""
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return answerFailed, ""
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", a.agent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return answerFailed, ""
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, bodyMax+1))
	if cerr := resp.Body.Close(); err == nil {
		err = cerr
	}
	if err != nil || len(raw) > bodyMax {
		return answerFailed, ""
	}
	if resp.StatusCode == http.StatusOK {
		if json.Unmarshal(raw, out) != nil {
			return answerFailed, ""
		}
		return answerOK, ""
	}
	if resp.StatusCode < 400 {
		return answerFailed, ""
	}
	var refusal struct {
		Error *string `json:"error"`
	}
	if json.Unmarshal(raw, &refusal) != nil || refusal.Error == nil {
		return answerFailed, ""
	}
	return answerRefused, *refusal.Error
}

// resolved is what the control plane says a host name is.
type resolved struct {
	HostID   string
	Hostname string
}

// resolve is GET /api/v1/hosts/resolve?name=<host>.
func (a *api) resolve(ctx context.Context, host string) (resolved, answer, string) {
	var body struct {
		HostID   *string `json:"host_id"`
		Hostname any     `json:"hostname"`
	}
	how, code := a.call(ctx, http.MethodGet, "/api/v1/hosts/resolve", url.Values{"name": {host}}, nil, &body)
	if how != answerOK {
		return resolved{}, how, code
	}
	if body.HostID == nil || !hostIDRe.MatchString(*body.HostID) {
		return resolved{}, answerFailed, ""
	}
	r := resolved{HostID: *body.HostID}
	if name, ok := body.Hostname.(string); ok {
		r.Hostname = name
	}
	return r, answerOK, ""
}

// ticket is POST /api/v1/ws-ticket for one host and one purpose.
func (a *api) ticket(ctx context.Context, hostID, purpose string) (string, answer, string) {
	var body struct {
		Ticket *string `json:"ticket"`
	}
	req := map[string]string{"host_id": hostID, "purpose": purpose}
	how, code := a.call(ctx, http.MethodPost, "/api/v1/ws-ticket", nil, req, &body)
	if how != answerOK {
		return "", how, code
	}
	if body.Ticket == nil || !ticketRe.MatchString(*body.Ticket) {
		return "", answerFailed, ""
	}
	return *body.Ticket, answerOK, ""
}

// terminalURL is the WebSocket address of the terminal. It holds the
// ticket and is never printed.
func terminalURL(base *url.URL, hostID, ticket, purpose string, rows, cols int) string {
	u := url.URL{Scheme: "wss", Host: base.Host, Path: "/api/v1/systems/terminal/" + hostID}
	u.RawQuery = url.Values{
		"ticket":  {ticket},
		"purpose": {purpose},
		"rows":    {strconv.Itoa(rows)},
		"cols":    {strconv.Itoa(cols)},
	}.Encode()
	return u.String()
}
