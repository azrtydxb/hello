package prov

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	macA = "805ec0aabbcc"
	macB = "805ec0ddeeff"
)

// expectEmpty fails unless w is status with an empty body and the newest
// audit row records res.
func expectEmpty(t *testing.T, e *env, w interface {
	Result() *http.Response
}, status int, res Result,
) FetchRecord {
	t.Helper()
	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()
	var body bytes.Buffer
	_, _ = body.ReadFrom(resp.Body)
	if resp.StatusCode != status || body.Len() != 0 {
		t.Fatalf("got %d with %d body bytes, want an empty %d", resp.StatusCode, body.Len(), status)
	}
	row := e.last()
	if row.Result != res || row.Status != status {
		t.Fatalf("audit row %s/%d, want %s/%d", row.Result, row.Status, res, status)
	}
	return row
}

// TestProvEndpointAuth fails if an unknown token, a MAC mismatch, a
// disabled or unbound phone, or a per-device request over plain HTTP
// (including a spoofed X-Forwarded-Proto from an untrusted peer) is
// served; if any denial answers other than an empty 404; if plain HTTP
// does not flag token_exposed; or if If-None-Match with the current ETag
// does not answer 304 (spec S-4, S-6).
func TestProvEndpointAuth(t *testing.T) {
	e := newEnv(t)
	p, tok := e.s.addPhone(Yealink, "T54W", macA)
	path := devPath(tok, macA+".cfg")

	w := e.do(req{path: path, ua: "Yealink SIP-T54W 96.86.0.70 80:5e:c0:aa:bb:cc"})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "account.1.password = "+p.secret) {
		t.Fatalf("valid fetch: %d %q", w.Code, w.Body.String())
	}
	row := e.last()
	if row.Result != ResultServed || row.PhoneID != p.id || row.IP != netip.MustParseAddr("192.168.10.50") || row.UAMismatch {
		t.Fatalf("served row %+v", row)
	}
	if got := e.s.phone(p.id); got.firmwareSeen != "96.86.0.70" || got.lastFile != macA+".cfg" || got.armed {
		t.Fatalf("fetch state not recorded: %+v", got)
	}
	etag := w.Header().Get("ETag")

	// ETag re-check.
	w = e.do(req{path: path, inm: etag})
	if w.Code != http.StatusNotModified || w.Body.Len() != 0 || e.last().Result != ResultNotModified {
		t.Fatalf("If-None-Match: %d", w.Code)
	}
	if w = e.do(req{path: path, inm: `"stale"`}); w.Code != http.StatusOK {
		t.Fatalf("stale ETag: %d", w.Code)
	}

	// Denials, every one an empty 404.
	other, _ := NewToken()
	expectEmpty(t, e, e.do(req{path: devPath(other, macA+".cfg")}), 404, ResultUnknownToken)
	expectEmpty(t, e, e.do(req{path: devPath("short", macA+".cfg")}), 404, ResultUnknownToken)
	r := expectEmpty(t, e, e.do(req{path: devPath(tok, macB+".cfg")}), 404, ResultMACMismatch)
	if r.MACClaimed != macB {
		t.Fatalf("claimed MAC %q", r.MACClaimed)
	}
	expectEmpty(t, e, e.do(req{path: devPath(tok, strings.ToUpper(macB)+".cfg")}), 404, ResultMACMismatch)
	expectEmpty(t, e, e.do(req{path: devPath(tok, "snomD785.htm")}), 404, ResultNotFound)
	expectEmpty(t, e, e.do(req{path: devPath(tok, macA+"-local.cfg")}), 404, ResultNotFound)

	e.s.mu.Lock()
	e.s.phones[p.id].enabled = false
	e.s.mu.Unlock()
	expectEmpty(t, e, e.do(req{path: path}), 404, ResultNotAllowlisted)
	e.s.mu.Lock()
	e.s.phones[p.id].enabled, e.s.phones[p.id].bound = true, false
	e.s.mu.Unlock()
	expectEmpty(t, e, e.do(req{path: path}), 404, ResultNotAllowlisted)
	e.s.mu.Lock()
	e.s.phones[p.id].bound = true
	e.s.mu.Unlock()

	// Plain HTTP, and HTTPS claimed by a peer that is not a trusted proxy.
	if e.s.phone(p.id).exposed {
		t.Fatal("exposed before any plain-HTTP request")
	}
	expectEmpty(t, e, e.do(req{path: path, proto: "http"}), 404, ResultPlainHTTP)
	if !e.s.phone(p.id).exposed {
		t.Fatal("plain HTTP did not flag token_exposed")
	}
	row = expectEmpty(t, e, e.do(req{path: path, peer: "203.0.113.9:5000"}), 404, ResultPlainHTTP)
	if row.IP != netip.MustParseAddr("203.0.113.9") {
		t.Fatalf("an untrusted peer's X-Forwarded-For was believed: %v", row.IP)
	}
	if w = e.do(req{path: path, peer: "192.168.10.60:5000", tls: true}); w.Code != http.StatusOK {
		t.Fatalf("TLS on the listener: %d", w.Code)
	}

	// User-Agent naming another MAC: served, recorded as evidence.
	w = e.do(req{path: path, ua: "Yealink SIP-T54W 96.86.0.70 80:5e:c0:dd:ee:ff"})
	if w.Code != http.StatusOK || !e.last().UAMismatch || !e.s.phone(p.id).uaMismatch {
		t.Fatalf("ua mismatch: %d", w.Code)
	}

	// Uploads are discarded.
	w = e.do(req{method: http.MethodPut, path: devPath(tok, macA+"-app.log"), body: strings.Repeat("x", MaxUpload+10)})
	if w.Code != http.StatusNoContent || e.last().Result != ResultUploadDiscarded || e.last().Bytes != MaxUpload {
		t.Fatalf("upload: %d %+v", w.Code, e.last())
	}

	// Other paths and methods.
	expectEmpty(t, e, e.do(req{path: "/admin"}), 404, ResultNotFound)
	expectEmpty(t, e, e.do(req{method: http.MethodPost, path: path}), 404, ResultNotFound)
	expectEmpty(t, e, e.do(req{method: http.MethodPut, path: "/p/boot/cfg.xml"}), 404, ResultNotFound)

	// No template, a sealed value that does not open, a store outage.
	g, gtok := e.s.addPhone(Generic, "Acme 1", macB)
	expectEmpty(t, e, e.do(req{path: devPath(gtok, "acme-"+macB+".xml")}), 404, ResultNoTemplate)
	e.s.mu.Lock()
	e.s.phones[g.id].sealedBroken = true
	e.s.mu.Unlock()
	expectEmpty(t, e, e.do(req{path: devPath(gtok, "acme-"+macB+".xml")}), 404, ResultRenderError)
	e.s.mu.Lock()
	e.s.outage = errors.New("db down")
	e.s.mu.Unlock()
	w = e.do(req{path: path})
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "300" || w.Body.Len() != 0 {
		t.Fatalf("outage: %d %q", w.Code, w.Header().Get("Retry-After"))
	}
	e.s.mu.Lock()
	e.s.outage = nil
	e.s.mu.Unlock()

	// Firmware: under the token only, with Range; MinIO down is a 503.
	fw := Firmware{ID: 1, Vendor: Yealink, Filename: "T54W-96.86.0.70.rom", ObjectKey: "yealink/abc/T54W-96.86.0.70.rom", SHA256: strings.Repeat("a", 64), UploadedAt: time.Now()}
	e.s.firmware["yealink/"+fw.Filename] = fw
	e.o[fw.ObjectKey] = []byte("0123456789")
	hr := e.do(req{path: devPath(tok, "fw/"+fw.Filename)})
	if hr.Code != http.StatusOK || hr.Body.String() != "0123456789" {
		t.Fatalf("firmware: %d", hr.Code)
	}
	expectEmpty(t, e, e.do(req{path: devPath(other, "fw/"+fw.Filename)}), 404, ResultUnknownToken)
	expectEmpty(t, e, e.do(req{path: devPath(tok, "fw/"+fw.Filename), proto: "http"}), 404, ResultPlainHTTP)
	expectEmpty(t, e, e.do(req{path: "/p/boot/fw/" + fw.Filename}), 404, ResultNotFound)
	expectEmpty(t, e, e.do(req{path: devPath(tok, "fw/missing.rom")}), 404, ResultNotFound)
	delete(e.o, fw.ObjectKey)
	expectEmpty(t, e, e.do(req{path: devPath(tok, "fw/"+fw.Filename)}), 503, ResultUnavailable)
}

