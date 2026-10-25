// Check-sync pushes (spec S-5): hello-control queues a resync or reboot of
// a phone, a hello-sip node claims it, sends an out-of-dialog NOTIFY with
// Event: check-sync to every registered contact of the phone's device
// (answering the phone's digest challenge from the device's stored HA1) and
// reports the best result. Hello never sends Event: reset.
package sip

import (
	"context"
	"crypto/md5" //nolint:gosec // RFC 3261 digest; SHA-256 is preferred when the phone offers it.
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
)

// Push kinds (store.PushResync, store.PushReboot).
const (
	PushResync = "resync"
	PushReboot = "reboot"
)

// Push results (store.PushOK and the others).
const (
	PushOK            = "ok"
	PushNotRegistered = "not_registered"
	PushTimeout       = "timeout"
	PushAuthFailed    = "auth_failed"
	PushFailed        = "failed"
)

// PushJob is one claimed push.
type PushJob struct {
	ID     int64
	Kind   string // PushResync or PushReboot
	Vendor string
	Device string // the bound device's SIP username
}

// PushResult is what a push achieved.
type PushResult struct {
	Status   string
	Detail   string
	Contacts int // contacts the NOTIFY was sent to
}

// PushQueue is the check-sync queue hello-control fills (PostgreSQL).
type PushQueue interface {
	// Watch calls wake for every queued push, and once after every
	// (re)connect, until ctx ends.
	Watch(ctx context.Context, wake func())
	// Claim takes pending pushes for this node, never more than perMinute
	// per minute cluster-wide.
	Claim(ctx context.Context, node string, perMinute int) ([]PushJob, error)
	// Finish writes a claimed push's result.
	Finish(ctx context.Context, id int64, r PushResult) error
}

// checkSyncTimeout bounds one NOTIFY exchange (challenge included); a
// variable so tests can shorten it.
var checkSyncTimeout = 8 * time.Second

// pushPollInterval is how often the queue is claimed from besides wakeups,
// so rows held back by the rate limit are picked up.
var pushPollInterval = 2 * time.Second

// checkSyncEvent is the Event header value for a vendor and push kind.
// The reboot parameter is vendor-specific (spec "Vendor reference"):
// Yealink and Snom take an explicit reboot=false for a plain resync, the
// others a bare check-sync. Event: reset is never produced.
func checkSyncEvent(vendor, kind string) string {
	reboot := kind == PushReboot
	switch vendor {
	case "yealink", "snom":
		if reboot {
			return "check-sync;reboot=true"
		}
		return "check-sync;reboot=false"
	default: // grandstream, poly, fanvil, generic
		if reboot {
			return "check-sync;reboot=true"
		}
		return "check-sync"
	}
}

// pushLoop claims queued pushes and runs them until ctx ends.
func (s *Server) pushLoop(ctx context.Context) {
	wake := make(chan struct{}, 1)
	kick := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	s.bg.Go(func() { s.deps.Pushes.Watch(ctx, kick) })
	rate := s.cfg.PushRate
	if rate <= 0 {
		rate = 20
	}
	t := time.NewTicker(pushPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-wake:
		case <-t.C:
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		jobs, err := s.deps.Pushes.Claim(cctx, s.cfg.NodeID, rate)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("claim check-sync pushes", "error", err)
			}
			continue
		}
		for _, j := range jobs {
			s.goBG(func() { s.runPush(j) })
		}
	}
}

// runPush sends one push and writes its result.
func (s *Server) runPush(j PushJob) {
	defer contain(s.log, "check-sync")
	res := s.sendCheckSync(j)
	event := checkSyncEvent(j.Vendor, j.Kind)
	s.m.CheckSync.WithLabelValues(event, res.Status).Inc()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.deps.Pushes.Finish(ctx, j.ID, res); err != nil {
		s.log.Warn("record check-sync result", "push", j.ID, "error", err)
	}
}

// pushRank orders results best first, for the best of several contacts.
func pushRank(status string) int {
	switch status {
	case PushOK:
		return 0
	case PushAuthFailed:
		return 1
	case PushTimeout:
		return 2
	case PushFailed:
		return 3
	}
	return 4
}

