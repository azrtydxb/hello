// Package sipua is a scriptable SIP phone for Hello's tests: it registers
// with digest auth, places calls, and answers, rejects or hangs up incoming
// calls, all over UDP through sipgo. It is test tooling, not product code.
package sipua

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// Options configures a Phone.
type Options struct {
	User     string // SIP username (device)
	Password string // device secret
	Domain   string // SIP domain / digest realm
	Proxy    string // host:port every request is sent to (a Hello SIP node)
	// Listen is the local UDP address; port 0 picks a free port.
	Listen string
	// ContactHost overrides the host advertised in Contact, for when the
	// phone is reached through an address other than Listen.
	ContactHost string
}

// Phone is one SIP user agent.
type Phone struct {
	opts     Options
	ua       *sipgo.UserAgent
	client   *sipgo.Client
	server   *sipgo.Server
	contact  sip.ContactHeader
	dlgCli   *sipgo.DialogClientCache
	dlgSrv   *sipgo.DialogServerCache
	cancel   context.CancelFunc
	incoming chan *Incoming
}

// New starts a phone listening on opts.Listen.
func New(opts Options) (*Phone, error) {
	if opts.Listen == "" {
		opts.Listen = "127.0.0.1:0"
	}
	addr, err := freeUDP(opts.Listen)
	if err != nil {
		return nil, err
	}
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	if opts.ContactHost != "" {
		host = opts.ContactHost
	}

	ua, err := sipgo.NewUA(sipgo.WithUserAgent("hello-sipua/" + opts.User))
	if err != nil {
		return nil, err
	}
	client, err := sipgo.NewClient(ua, sipgo.WithClientHostname(host), sipgo.WithClientPort(port))
	if err != nil {
		_ = ua.Close()
		return nil, err
	}
	server, err := sipgo.NewServer(ua)
	if err != nil {
		_ = ua.Close()
		return nil, err
	}
	p := &Phone{
		opts:     opts,
		ua:       ua,
		client:   client,
		server:   server,
		contact:  sip.ContactHeader{Address: sip.Uri{User: opts.User, Host: host, Port: port}},
		incoming: make(chan *Incoming, 8),
	}
	p.dlgCli = sipgo.NewDialogClientCache(client, p.contact)
	p.dlgSrv = sipgo.NewDialogServerCache(client, p.contact)
	p.routes()

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	ready := make(chan struct{})
	go func() {
		// sipgo defines its listen-ready key as a string; the key is theirs.
		readyCtx := context.WithValue(ctx, sipgo.ListenReadyCtxKey, sipgo.ListenReadyCtxValue(ready)) //nolint:staticcheck // SA1029: sipgo's own key type
		_ = server.ListenAndServe(readyCtx, "udp", addr)
	}()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		p.Close()
		return nil, fmt.Errorf("sipua: listener on %s did not start", addr)
	}
	return p, nil
}

func (p *Phone) routes() {
	p.server.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		sess, err := p.dlgSrv.ReadInvite(req, tx)
		if err != nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
			return
		}
		in := &Incoming{Request: req, sess: sess, canceled: make(chan struct{}), final: make(chan struct{})}
		tx.OnCancel(func(*sip.Request) { in.cancelOnce.Do(func() { close(in.canceled) }) })
		_ = sess.Respond(sip.StatusTrying, "Trying", nil)
		select {
		case p.incoming <- in:
		default:
			_ = sess.Respond(sip.StatusBusyHere, "Busy Here", nil)
			return
		}
		// sipgo terminates the server transaction when this handler returns,
		// so hold it until the test sends a final response or the caller
		// cancels.
		select {
		case <-in.final:
		case <-in.canceled:
		case <-tx.Done():
		case <-time.After(3 * time.Minute):
		}
	})
	p.server.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = p.dlgSrv.ReadAck(req, tx)
	})
	p.server.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		if err := p.dlgSrv.ReadBye(req, tx); err == nil {
			return
		}
		if err := p.dlgCli.ReadBye(req, tx); err != nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
		}
	})
	p.server.OnOptions(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
	})
}

// AOR is the phone's address of record.
func (p *Phone) AOR() sip.Uri { return sip.Uri{Scheme: "sip", User: p.opts.User, Host: p.opts.Domain} }

// Contact is the URI the phone registers.
func (p *Phone) Contact() sip.Uri { return p.contact.Address }

// Addr is the host:port the phone listens on (as advertised in Contact).
func (p *Phone) Addr() string {
	return net.JoinHostPort(p.contact.Address.Host, strconv.Itoa(p.contact.Address.Port))
}

// Register sends REGISTER with the given expiry (0 unregisters), answering a
// digest challenge with the phone's credentials, and returns the final
// response.
func (p *Phone) Register(ctx context.Context, expires time.Duration) (*sip.Response, error) {
	return p.register(ctx, p.contact.Address.String(), expires, p.opts.Password)
}

// RegisterWithPassword is Register with explicit credentials, for testing
// authentication failures.
func (p *Phone) RegisterWithPassword(ctx context.Context, expires time.Duration, password string) (*sip.Response, error) {
	return p.register(ctx, p.contact.Address.String(), expires, password)
}

// UnregisterAll sends REGISTER with "Contact: *" and Expires: 0.
func (p *Phone) UnregisterAll(ctx context.Context) (*sip.Response, error) {
	return p.register(ctx, "*", 0, p.opts.Password)
}