// TestFirmwareRange fails if firmware ignores Range.
func TestFirmwareRange(t *testing.T) {
	e := newEnv(t)
	_, tok := e.s.addPhone(Yealink, "T54W", macA)
	fw := Firmware{ID: 1, Vendor: Yealink, Filename: "f.rom", ObjectKey: "yealink/x/f.rom", SHA256: strings.Repeat("b", 64), UploadedAt: time.Now()}
	e.s.firmware["yealink/f.rom"] = fw
	e.o[fw.ObjectKey] = []byte("0123456789")
	hr := e.do(req{path: devPath(tok, "fw/f.rom"), headers: map[string]string{"Range": "bytes=2-5"}})
	if hr.Code != http.StatusPartialContent || hr.Body.String() != "2345" {
		t.Fatalf("range: %d %q", hr.Code, hr.Body.String())
	}
	if row := e.last(); row.Result != ResultServed || row.Bytes != 4 || row.Kind != KindFirmware {
		t.Fatalf("row %+v", row)
	}
}

// TestCACertificate fails if the CA is not served over plain HTTP as PEM
// and DER.
func TestCACertificate(t *testing.T) {
	dir := t.TempDir()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Hello lab CA"}, NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	file := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, func(o *Options) { o.CACertFile = file })
	w := e.do(req{path: "/p/ca.crt", proto: "http"})
	if b, _ := pem.Decode(w.Body.Bytes()); w.Code != 200 || b == nil || !bytes.Equal(b.Bytes, der) {
		t.Fatalf("ca.crt: %d", w.Code)
	}
	if w = e.do(req{path: "/p/ca.der", proto: "http"}); !bytes.Equal(w.Body.Bytes(), der) {
		t.Fatal("ca.der is not the DER certificate")
	}
	if w = e.do(req{path: "/p/ca.crt", accept: "application/pkix-cert"}); !bytes.Equal(w.Body.Bytes(), der) {
		t.Fatal("ca.crt with Accept: application/pkix-cert is not DER")
	}
	if e.last().Kind != KindCA {
		t.Fatal("not recorded as ca")
	}
	none := newEnv(t)
	expectEmpty(t, none, none.do(req{path: "/p/ca.crt"}), 404, ResultNotFound)
}