// sendCheckSync sends the push to every registered contact of the device
// and returns the best result.
func (s *Server) sendCheckSync(j PushJob) PushResult {
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		return PushResult{Status: PushFailed, Detail: "no configuration loaded"}
	}
	dev, ok := snap.DeviceByUsername(j.Device)
	if !ok {
		return PushResult{Status: PushNotRegistered, Detail: "device not in the configuration"}
	}
	ctx, cancel := s.stateCtx()
	bindings, err := s.deps.State.Bindings(ctx, s.aor(j.Device))
	cancel()
	if err != nil {
		return PushResult{Status: PushFailed, Detail: "registrations unavailable: " + err.Error()}
	}
	if len(bindings) == 0 {
		return PushResult{Status: PushNotRegistered, Detail: "not registered"}
	}
	event := checkSyncEvent(j.Vendor, j.Kind)
	results := make([]PushResult, len(bindings))
	var wg sync.WaitGroup
	for i, b := range bindings {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer contain(s.log, "check-sync contact")
			ctx, cancel := context.WithTimeout(context.Background(), checkSyncTimeout)
			defer cancel()
			results[i] = s.checkSyncContact(ctx, dev.Username, dev.Realm, dev.HA1MD5, dev.HA1SHA256, b, event)
		}()
	}
	wg.Wait()
	best := results[0]
	for _, r := range results[1:] {
		if pushRank(r.Status) < pushRank(best.Status) {
			best = r
		}
	}
	best.Contacts = len(bindings)
	return best
}

// checkSyncContact sends the NOTIFY to one contact, answering a digest
// challenge once.
func (s *Server) checkSyncContact(ctx context.Context, user, realm, ha1MD5, ha1SHA256 string, b livestate.Binding, event string) PushResult {
	req, err := s.checkSyncRequest(user, b, event)
	if err != nil {
		return PushResult{Status: PushFailed, Detail: err.Error()}
	}
	res, err := s.client.Do(ctx, req)
	if err != nil {
		return pushError(err)
	}
	s.m.response(res.StatusCode)
	if res.StatusCode == sip.StatusUnauthorized || res.StatusCode == sip.StatusProxyAuthRequired {
		hdr, authHdr := "WWW-Authenticate", "Authorization"
		if res.StatusCode == sip.StatusProxyAuthRequired {
			hdr, authHdr = "Proxy-Authenticate", "Proxy-Authorization"
		}
		cred, err := answerChallenge(res.GetHeaders(hdr), req.Method.String(), req.Recipient.String(), user, realm, ha1MD5, ha1SHA256)
		if err != nil {
			s.log.Warn("check-sync challenge not answerable", "device", user, "error", err)
			return PushResult{Status: PushAuthFailed, Detail: err.Error()}
		}
		req.RemoveHeader(authHdr)
		req.AppendHeader(sip.NewHeader(authHdr, cred.String()))
		req.CSeq().SeqNo++
		req.RemoveHeader("Via")
		if res, err = s.client.Do(ctx, req); err != nil {
			return pushError(err)
		}
		s.m.response(res.StatusCode)
	}
	switch c := res.StatusCode; {
	case c >= 200 && c < 300:
		return PushResult{Status: PushOK, Detail: strconv.Itoa(c)}
	case c == sip.StatusUnauthorized || c == sip.StatusProxyAuthRequired || c == sip.StatusForbidden:
		return PushResult{Status: PushAuthFailed, Detail: strconv.Itoa(c) + " " + res.Reason}
	case c == sip.StatusNotFound || c == sip.StatusCallTransactionDoesNotExists:
		return PushResult{Status: PushNotRegistered, Detail: strconv.Itoa(c) + " " + res.Reason}
	}
	return PushResult{Status: PushFailed, Detail: strconv.Itoa(res.StatusCode) + " " + res.Reason}
}

func pushError(err error) PushResult {
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline") {
		return PushResult{Status: PushTimeout, Detail: "no answer"}
	}
	return PushResult{Status: PushFailed, Detail: err.Error()}
}

