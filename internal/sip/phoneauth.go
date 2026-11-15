// Requests Hello sends to a phone that challenges them (phone-feature-parity
// S-22). A Grandstream WP with "Authenticate Incoming INVITE" (P2346) on
// answers every INVITE it receives with 401 (or 407) and accepts it only with
// the device's own digest credentials. Hello answers the challenge with the
// device's credentials, as the check-sync NOTIFY does (S-5): from the stored
// HA1 when the phone challenges in the SIP domain's realm, else from the
// device's sealed password hashed for the phone's realm (a WP challenges in
// its configured server, "host:port"). The password itself is never kept:
// only the HA1 it gives for that realm, cached per device until the device's
// secret changes.
//
// The initial INVITE is re-sent as a new transaction of the same dialog
// attempt (RFC 3261 22.2 and 8.1.3.5): the same Call-ID, From tag and To, the
// CSeq one higher and a new Via branch. An in-dialog request (a re-INVITE or
// UPDATE: hold, a transfer's re-anchor, a session refresh) is re-sent the
// same way inside its dialog.
package sip

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
)

// maxPhoneAuthAttempts bounds how often one request answers a challenge: the
// first, and one more for a phone that says the nonce went stale.
const maxPhoneAuthAttempts = 2

// Results of hello_phone_challenges_total.
const (
	challengeAnswered   = "answered"
	challengeUnanswered = "unanswerable"
)

// errOtherRealm is answerChallenge's error for a challenge in a realm other
// than the device's: the stored HA1 cannot answer it, the password can.
var errOtherRealm = errors.New("challenge realm is not the device's realm")

// phoneRealms caches the HA1 values of a device's password for the realms its
// phone challenges in, so a challenged call reads the sealed password from
// PostgreSQL once, not on every call. The key carries the device's stored
// HA1, so a new secret (a rotation, a re-provisioning) misses the cache.
type phoneRealms struct {
	mu  sync.Mutex
	ha1 map[string][2]string
}

// phoneRealmCacheMax bounds the cache; it is cleared when full.
const phoneRealmCacheMax = 4096

func (p *phoneRealms) get(key string) ([2]string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.ha1[key]
	return v, ok
}

func (p *phoneRealms) put(key string, v [2]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ha1 == nil || len(p.ha1) >= phoneRealmCacheMax {
		p.ha1 = map[string][2]string{}
	}
	p.ha1[key] = v
}

// deviceSecrets is where a device's sealed password is opened: Deps'
// DeviceSecrets, else the check-sync queue when it can (older wiring).
func (s *Server) deviceSecrets() DeviceSecrets {
	if s.deps.DeviceSecrets != nil {
		return s.deps.DeviceSecrets
	}
	if ds, ok := s.deps.Pushes.(DeviceSecrets); ok {
		return ds
	}
	return nil
}

// challengeHeaders names the challenge header of a 401 or 407 and the header
// that answers it.
func challengeHeaders(code int) (challenge, answer string) {
	if code == sip.StatusProxyAuthRequired {
		return "Proxy-Authenticate", "Proxy-Authorization"
	}
	return "WWW-Authenticate", "Authorization"
}

// isChallenge reports whether res is a digest challenge Hello may answer.
func isChallenge(res *sip.Response) bool {
	if res == nil {
		return false
	}
	switch res.StatusCode {
	case sip.StatusUnauthorized:
		return res.GetHeader("WWW-Authenticate") != nil
	case sip.StatusProxyAuthRequired:
		return res.GetHeader("Proxy-Authenticate") != nil
	}
	return false
}

// deviceCredential answers a phone's digest challenge to a request Hello
// sends it (method, uri) as device user: from the stored HA1 in the device's
// realm, else from the device's password for the phone's own realm.
func (s *Server) deviceCredential(ctx context.Context, user string, headers []sip.Header, method, uri string) (*digest.Credentials, error) {
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		return nil, errors.New("no configuration loaded")
	}
	dev, ok := snap.DeviceByUsername(user)
	if !ok {
		return nil, fmt.Errorf("device %q is not in the configuration", user)
	}
	cred, err := answerChallenge(headers, method, uri, dev.Username, dev.Realm, dev.HA1MD5, dev.HA1SHA256)
	if err == nil || !errors.Is(err, errOtherRealm) {
		return cred, err
	}
	return s.answerOwnRealm(ctx, headers, method, uri, dev, err)
}