// TestFetchRaceWithRearmAndRotation fails if a fetch that matched a token
// before a concurrent re-arm still records the fetch and disarms the
// re-armed hand-off, or if a fetch with the current token that a rotation
// overtakes still ends the new rotation's grace (MarkFetched and
// PromoteToken are compare-and-set on the matched hash).
func TestFetchRaceWithRearmAndRotation(t *testing.T) {
	e := newEnv(t)
	p, t0 := e.s.addPhone(Yealink, "T54W", macA)
	file := macA + ".cfg"

	// A re-arm lands between PhoneByToken and MarkFetched.
	e.s.beforeMark = func() { e.s.rotate(p.id, true, true) }
	if w := e.do(req{path: devPath(t0, file)}); w.Code != 200 {
		t.Fatalf("fetch: %d", w.Code)
	}
	e.s.beforeMark = nil
	if ph := e.s.phone(p.id); !ph.armed || ph.fetched != 0 || !ph.firstFetch.IsZero() {
		t.Fatalf("the stale fetch changed the re-armed phone: armed %v, fetched %d, first %v", ph.armed, ph.fetched, ph.firstFetch)
	}

	// A rolling rotation lands between the current-token fetch's match
	// and its promote: the new previous token keeps its grace.
	t1 := e.s.rotate(p.id, false, false)
	var t2 string
	e.s.beforeMark = func() { t2 = e.s.rotate(p.id, false, false) }
	if w := e.do(req{path: devPath(t1, file)}); w.Code != 200 {
		t.Fatalf("fetch: %d", w.Code)
	}
	e.s.beforeMark = nil
	if toks := e.s.phone(p.id).tokens; !bytes.Equal(toks.Hash, HashToken(t2)) || !bytes.Equal(toks.PrevHash, HashToken(t1)) {
		t.Fatalf("the overtaken fetch promoted: %+v", toks)
	}
	if w := e.do(req{path: devPath(t1, file)}); w.Code != 200 {
		t.Fatal("the previous token lost its grace to an overtaken fetch")
	}
}

