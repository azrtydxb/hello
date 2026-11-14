package sipua

// This file holds what a Grandstream WP handset does beyond calls (phone
// feature parity S-38): subscribe to Hello's event packages, publish, send and
// receive MESSAGE, answer a check-sync NOTIFY after a digest challenge, and
// send reliable provisional responses.

import (
	"context"
	"crypto/md5" //nolint:gosec // RFC 3261 digest; the phone offers SHA-256 first.
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
)

// checkSyncNonce is the nonce of the digest challenge the phone puts to a
// check-sync NOTIFY, as a WP does before it reboots or resyncs.
const checkSyncNonce = "sipua-checksync-nonce"

func (p *Phone) eventRoutes() {
	p.server.OnNotify(p.onNotify)
	p.server.OnMessage(func(req *sip.Request, tx sip.ServerTransaction) {
		push(p.messages, req)
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
	})
	p.server.OnRequest(sip.PRACK, func(req *sip.Request, tx sip.ServerTransaction) {
		push(p.pracks, req)
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
	})
}

func push(ch chan *sip.Request, r *sip.Request) {
	select {
	case ch <- r:
	default:
	}
}

// Notifies carries every NOTIFY the phone accepted: subscription NOTIFYs,
// and a check-sync once it carried valid credentials.
func (p *Phone) Notifies() <-chan *sip.Request { return p.notifies }

// Messages carries every SIP MESSAGE the phone received.
func (p *Phone) Messages() <-chan *sip.Request { return p.messages }

// Pracks carries every PRACK the phone received for a reliable provisional
// response it sent.
func (p *Phone) Pracks() <-chan *sip.Request { return p.pracks }

// onNotify answers a NOTIFY: a check-sync is challenged with digest first
// (SHA-256 and MD5, the phone's own device password) and accepted only with
// valid credentials; any other event is accepted.
func (p *Phone) onNotify(req *sip.Request, tx sip.ServerTransaction) {
	if ev := req.GetHeader("Event"); ev != nil && strings.HasPrefix(strings.ToLower(ev.Value()), "check-sync") {
		h := req.GetHeader("Authorization")
		if h == nil || !p.digestOK(h.Value(), req) {
			res := sip.NewResponseFromRequest(req, sip.StatusUnauthorized, "Unauthorized", nil)
			for _, alg := range []string{"SHA-256", "MD5"} {
				c := digest.Challenge{Realm: p.opts.Domain, Nonce: checkSyncNonce, Algorithm: alg, QOP: []string{"auth"}}
				res.AppendHeader(sip.NewHeader("WWW-Authenticate", c.String()))
			}
			_ = tx.Respond(res)
			return
		}
	}
	push(p.notifies, req)
	_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
}

// digestOK verifies an Authorization header against the phone's password.
func (p *Phone) digestOK(v string, req *sip.Request) bool {
	c, err := digest.ParseCredentials(v)
	if err != nil || c.Username != p.opts.User || c.Nonce != checkSyncNonce || c.QOP != "auth" {
		return false
	}
	hex := func(alg, s string) string {
		if strings.EqualFold(alg, "SHA-256") {
			return fmt.Sprintf("%x", sha256.Sum256([]byte(s)))
		}
		return fmt.Sprintf("%x", md5.Sum([]byte(s))) //nolint:gosec // digest.
	}
	alg := c.Algorithm
	ha1 := hex(alg, p.opts.User+":"+c.Realm+":"+p.opts.Password)
	ha2 := hex(alg, req.Method.String()+":"+c.URI)
	return c.Response == hex(alg, fmt.Sprintf("%s:%s:%08x:%s:%s:%s", ha1, c.Nonce, c.Nc, c.Cnonce, c.QOP, ha2))
}

// Send sends an out-of-dialog request (SUBSCRIBE, PUBLISH, MESSAGE) to
// user@domain through the proxy, with the given headers in order and body,
// and answers a digest challenge with the phone's credentials. It returns the
// final response.
func (p *Phone) Send(ctx context.Context, method sip.RequestMethod, user string, headers [][2]string, body []byte) (*sip.Response, error) {
	return p.SendTo(ctx, method, user, user, headers, body)
}

// SendTo is Send with a To header naming toUser, for a request whose
// Request-URI is not its addressee (a quality report goes to the collector but
// names the phone itself).
func (p *Phone) SendTo(ctx context.Context, method sip.RequestMethod, ruriUser, toUser string, headers [][2]string, body []byte) (*sip.Response, error) {
	to := sip.Uri{Scheme: "sip", User: toUser, Host: p.opts.Domain}
	req := sip.NewRequest(method, sip.Uri{Scheme: "sip", User: ruriUser, Host: p.opts.Domain})
	req.AppendHeader(&sip.FromHeader{Address: p.AOR(), Params: tagParams()})
	req.AppendHeader(&sip.ToHeader{Address: to})
	req.AppendHeader(sip.NewHeader("Contact", "<"+p.contact.Address.String()+">"))
	for _, h := range headers {
		req.AppendHeader(sip.NewHeader(h[0], h[1]))
	}
	if len(body) > 0 {
		req.SetBody(body)
	}
	req.SetDestination(p.opts.Proxy)
	res, err := p.client.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == sip.StatusUnauthorized || res.StatusCode == sip.StatusProxyAuthRequired {
		req.SetDestination(p.opts.Proxy)
		return p.client.DoDigestAuth(ctx, req, res, sipgo.DigestAuth{Username: p.opts.User, Password: p.opts.Password})
	}
	return res, nil
}

// Subscribe sends an out-of-dialog SUBSCRIBE for event to user@domain.
func (p *Phone) Subscribe(ctx context.Context, user, event string, expires int, headers [][2]string, body []byte) (*sip.Response, error) {
	h := append([][2]string{{"Event", event}, {"Expires", strconv.Itoa(expires)}}, headers...)
	return p.Send(ctx, sip.SUBSCRIBE, user, h, body)
}

// RingReliable sends 180 Ringing as a reliable provisional response (RFC
// 3262): Require: 100rel and the given RSeq. The caller's side PRACKs it.
func (i *Incoming) RingReliable(rseq uint32) error {
	return i.sess.Respond(sip.StatusRinging, "Ringing", nil,
		sip.NewHeader("Require", "100rel"), sip.NewHeader("RSeq", strconv.FormatUint(uint64(rseq), 10)))
}
