package fax

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The hello-fax control API. hello-sip and hello-control speak it; the
// service is cluster-internal and carries no UI. Media does not go over it:
// a session opens a UDP port and hello-sip's relay forwards RTP or UDPTL
// there.
//
//	GET    /healthz                       200 when the engine can run sessions
//	POST   /v1/rasterise                  body: PDF; 200 {"pages":n} | 422
//	POST   /v1/sessions?direction=&mode=&ident=&pt=&remote=&t38=
//	                                      body: PDF when direction=send
//	GET    /v1/sessions/{id}              the session's state
//	POST   /v1/sessions/{id}/t38          switch a running session to T.38
//	POST   /v1/sessions/{id}/stop         end it, keep what arrived; returns the state
//	GET    /v1/sessions/{id}/pdf          the received pages as a PDF
//	DELETE /v1/sessions/{id}              forget it

// Session directions.
const (
	SessionReceive = "receive"
	SessionSend    = "send"
)

// SessionRequest starts a session.
type SessionRequest struct {
	Direction string
	// Mode is "g711" or "t38".
	Mode  string
	Ident string
	// PT is the G.711 payload type (0 or 8).
	PT uint8
	// Remote is where hello-fax sends media until it hears from the relay:
	// the relay socket facing hello-fax, host:port.
	Remote string
	// AllowT38 lets an outbound session ask for T.38.
	AllowT38 bool
}

// SessionInfo is the state of a session.
type SessionInfo struct {
	ID string `json:"id"`
	// Host is the address of the hello-fax pod that holds the session: media
	// goes to Host:Port, and later calls for the session go to that pod's
	// control port, so several replicas can run behind one Service.
	Host string `json:"host"`
	// Port is the UDP port hello-fax listens on for media.
	Port      int    `json:"port"`
	Mode      string `json:"mode"`
	State     string `json:"state"` // running or done
	WantT38   bool   `json:"wantT38"`
	Success   bool   `json:"success"`
	Code      int    `json:"code"`
	Text      string `json:"text"`
	Pages     int    `json:"pages"`
	BitRate   int    `json:"bitRate"`
	ECM       bool   `json:"ecm"`
	HasPDF    bool   `json:"hasPdf"`
	Truncated bool   `json:"truncated"`
}

// Done reports whether the session has ended.
func (i SessionInfo) Done() bool { return i.State == "done" }

// Errors the client maps from the service.
var (
	// ErrUnavailable means hello-fax could not be reached or cannot run
	// sessions.
	ErrUnavailable = errors.New("fax: engine unavailable")
	// ErrRejected means hello-fax refused the document (422).
	ErrRejected = errors.New("fax: document rejected")
)

// Client is the hello-fax API client.
type Client struct {
	// Addr is host:port of the service.
	Addr  string
	Token string
	HTTP  *http.Client
}

// WithHost returns a client for the pod at host, on the control port of c.
func (c *Client) WithHost(host string) *Client {
	_, port, err := net.SplitHostPort(c.Addr)
	if err != nil || host == "" {
		return c
	}
	n := *c
	n.Addr = net.JoinHostPort(host, port)
	return &n
}

// Host is the host part of Addr.
func (c *Client) Host() string {
	h, _, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return c.Addr
	}
	return h
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body []byte, out any) (status int, raw []byte, err error) {
	u := "http://" + c.Addr + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/pdf")
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err = io.ReadAll(io.LimitReader(resp.Body, MaxReceiveBytes+1<<20))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnprocessableEntity:
		return resp.StatusCode, raw, fmt.Errorf("%w: %s", ErrRejected, errText(raw))
	case resp.StatusCode == http.StatusNotFound:
		return resp.StatusCode, raw, ErrNotFound
	case resp.StatusCode >= 300:
		return resp.StatusCode, raw, fmt.Errorf("%w: %d %s", ErrUnavailable, resp.StatusCode, errText(raw))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, raw, fmt.Errorf("%w: bad response: %w", ErrUnavailable, err)
		}
	}
	return resp.StatusCode, raw, nil
}

func errText(raw []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		return e.Error
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// Healthy reports whether the service can run sessions.
func (c *Client) Healthy(ctx context.Context) error {
	_, _, err := c.do(ctx, http.MethodGet, "/healthz", nil, nil, nil)
	return err
}

// Rasterise checks that pdf can be sent and returns its page count.
func (c *Client) Rasterise(ctx context.Context, pdf []byte) (int, error) {
	var out struct {
		Pages int `json:"pages"`
	}
	if _, _, err := c.do(ctx, http.MethodPost, "/v1/rasterise", nil, pdf, &out); err != nil {
		return 0, err
	}
	return out.Pages, nil
}

// Start opens a session. For SessionSend pdf is the document.
func (c *Client) Start(ctx context.Context, r SessionRequest, pdf []byte) (SessionInfo, error) {
	q := url.Values{
		"direction": {r.Direction}, "mode": {r.Mode}, "ident": {r.Ident},
		"pt": {strconv.Itoa(int(r.PT))}, "remote": {r.Remote},
	}
	if r.AllowT38 {
		q.Set("t38", "1")
	}
	var info SessionInfo
	_, _, err := c.do(ctx, http.MethodPost, "/v1/sessions", q, pdf, &info)
	return info, err
}

// Status reads a session.
func (c *Client) Status(ctx context.Context, id string) (SessionInfo, error) {
	var info SessionInfo
	_, _, err := c.do(ctx, http.MethodGet, "/v1/sessions/"+url.PathEscape(id), nil, nil, &info)
	return info, err
}

// SwitchT38 restarts a running session on T.38 (same port).
func (c *Client) SwitchT38(ctx context.Context, id string) (SessionInfo, error) {
	var info SessionInfo
	_, _, err := c.do(ctx, http.MethodPost, "/v1/sessions/"+url.PathEscape(id)+"/t38", nil, nil, &info)
	return info, err
}

// Stop ends a session and returns its final state.
func (c *Client) Stop(ctx context.Context, id string) (SessionInfo, error) {
	var info SessionInfo
	_, _, err := c.do(ctx, http.MethodPost, "/v1/sessions/"+url.PathEscape(id)+"/stop", nil, nil, &info)
	return info, err
}

// PDF returns the pages a receive session got.
func (c *Client) PDF(ctx context.Context, id string) ([]byte, error) {
	_, raw, err := c.do(ctx, http.MethodGet, "/v1/sessions/"+url.PathEscape(id)+"/pdf", nil, nil, nil)
	return raw, err
}

// Forget deletes a session; a missing one is not an error.
func (c *Client) Forget(ctx context.Context, id string) error {
	_, _, err := c.do(ctx, http.MethodDelete, "/v1/sessions/"+url.PathEscape(id), nil, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// BearerOK checks an Authorization header against the configured token in
// constant time; an empty token admits everyone (a cluster-internal service
// behind a network policy).
func BearerOK(header, token string) bool {
	if token == "" {
		return true
	}
	got, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}