// TestTokenRollingRotation fails if the previous token stops working
// before the phone's first fetch with the new token or the grace period,
// if a previous-token fetch does not render the new provisioning URL, if
// immediate rotation leaves the old token valid, or if anything but the
// hash and the sealed token is stored (spec S-3).
func TestTokenRollingRotation(t *testing.T) {
	e := newEnv(t)
	p, t0 := e.s.addPhone(Yealink, "T54W", macA)
	file := macA + ".cfg"
	t1 := e.s.rotate(p.id, false, false)

	// The old token is served, with the new URL in its config.
	e.s.advance(6 * 24 * time.Hour)
	w := e.do(req{path: devPath(t0, file)})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "/p/"+t1+"/") || strings.Contains(w.Body.String(), t0) {
		t.Fatalf("previous-token fetch: %d, new URL present %v", w.Code, strings.Contains(w.Body.String(), t1))
	}
	if w = e.do(req{path: devPath(t0, file)}); w.Code != 200 {
		t.Fatal("previous token stopped working before the first new-token fetch")
	}
	// The first fetch with the new token ends the grace.
	if w = e.do(req{path: devPath(t1, file)}); w.Code != 200 {
		t.Fatalf("new token: %d", w.Code)
	}
	expectEmpty(t, e, e.do(req{path: devPath(t0, file)}), 404, ResultUnknownToken)

	// Grace expiry without a new-token fetch.
	t2 := e.s.rotate(p.id, false, false)
	e.s.advance(7*24*time.Hour - time.Second)
	if w = e.do(req{path: devPath(t1, file)}); w.Code != 200 {
		t.Fatal("previous token refused inside the grace")
	}
	e.s.advance(2 * time.Second)
	expectEmpty(t, e, e.do(req{path: devPath(t1, file)}), 404, ResultUnknownToken)
	if w = e.do(req{path: devPath(t2, file)}); w.Code != 200 {
		t.Fatal("current token refused")
	}

	// Immediate rotation.
	t3 := e.s.rotate(p.id, true, false)
	expectEmpty(t, e, e.do(req{path: devPath(t2, file)}), 404, ResultUnknownToken)
	if w = e.do(req{path: devPath(t3, file)}); w.Code != 200 {
		t.Fatal("token after immediate rotation refused")
	}

	// Only the hash and the sealed token are kept.
	toks := e.s.phone(p.id).tokens
	if !bytes.Equal(toks.Hash, HashToken(t3)) || toks.PrevHash != nil {
		t.Fatalf("stored tokens %+v", toks)
	}
	for _, b := range [][]byte{toks.Hash, toks.Enc, toks.PrevHash} {
		if bytes.Contains(b, []byte(t3)) {
			t.Fatal("a plaintext token is stored")
		}
	}
	rot := Tokens{Hash: []byte("h0")}.Rotate([]byte("h1"), []byte("e1"), time.Unix(0, 0), time.Hour, false)
	if string(rot.PrevHash) != "h0" || !rot.PrevExpires.Equal(time.Unix(3600, 0)) {
		t.Fatalf("rotate kept %+v", rot)
	}
}