// checkSyncRequest builds the out-of-dialog NOTIFY for one binding. It is
// routed like the INVITE legs: through the registration's Path (the edge
// proxy that holds the phone's NAT flow) when there is one.
func (s *Server) checkSyncRequest(user string, b livestate.Binding, event string) (*sip.Request, error) {
	var target sip.Uri
	raw := strings.TrimSuffix(strings.TrimPrefix(b.ContactURI, "<"), ">")
	if err := sip.ParseUri(raw, &target); err != nil {
		return nil, fmt.Errorf("binding contact %q: %w", b.ContactURI, err)
	}
	req := sip.NewRequest(sip.NOTIFY, target)
	req.AppendHeader(&sip.FromHeader{
		Address: sip.Uri{Scheme: "sip", User: "hello", Host: s.cfg.Domain},
		Params:  sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(8)}},
	})
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: user, Host: s.cfg.Domain}})
	callID := sip.CallIDHeader(newID())
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: sip.NOTIFY})
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.HeaderClone(&s.contact))
	req.AppendHeader(sip.NewHeader("Event", event))
	req.SetTransport("UDP")
	if err := s.routeToBinding(req, b); err != nil {
		return nil, err
	}
	return req, nil
}

// routeToBinding addresses req to a registered contact: via the Path of a
// registration that came through an edge proxy (or another node), else
// straight to the address the REGISTER came from.
func (s *Server) routeToBinding(req *sip.Request, b livestate.Binding) error {
	viaPath := len(b.Path) > 0 &&
		(!strings.Contains(b.Path[0], "hflow=") || b.ReceivedNode != s.cfg.NodeID)
	switch {
	case viaPath:
		var first sip.Uri
		if err := sip.ParseUri(strings.Trim(b.Path[0], "<> "), &first); err != nil {
			return err
		}
		for _, p := range b.Path {
			req.AppendHeader(sip.NewHeader("Route", p))
		}
		req.SetDestination(hostPort(first))
	case b.Source != "":
		req.SetDestination(b.Source)
	}
	return nil
}

// answerChallenge builds the Authorization value for the first challenge
// the phone offers that Hello can answer: SHA-256 before MD5, in the
// device's realm, from the stored HA1 (the password is never kept). A
// challenge in another realm is refused.
func answerChallenge(headers []sip.Header, method, uri, user, realm, ha1MD5, ha1SHA256 string) (*digest.Credentials, error) {
	lastErr := errors.New("no digest challenge")
	var best *digest.Challenge
	for _, h := range headers {
		c, err := digest.ParseChallenge(h.Value())
		if err != nil {
			lastErr = err
			continue
		}
		if c.Realm != realm {
			lastErr = fmt.Errorf("challenge realm %q is not the device's realm", c.Realm)
			continue
		}
		switch strings.ToUpper(c.Algorithm) {
		case "SHA-256":
			best = c
		case "", "MD5":
			if best == nil {
				best = c
			}
		default:
			lastErr = fmt.Errorf("unsupported digest algorithm %q", c.Algorithm)
		}
		if best != nil && strings.EqualFold(best.Algorithm, "SHA-256") {
			break
		}
	}
	if best == nil {
		return nil, lastErr
	}
	var (
		h   hash.Hash
		ha1 string
		alg = strings.ToUpper(best.Algorithm)
	)
	if alg == "SHA-256" {
		h, ha1 = sha256.New(), ha1SHA256
	} else {
		h, ha1 = md5.New(), ha1MD5 //nolint:gosec // RFC 3261 digest.
	}
	if ha1 == "" {
		return nil, errors.New("device has no HA1 for the challenge algorithm")
	}
	cred := &digest.Credentials{
		Username: user, Realm: best.Realm, Nonce: best.Nonce, URI: uri,
		Algorithm: best.Algorithm, Opaque: best.Opaque,
	}
	ha2 := hexHash(h, method+":"+uri)
	if best.SupportsQOP("auth") {
		var raw [8]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, err
		}
		cred.QOP, cred.Cnonce, cred.Nc = "auth", hex.EncodeToString(raw[:]), 1
		cred.Response = hexHash(h, fmt.Sprintf("%s:%s:%08x:%s:%s:%s", strings.ToLower(ha1), best.Nonce, cred.Nc, cred.Cnonce, cred.QOP, ha2))
	} else {
		cred.Response = hexHash(h, strings.ToLower(ha1)+":"+best.Nonce+":"+ha2)
	}
	return cred, nil
}