func (p *Phone) register(ctx context.Context, contact string, expires time.Duration, password string) (*sip.Response, error) {
	req := sip.NewRequest(sip.REGISTER, sip.Uri{Scheme: "sip", Host: p.opts.Domain})
	aor := p.AOR()
	req.AppendHeader(&sip.ToHeader{Address: aor})
	req.AppendHeader(&sip.FromHeader{Address: aor, Params: tagParams()})
	req.AppendHeader(sip.NewHeader("Contact", "<"+contact+">"))
	if contact == "*" {
		req.ReplaceHeader(sip.NewHeader("Contact", "*"))
	}
	req.AppendHeader(sip.NewHeader("Expires", strconv.Itoa(int(expires/time.Second))))
	req.SetDestination(p.opts.Proxy)

	res, err := p.client.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == sip.StatusUnauthorized || res.StatusCode == sip.StatusProxyAuthRequired {
		req.SetDestination(p.opts.Proxy)
		return p.client.DoDigestAuth(ctx, req, res, sipgo.DigestAuth{Username: p.opts.User, Password: password})
	}
	return res, nil
}

// Outgoing is a call this phone placed.
type Outgoing struct {
	// Status is the final response code to the INVITE.
	Status int
	// Response is the final response, including its SDP body on success.
	Response *sip.Response
	sess     *sipgo.DialogClientSession
}

// Dial calls number@domain through the proxy with the given SDP offer and
// waits for the final response. A non-2xx final response is returned in
// Outgoing.Status with a nil error.
func (p *Phone) Dial(ctx context.Context, number string, sdp []byte) (*Outgoing, error) {
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: number, Host: p.opts.Domain})
	req.AppendHeader(&sip.FromHeader{Address: p.AOR(), Params: tagParams()})
	if sdp != nil {
		req.SetBody(sdp)
		req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	}
	// Loose-routed outbound proxy (RFC 3261 §8.1.2): the INVITE and its
	// transaction ACK/CANCEL go to the proxy, not to the request URI host.
	req.AppendHeader(sip.NewHeader("Route", "<sip:"+p.opts.Proxy+";lr>"))
	sess, err := p.dlgCli.WriteInvite(ctx, req)
	if err != nil {
		return nil, err
	}
	out := &Outgoing{sess: sess}
	err = sess.WaitAnswer(ctx, sipgo.AnswerOptions{Username: p.opts.User, Password: p.opts.Password})
	var dr *sipgo.ErrDialogResponse
	switch {
	case errors.As(err, &dr):
		out.Status, out.Response = dr.Res.StatusCode, dr.Res
		_ = sess.Close()
		return out, nil
	case err != nil:
		_ = sess.Close()
		return nil, err
	}
	out.Status, out.Response = sess.InviteResponse.StatusCode, sess.InviteResponse
	if err := sess.Ack(ctx); err != nil {
		return out, fmt.Errorf("sipua: ack: %w", err)
	}
	return out, nil
}

// Hangup sends BYE on an answered call.
func (o *Outgoing) Hangup(ctx context.Context) error {
	defer func() { _ = o.sess.Close() }()
	return o.sess.Bye(ctx)
}

// Ended is closed when the dialog ends from either side.
func (o *Outgoing) Ended() <-chan struct{} { return ended(&o.sess.Dialog) }

// Incoming is a call offered to this phone.
type Incoming struct {
	Request    *sip.Request
	sess       *sipgo.DialogServerSession
	canceled   chan struct{}
	cancelOnce sync.Once
	final      chan struct{}
	finalOnce  sync.Once
}

// Next waits for the next incoming call.
func (p *Phone) Next(ctx context.Context) (*Incoming, error) {
	select {
	case in := <-p.incoming:
		return in, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Ring sends 180 Ringing.
func (i *Incoming) Ring() error { return i.sess.Respond(sip.StatusRinging, "Ringing", nil) }

// Answer sends 200 OK with the SDP answer.
func (i *Incoming) Answer(sdp []byte) error {
	defer i.finalOnce.Do(func() { close(i.final) })
	return i.sess.RespondSDP(sdp)
}

// Reject answers with a final error code, e.g. 486.
func (i *Incoming) Reject(code int, reason string) error {
	defer i.finalOnce.Do(func() { close(i.final) })
	return i.sess.Respond(code, reason, nil)
}

// Canceled is closed when the caller's side cancels this offer.
func (i *Incoming) Canceled() <-chan struct{} { return i.canceled }

// Hangup sends BYE on an answered call.
func (i *Incoming) Hangup(ctx context.Context) error {
	defer func() { _ = i.sess.Close() }()
	return i.sess.Bye(ctx)
}

// Ended is closed when the dialog ends from either side.
func (i *Incoming) Ended() <-chan struct{} { return ended(&i.sess.Dialog) }

func ended(d *sipgo.Dialog) <-chan struct{} {
	ch := make(chan struct{})
	var once sync.Once
	d.OnStateReplay(func(s sip.DialogState) {
		if s == sip.DialogStateEnded {
			once.Do(func() { close(ch) })
		}
	})
	return ch
}

// Close stops the phone.
func (p *Phone) Close() {
	p.cancel()
	_ = p.server.Close()
	_ = p.client.Close()
	_ = p.ua.Close()
}

func tagParams() sip.HeaderParams {
	p := sip.NewParams()
	p.Add("tag", sip.GenerateTagN(16))
	return p
}

// freeUDP resolves a port-0 address to a concrete free port, so the Contact
// header can name the port before the listener starts.
func freeUDP(addr string) (string, error) {
	c, err := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp", addr)
	if err != nil {
		return "", err
	}
	defer func() { _ = c.Close() }()
	return c.LocalAddr().String(), nil
}