// answerOwnRealm answers a challenge in a realm other than the device's (the
// phone's own, see DeviceSecrets) by hashing the device password for that
// realm. It returns prior when no password is available, so the original
// reason is reported.
func (s *Server) answerOwnRealm(ctx context.Context, headers []sip.Header, method, uri string, dev snapshot.Device, prior error) (*digest.Credentials, error) {
	lastErr := prior
	var secret string
	for _, h := range headers {
		c, err := digest.ParseChallenge(h.Value())
		if err != nil || c.Realm == "" {
			continue
		}
		key := dev.Username + "\x00" + c.Realm + "\x00" + dev.HA1MD5
		ha1, ok := s.realms.get(key)
		if !ok {
			if secret == "" {
				ds := s.deviceSecrets()
				if ds == nil {
					return nil, prior
				}
				pw, err := ds.DeviceSecret(ctx, dev.Username)
				if err != nil || pw == "" {
					return nil, prior
				}
				secret = pw
			}
			md5Hex, shaHex := auth.HA1(dev.Username, c.Realm, secret)
			ha1 = [2]string{md5Hex, shaHex}
			s.realms.put(key, ha1)
		}
		cred, err := answerChallenge([]sip.Header{h}, method, uri, dev.Username, c.Realm, ha1[0], ha1[1])
		if err == nil {
			return cred, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// notePhoneChallenge counts one challenge a phone sent Hello.
func (s *Server) notePhoneChallenge(method, result string) {
	if s.m.PhoneChallenges != nil {
		s.m.PhoneChallenges.WithLabelValues(method, result).Inc()
	}
}

// waitAnswerPhone is WaitAnswer for an INVITE Hello sends a phone's binding
// of device. A 401 or 407 is answered with the device's credentials and the
// INVITE re-sent (see the file comment); resent sees each new session before
// it is waited on, so a leg's CANCEL and BYE use the dialog that answers. It
// returns the last session and WaitAnswer's error for it. A device of "" (a
// trunk, a SIP URI) is never answered here.
func (s *Server) waitAnswerPhone(ctx context.Context, dcs *sipgo.DialogClientSession, device string, opts sipgo.AnswerOptions, resent func(*sipgo.DialogClientSession)) (*sipgo.DialogClientSession, error) {
	for attempt := 0; ; attempt++ {
		err := dcs.WaitAnswer(ctx, opts)
		var de *sipgo.ErrDialogResponse
		if err == nil || !errors.As(err, &de) || !isChallenge(de.Res) || device == "" || attempt >= maxPhoneAuthAttempts || ctx.Err() != nil {
			return dcs, err
		}
		chal, answer := challengeHeaders(de.Res.StatusCode)
		inv := dcs.InviteRequest
		cred, cerr := s.deviceCredential(ctx, device, de.Res.GetHeaders(chal), sip.INVITE.String(), inv.Recipient.String())
		if cerr != nil {
			s.notePhoneChallenge(sip.INVITE.String(), challengeUnanswered)
			s.log.Warn("phone challenged an INVITE that Hello cannot answer", "device", device, "error", cerr)
			return dcs, err
		}
		s.notePhoneChallenge(sip.INVITE.String(), challengeAnswered)
		req := inv.Clone()
		req.RemoveHeader("Authorization")
		req.RemoveHeader("Proxy-Authorization")
		req.AppendHeader(sip.NewHeader(answer, cred.String()))
		req.CSeq().SeqNo++
		req.RemoveHeader("Via") // a new transaction: a new branch
		next, werr := s.uac.WriteInvite(context.Background(), req)
		if werr != nil {
			s.log.Warn("authenticated INVITE could not be sent", "device", device, "error", werr)
			return dcs, err
		}
		next.UA = dcs.UA // the leg's own UA (its CANCEL Reason)
		if resent != nil {
			resent(next)
		}
		dcs = next
	}
}

// dialogDevice is the device on the far end of the dialog callID, "" when
// it is not a phone of this node's calls (a trunk, a gone call).
func (s *Server) dialogDevice(callID string) string {
	ref, ok := s.lookup(callID)
	if !ok {
		return ""
	}
	if l := ref.leg; l != nil {
		if l.trunk != nil || l.uri != nil {
			return ""
		}
		return l.binding.Device
	}
	c := ref.c
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.callerDevice
}

// dialogDo sends an in-dialog request on a dialog session (do is the
// session's Do) and answers a phone's challenge to it: the request is re-sent
// in the dialog with the credentials, the session giving it the next CSeq and
// a new Via branch. Like do it never returns (nil, nil).
func (s *Server) dialogDo(ctx context.Context, req *sip.Request, do func(context.Context, *sip.Request) (*sip.Response, error)) (*sip.Response, error) {
	res, err := requireResponse(do(ctx, req))
	for attempt := 0; err == nil && isChallenge(res) && attempt < maxPhoneAuthAttempts; attempt++ {
		device := s.dialogDevice(req.CallID().Value())
		if device == "" {
			return res, nil
		}
		chal, answer := challengeHeaders(res.StatusCode)
		cred, cerr := s.deviceCredential(ctx, device, res.GetHeaders(chal), req.Method.String(), req.Recipient.String())
		if cerr != nil {
			s.notePhoneChallenge(req.Method.String(), challengeUnanswered)
			s.log.Warn("phone challenged an in-dialog request that Hello cannot answer", "device", device, "method", req.Method.String(), "error", cerr)
			return res, nil
		}
		s.notePhoneChallenge(req.Method.String(), challengeAnswered)
		resendPrepare(req, answer, cred.String())
		for req.RemoveHeader("Route") { // the session adds its route set again
		}
		res, err = requireResponse(do(ctx, req))
	}
	return res, err
}

// doDevice is s.do for a request Hello builds itself on a phone's dialog
// (a taken-over call's, a raw caller dialog's): a challenge is answered as
// device and the request re-sent with the next CSeq, which is recorded for
// the dialog.
func (s *Server) doDevice(ctx context.Context, req *sip.Request, device string) (*sip.Response, error) {
	res, err := s.do(ctx, req)
	for attempt := 0; err == nil && isChallenge(res) && device != "" && attempt < maxPhoneAuthAttempts; attempt++ {
		chal, answer := challengeHeaders(res.StatusCode)
		cred, cerr := s.deviceCredential(ctx, device, res.GetHeaders(chal), req.Method.String(), req.Recipient.String())
		if cerr != nil {
			s.notePhoneChallenge(req.Method.String(), challengeUnanswered)
			s.log.Warn("phone challenged a request that Hello cannot answer", "device", device, "method", req.Method.String(), "error", cerr)
			return res, nil
		}
		s.notePhoneChallenge(req.Method.String(), challengeAnswered)
		resendPrepare(req, answer, cred.String())
		if cs := req.CSeq(); cs != nil {
			cs.SeqNo++
			s.noteSentCSeq(req.CallID().Value(), cs.SeqNo)
		}
		res, err = s.do(ctx, req)
	}
	return res, err
}

// resendPrepare readies a challenged request for its second send: the
// answer header replaces any earlier one and the Via (the old transaction's
// branch) is dropped.
func resendPrepare(req *sip.Request, answerHeader, value string) {
	req.RemoveHeader("Authorization")
	req.RemoveHeader("Proxy-Authorization")
	req.AppendHeader(sip.NewHeader(answerHeader, value))
	for req.RemoveHeader("Via") {
	}
}

// endpointDevice is the device a replicated endpoint names: its SIP username
// (a callee leg's AOR), or, for a caller leg's AOR (the extension number),
// the extension's device when it has only one.
func (s *Server) endpointDevice(user string) string {
	snap := s.deps.Snapshots.Current()
	if snap == nil || user == "" {
		return user
	}
	if _, ok := snap.DeviceByUsername(user); ok {
		return user
	}
	if ds := snap.DevicesForExtension(user); len(ds) == 1 {
		return ds[0].Username
	}
	return ""
}

// deviceOfAOR is the SIP username of an AOR "sip:user@domain".
func deviceOfAOR(aor string) string {
	aor = strings.TrimPrefix(strings.TrimPrefix(aor, "sips:"), "sip:")
	if i := strings.IndexByte(aor, '@'); i >= 0 {
		return aor[:i]
	}
	return ""
}