// TestBootTrustOnFirstUse fails if a /p/boot/ response carries a SIP
// secret or an admin password; if a token is handed out other than once
// to an armed, allowlisted phone; if two concurrent first requests both
// get it; if a request after the hand-off does not get an empty 404, a
// boot_reclaim row and the reclaimed flag; if a request from outside
// HELLO_PROV_BOOT_CIDRS disarms; if the first HTTPS fetch does not disarm;
// or if re-arm does not revoke the old token (spec S-10).
func TestBootTrustOnFirstUse(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.BootCIDRs = []netip.Prefix{netip.MustParsePrefix("192.168.10.0/24")} })
	secretFree := func(t *testing.T, body string, p fakePhone) {
		t.Helper()
		if strings.Contains(body, p.secret) || strings.Contains(body, p.admin) {
			t.Fatalf("boot body carries a secret: %q", body)
		}
	}
	type vend struct {
		v             Vendor
		model, mac    string
		common, claim string
		before        []string // per-MAC names served before the hand-off without a claim
	}
	vendors := []vend{
		{Yealink, "T54W", "805ec0000001", "y000000000096.cfg", "805ec0000001.cfg", nil},
		{Poly, "VVX 450", "0004f2000002", "000000000000.cfg", "0004f2000002-hello.cfg", []string{"0004f2000002.cfg"}},
		{Grandstream, "GRP2614", "000b82000003", "cfg.xml", "cfg000b82000003.xml", nil},
		{Snom, "D785", "000413000004", "snomD785.htm", "snomD785-000413000004.htm", nil},
		{Fanvil, "X5U", "0c383e000005", "F0V00X5U0000.cfg", "0c383e000005.cfg", nil},
	}
	for _, vd := range vendors {
		p, tok := e.s.addPhone(vd.v, vd.model, vd.mac)
		w := e.do(req{path: "/p/boot/" + vd.common, proto: "http"})
		if w.Code != 200 || !strings.Contains(w.Body.String(), "http://prov.hello.test/p/") || e.last().Result != ResultBootServed {
			t.Fatalf("%s common: %d %q", vd.v, w.Code, w.Body.String())
		}
		secretFree(t, w.Body.String(), *p)
		for _, n := range vd.before {
			if w = e.do(req{path: "/p/boot/" + n, proto: "http"}); w.Code != 200 || e.last().Result != ResultBootServed || !e.s.phone(p.id).armed {
				t.Fatalf("%s %s: %d", vd.v, n, w.Code)
			}
		}
		// A wrong vendor's per-MAC name does not claim.
		if vd.v != Grandstream {
			expectEmpty(t, e, e.do(req{path: "/p/boot/cfg" + vd.mac + ".xml", proto: "http"}), 404, ResultNotFound)
		}
		// From outside the boot networks: denied, still armed.
		expectEmpty(t, e, e.do(req{path: "/p/boot/" + vd.claim, proto: "http", client: "10.9.9.9"}), 404, ResultBootDenied)
		if !e.s.phone(p.id).armed {
			t.Fatalf("%s: a denied source disarmed the phone", vd.v)
		}
		w = e.do(req{path: "/p/boot/" + vd.claim, proto: "http"})
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, "https://prov.hello.test/p/"+tok+"/") || !strings.Contains(body, "http://prov.hello.test/p/ca.crt") {
			t.Fatalf("%s hand-off: %d %q", vd.v, w.Code, body)
		}
		if vd.v == Snom && !strings.Contains(body, tok+"/{mac}") {
			t.Fatalf("snom hand-off without {mac}: %q", body)
		}
		secretFree(t, body, *p)
		if row := e.last(); row.Result != ResultBootHandoff || row.Kind != KindBoot || strings.Contains(row.PathRedacted, tok) {
			t.Fatalf("%s hand-off row %+v", vd.v, row)
		}
		got := e.s.phone(p.id)
		if got.armed || got.exposed {
			t.Fatalf("%s after hand-off: armed %v exposed %v", vd.v, got.armed, got.exposed)
		}
		// The same phone asking again in the same boot cycle (same
		// source, inside the grace) gets the same hand-off, not a reclaim.
		if w2 := e.do(req{path: "/p/boot/" + vd.claim, proto: "http"}); w2.Code != 200 || w2.Body.String() != body || e.last().Result != ResultBootHandoff {
			t.Fatalf("%s repeat hand-off: %d %q", vd.v, w2.Code, w2.Body.String())
		}
		if e.s.phone(p.id).reclaimed {
			t.Fatalf("%s: a repeat inside the grace flagged a reclaim", vd.v)
		}
		// Another source inside the grace is a reclaim.
		expectEmpty(t, e, e.do(req{path: "/p/boot/" + vd.claim, proto: "http", client: "192.168.10.66"}), 404, ResultBootReclaim)
		if !e.s.phone(p.id).reclaimed {
			t.Fatalf("%s: reclaim not flagged", vd.v)
		}
	}

	// After the grace, even the same source is a reclaim.
	late, _ := e.s.addPhone(Yealink, "T54W", "805ec0000012")
	if w := e.do(req{path: "/p/boot/805ec0000012.cfg", proto: "http"}); w.Code != 200 {
		t.Fatalf("late hand-off: %d", w.Code)
	}
	e.s.advance(BootHandoffGrace + time.Second)
	expectEmpty(t, e, e.do(req{path: "/p/boot/805ec0000012.cfg", proto: "http"}), 404, ResultBootReclaim)
	if !e.s.phone(late.id).reclaimed {
		t.Fatal("a repeat after the grace did not flag a reclaim")
	}

	// Unknown MAC, and an armed phone that is not allowlisted.
	expectEmpty(t, e, e.do(req{path: "/p/boot/805ec0999999.cfg", proto: "http"}), 404, ResultNotAllowlisted)
	off, _ := e.s.addPhone(Yealink, "T54W", "805ec0000010")
	e.s.mu.Lock()
	e.s.phones[off.id].enabled = false
	e.s.mu.Unlock()
	expectEmpty(t, e, e.do(req{path: "/p/boot/805ec0000010.cfg", proto: "http"}), 404, ResultNotAllowlisted)
	if !e.s.phone(off.id).armed || e.s.phone(off.id).reclaimed {
		t.Fatal("a non-allowlisted phone changed state")
	}

	// A sealed token that does not open: no hand-off, still armed.
	broken, _ := e.s.addPhone(Yealink, "T54W", "805ec0000011")
	e.s.mu.Lock()
	e.s.phones[broken.id].sealedBroken = true
	e.s.mu.Unlock()
	expectEmpty(t, e, e.do(req{path: "/p/boot/805ec0000011.cfg", proto: "http"}), 404, ResultRenderError)
	if !e.s.phone(broken.id).armed {
		t.Fatal("disarmed without a hand-off")
	}

	// Concurrent first requests from different sources: exactly one
	// hand-off.
	racer, _ := e.s.addPhone(Yealink, "T54W", "805ec0000020")
	var wg sync.WaitGroup
	codes := make(chan int, 20)
	for i := range 20 {
		wg.Go(func() {
			codes <- e.do(req{path: "/p/boot/805ec0000020.cfg", proto: "http", client: fmt.Sprintf("192.168.10.%d", 100+i)}).Code
		})
	}
	wg.Wait()
	close(codes)
	won := 0
	for c := range codes {
		if c == 200 {
			won++
		}
	}
	if won != 1 || !e.s.phone(racer.id).reclaimed {
		t.Fatalf("%d concurrent hand-offs", won)
	}

	// The first HTTPS fetch disarms a phone that got its token elsewhere.
	direct, dtok := e.s.addPhone(Yealink, "T54W", "805ec0000030")
	if w := e.do(req{path: devPath(dtok, "805ec0000030.cfg")}); w.Code != 200 {
		t.Fatal("direct fetch")
	}
	expectEmpty(t, e, e.do(req{path: "/p/boot/805ec0000030.cfg", proto: "http"}), 404, ResultBootReclaim)

	// Re-arm: the old token is revoked at once, and one more hand-off
	// carries the new one.
	fresh := e.s.rotate(direct.id, true, true)
	expectEmpty(t, e, e.do(req{path: devPath(dtok, "805ec0000030.cfg")}), 404, ResultUnknownToken)
	w := e.do(req{path: "/p/boot/805ec0000030.cfg", proto: "http"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), fresh) {
		t.Fatalf("re-armed hand-off: %d", w.Code)
	}
}
