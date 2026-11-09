package prov

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/smallstep/scep"
	"github.com/smallstep/scep/x509util"
)

// fakeSCEP is an in-memory SCEPStore with the contract's semantics: one
// active CA, a one-time challenge per phone, issued certificates by serial,
// revocation.
type fakeSCEP struct {
	mu         sync.Mutex
	ca         *SCEPCA
	enabled    map[int64]bool
	challenges map[int64]string
	certs      map[string]*fakeCert // by serial
	signs      int
}

type fakeCert struct {
	phoneID int64
	caID    int64
	keyHash string
	cert    *x509.Certificate
	revoked bool
}

func newFakeSCEP(t *testing.T) *fakeSCEP {
	t.Helper()
	certPEM, key, err := GenerateSCEPCA("", 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cert, _, err := ParseSCEPCA(certPEM, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return &fakeSCEP{ca: &SCEPCA{ID: 1, Cert: cert, Key: key}, enabled: map[int64]bool{},
		challenges: map[int64]string{}, certs: map[string]*fakeCert{}}
}

func (f *fakeSCEP) SCEPState(_ context.Context, id int64) (SCEPPhone, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return SCEPPhone{Enabled: f.enabled[id], ValidityDays: 90, CA: f.ca}, nil
}

func (f *fakeSCEP) SCEPActiveCA(context.Context) (*SCEPCA, error) {
	if f.ca == nil {
		return nil, ErrNotFound
	}
	return f.ca, nil
}

func (f *fakeSCEP) SCEPIssue(_ context.Context, in SCEPIssue) (*x509.Certificate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.certs {
		if c.phoneID == in.PhoneID && c.caID == in.CAID && c.keyHash == in.KeySHA256 && !c.revoked &&
			time.Since(c.cert.NotBefore) < SCEPRetryWindow+scepBackdate {
			return c.cert, nil
		}
	}
	switch {
	case in.Challenge != "":
		if !ChallengeEqual(f.challenges[in.PhoneID], in.Challenge) {
			return nil, ErrSCEPRefused
		}
		delete(f.challenges, in.PhoneID)
	case in.RenewalSerial != "":
		c := f.certs[in.RenewalSerial]
		if c == nil || c.phoneID != in.PhoneID || c.revoked || c.caID != in.CAID {
			return nil, ErrSCEPRefused
		}
	default:
		return nil, ErrSCEPRefused
	}
	cert, err := in.Sign()
	if err != nil {
		return nil, err
	}
	f.signs++
	f.certs[SerialHex(cert.SerialNumber)] = &fakeCert{phoneID: in.PhoneID, caID: in.CAID, keyHash: in.KeySHA256, cert: cert}
	return cert, nil
}

func (f *fakeSCEP) SCEPRevoked(_ context.Context, caID int64) ([]x509.RevocationListEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []x509.RevocationListEntry
	for _, c := range f.certs {
		if c.revoked && c.caID == caID {
			out = append(out, x509.RevocationListEntry{SerialNumber: c.cert.SerialNumber, RevocationTime: time.Now()})
		}
	}
	return out, nil
}

// scepClient is a SCEP client the way a phone is one: a transient
// self-signed certificate signs the request, which is encrypted to the CA.
type scepClient struct {
	t      *testing.T
	e      *env
	token  string
	caCert *x509.Certificate
}

func (c *scepClient) get(op string) (int, int) {
	w := c.e.do(req{path: devPath(c.token, "scep") + "?operation=" + op})
	return w.Code, w.Body.Len()
}

func newKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func selfSigned(t *testing.T, key *rsa.PrivateKey, cn string) *x509.Certificate {
	t.Helper()
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// request builds a PKIOperation message: the CSR for key with challenge,
// signed by signer (a transient self-signed certificate for an enrolment,
// the current certificate for a renewal).
func (c *scepClient) request(typ scep.MessageType, key *rsa.PrivateKey, challenge string, signerCert *x509.Certificate, signerKey *rsa.PrivateKey) []byte {
	c.t.Helper()
	der, err := x509util.CreateCertificateRequest(rand.Reader, &x509util.CertificateRequest{
		CertificateRequest: x509.CertificateRequest{Subject: pkix.Name{CommonName: "whatever-the-phone-asks"},
			DNSNames: []string{"evil.example"}},
		ChallengePassword: challenge,
	}, key)
	if err != nil {
		c.t.Fatal(err)
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		c.t.Fatal(err)
	}
	msg, err := scep.NewCSRRequest(csr, &scep.PKIMessage{MessageType: typ, Recipients: []*x509.Certificate{c.caCert},
		SignerKey: signerKey, SignerCert: signerCert})
	if err != nil {
		c.t.Fatal(err)
	}
	return msg.Raw
}

// post sends a PKIOperation and returns the CertRep, decrypted with the
// signer's key when it carries a certificate.
func (c *scepClient) post(raw []byte, signerCert *x509.Certificate, signerKey *rsa.PrivateKey) *scep.PKIMessage {
	c.t.Helper()
	w := c.e.do(req{method: http.MethodPost, path: devPath(c.token, "scep") + "?operation=PKIOperation", body: string(raw)})
	return c.answer(w.Code, w.Header().Get("Content-Type"), w.Body.Bytes(), signerCert, signerKey)
}

func (c *scepClient) answer(code int, ctype string, body []byte, signerCert *x509.Certificate, signerKey *rsa.PrivateKey) *scep.PKIMessage {
	c.t.Helper()
	if code != http.StatusOK || ctype != "application/x-pki-message" {
		c.t.Fatalf("PKIOperation = %d %s", code, ctype)
	}
	rep, err := scep.ParsePKIMessage(body, scep.WithCACerts([]*x509.Certificate{c.caCert}))
	if err != nil {
		c.t.Fatalf("CertRep does not parse or verify against the CA: %v", err)
	}
	if rep.MessageType != scep.CertRep {
		c.t.Fatalf("answer type %s", rep.MessageType)
	}
	if rep.PKIStatus == scep.SUCCESS {
		if err := rep.DecryptPKIEnvelope(signerCert, signerKey); err != nil {
			c.t.Fatalf("decrypt CertRep: %v", err)
		}
	}
	return rep
}

// TestSCEPServer is the S-25 acceptance test: a full SCEP enrolment, retry,
// renewal and revocation round trip with a Go SCEP client against the
// provisioning handler. It fails if a certificate is issued without the
// phone's one-time challenge, with another phone's challenge or with a
// challenge already used, if a renewal is accepted from a certificate that
// is not a live one of the same phone, if SCEP answers while off for the
// phone's group or with HELLO_SCEP off, if the certificate does not chain
// to the CA or names anything but the phone's MAC, or if the CRL misses a
// revoked certificate.
func TestSCEPServer(t *testing.T) {
	sc := newFakeSCEP(t)
	e := newEnv(t, func(o *Options) { o.SCEP = sc })
	p, token := e.s.addPhone(Grandstream, "WP836", "c074ad112233")
	other, _ := e.s.addPhone(Grandstream, "WP836", "c074ad445566")
	c := &scepClient{t: t, e: e, token: token, caCert: sc.ca.Cert}

	// Off for the phone's group: every operation is an empty 404.
	for _, op := range []string{"GetCACaps", "GetCACert", "PKIOperation"} {
		if code, n := c.get(op); code != http.StatusNotFound || n > 0 {
			t.Fatalf("%s while off = %d", op, code)
		}
	}
	sc.enabled[p.id], sc.enabled[other.id] = true, true
	sc.challenges[p.id], sc.challenges[other.id] = NewSCEPChallenge(), NewSCEPChallenge()

	// HELLO_SCEP off: no SCEP store at all.
	off := newEnvWith(t, e.s, nil)
	if w := off.do(req{path: devPath(token, "scep") + "?operation=GetCACaps"}); w.Code != http.StatusNotFound {
		t.Fatalf("GetCACaps with HELLO_SCEP off = %d", w.Code)
	}
	if w := off.do(req{path: "/p/scep-ca.crl"}); w.Code != http.StatusNotFound {
		t.Fatalf("CRL with HELLO_SCEP off = %d", w.Code)
	}

	// Discovery.
	caps := e.do(req{path: devPath(token, "scep") + "?operation=GetCACaps"})
	if caps.Code != http.StatusOK || !strings.Contains(caps.Body.String(), "POSTPKIOperation") || !strings.Contains(caps.Body.String(), "Renewal") {
		t.Fatalf("GetCACaps = %d %q", caps.Code, caps.Body)
	}
	cacert := e.do(req{path: devPath(token, "scep/pkiclient.exe") + "?operation=GetCACert"})
	if got, err := x509.ParseCertificate(cacert.Body.Bytes()); cacert.Code != http.StatusOK || err != nil || !got.Equal(sc.ca.Cert) {
		t.Fatalf("GetCACert = %d, %v", cacert.Code, err)
	}
	if w := e.do(req{path: devPath(token, "scep") + "?operation=GetCACaps", proto: "http"}); w.Code != http.StatusNotFound {
		t.Fatalf("SCEP over plain HTTP = %d", w.Code)
	}
	if w := e.do(req{path: devPath(strings.Repeat("a", 52), "scep") + "?operation=GetCACaps"}); w.Code != http.StatusNotFound {
		t.Fatalf("SCEP with an unknown token = %d", w.Code)
	}
	if w := e.do(req{path: devPath(token, "scep") + "?operation=Nonsense"}); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown operation = %d", w.Code)
	}
	if w := e.do(req{method: http.MethodPost, path: devPath(token, "scep") + "?operation=PKIOperation", body: "not pkcs7"}); w.Code != http.StatusBadRequest {
		t.Fatalf("garbage PKIOperation = %d", w.Code)
	}

	key := newKey(t)
	transient := selfSigned(t, key, "transient")
	refused := func(name string, raw []byte, signer *x509.Certificate, sk *rsa.PrivateKey) {
		t.Helper()
		if rep := c.post(raw, signer, sk); rep.PKIStatus != scep.FAILURE {
			t.Fatalf("%s: status %s, want FAILURE", name, rep.PKIStatus)
		}
	}
	before := sc.signs
	refused("no challenge", c.request(scep.PKCSReq, key, "", transient, key), transient, key)
	refused("wrong challenge", c.request(scep.PKCSReq, key, "not-the-challenge", transient, key), transient, key)
	refused("another phone's challenge", c.request(scep.PKCSReq, key, sc.challenges[other.id], transient, key), transient, key)
	if sc.signs != before {
		t.Fatalf("a refused request signed %d certificates", sc.signs-before)
	}

	// The enrolment: POST with the phone's challenge.
	challenge := sc.challenges[p.id]
	enrol := c.request(scep.PKCSReq, key, challenge, transient, key)
	rep := c.post(enrol, transient, key)
	if rep.PKIStatus != scep.SUCCESS || rep.Certificate == nil {
		t.Fatalf("enrolment status %s", rep.PKIStatus)
	}
	cert := rep.Certificate
	pool := x509.NewCertPool()
	pool.AddCert(sc.ca.Cert)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("issued certificate does not chain to the CA for client auth: %v", err)
	}
	if cert.Subject.CommonName != p.mac || len(cert.DNSNames) != 0 || !cert.PublicKey.(*rsa.PublicKey).Equal(&key.PublicKey) {
		t.Fatalf("issued subject %q SANs %v: Hello must name the phone's MAC and nothing the request asks for", cert.Subject.CommonName, cert.DNSNames)
	}
	if len(cert.CRLDistributionPoints) != 1 || cert.CRLDistributionPoints[0] != "http://prov.hello.test/p/scep-ca.crl" {
		t.Fatalf("CRL distribution points %v", cert.CRLDistributionPoints)
	}
	if d := cert.NotAfter.Sub(cert.NotBefore); d < 89*24*time.Hour || d > 91*24*time.Hour {
		t.Fatalf("validity %v, want the group's 90 days", d)
	}

	// A retry of the same transaction (the phone lost the answer) gets the
	// same certificate back; nothing new is signed.
	if again := c.post(enrol, transient, key); again.PKIStatus != scep.SUCCESS || !again.Certificate.Equal(cert) {
		t.Fatalf("retry status %s", again.PKIStatus)
	}
	// The challenge is spent: a new key with it is refused, also as a GET.
	key2 := newKey(t)
	t2 := selfSigned(t, key2, "transient")
	reuse := c.request(scep.PKCSReq, key2, challenge, t2, key2)
	g := e.do(req{path: devPath(token, "scep") + "?operation=PKIOperation&message=" + url.QueryEscape(base64.StdEncoding.EncodeToString(reuse))})
	if r := c.answer(g.Code, g.Header().Get("Content-Type"), g.Body.Bytes(), t2, key2); r.PKIStatus != scep.FAILURE {
		t.Fatalf("a reused challenge got %s", r.PKIStatus)
	}

	// Renewal, signed with the issued certificate: a new certificate
	// without a challenge.
	renew := c.request(scep.RenewalReq, key2, "", cert, key)
	rr := c.post(renew, cert, key)
	if rr.PKIStatus != scep.SUCCESS || rr.Certificate.Equal(cert) || rr.Certificate.Subject.CommonName != p.mac {
		t.Fatalf("renewal status %s", rr.PKIStatus)
	}
	// A renewal signed by a certificate Hello did not issue is refused,
	// and so is one from another phone's certificate.
	key3 := newKey(t)
	refused("renewal by a stranger", c.request(scep.RenewalReq, key3, "", t2, key2), t2, key2)
	oc := &scepClient{t: t, e: e, token: token, caCert: sc.ca.Cert}
	otherKey := newKey(t)
	otherCert, err := DeviceCertificate(sc.ca, &x509.CertificateRequest{PublicKey: &otherKey.PublicKey}, other.mac, 30, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	sc.certs[SerialHex(otherCert.SerialNumber)] = &fakeCert{phoneID: other.id, caID: 1, cert: otherCert, keyHash: "x"}
	refused("renewal by another phone's certificate", oc.request(scep.RenewalReq, key3, "", otherCert, otherKey), otherCert, otherKey)

	// Revocation: the revoked certificate can no longer renew and is on
	// the CRL.
	sc.certs[SerialHex(rr.Certificate.SerialNumber)].revoked = true
	refused("renewal by a revoked certificate", c.request(scep.RenewalReq, key3, "", rr.Certificate, key2), rr.Certificate, key2)
	crlResp := e.do(req{path: "/p/scep-ca.crl", proto: "http"})
	crl, err := x509.ParseRevocationList(crlResp.Body.Bytes())
	if crlResp.Code != http.StatusOK || err != nil || crl.CheckSignatureFrom(sc.ca.Cert) != nil {
		t.Fatalf("CRL = %d, %v", crlResp.Code, err)
	}
	if len(crl.RevokedCertificateEntries) != 1 || crl.RevokedCertificateEntries[0].SerialNumber.Cmp(rr.Certificate.SerialNumber) != 0 {
		t.Fatalf("CRL entries %v", crl.RevokedCertificateEntries)
	}
	if w := e.do(req{path: "/p/scep-ca.crt", proto: "http"}); w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), "-----BEGIN CERTIFICATE-----") {
		t.Fatalf("scep-ca.crt = %d", w.Code)
	}

	// Switching the group off stops even a phone holding a challenge.
	sc.enabled[p.id] = false
	sc.challenges[p.id] = NewSCEPChallenge()
	if w := e.do(req{method: http.MethodPost, path: devPath(token, "scep") + "?operation=PKIOperation",
		body: string(c.request(scep.PKCSReq, key3, sc.challenges[p.id], selfSigned(t, key3, "t"), key3))}); w.Code != http.StatusNotFound {
		t.Fatalf("PKIOperation while off = %d", w.Code)
	}

	// The audit never records the token, and records the SCEP kind.
	var issued, refusedRows int
	for _, r := range e.flush() {
		if strings.Contains(r.PathRedacted, token) {
			t.Fatalf("audit path holds the token: %s", r.PathRedacted)
		}
		switch r.Result {
		case ResultSCEPIssued:
			issued++
		case ResultSCEPRefused:
			refusedRows++
		}
	}
	if issued != 3 || refusedRows < 6 {
		t.Fatalf("audit: %d issued, %d refused", issued, refusedRows)
	}
}

// TestSCEPCAImport fails if a CA that is not a CA, has no matching RSA key,
// or has expired is accepted, or a valid one is refused.
func TestSCEPCAImport(t *testing.T) {
	now := time.Now()
	certPEM, key, err := GenerateSCEPCA("Lab CA", 2, now)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustPKCS8(t, key)}))
	if _, _, err := ParseSCEPCA(certPEM, keyPEM, now); err != nil {
		t.Fatalf("a generated CA does not import: %v", err)
	}
	other := newKey(t)
	otherPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(other)}))
	leaf := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: selfSigned(t, other, "leaf").Raw}))
	for name, tc := range map[string][2]string{
		"wrong key":   {certPEM, otherPEM},
		"not a CA":    {leaf, otherPEM},
		"no cert":     {"", keyPEM},
		"garbage key": {certPEM, "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"},
	} {
		if _, _, err := ParseSCEPCA(tc[0], tc[1], now); err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "BEGIN") {
			t.Errorf("%s: the error quotes the input: %v", name, err)
		}
	}
	if _, _, err := ParseSCEPCA(certPEM, keyPEM, now.AddDate(3, 0, 0)); err == nil {
		t.Error("an expired CA was accepted")
	}
	k, err := MarshalRSAKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if back, err := UnmarshalRSAKey(k); err != nil || !back.Equal(key) {
		t.Fatalf("key round trip: %v", err)
	}
}

func mustPKCS8(t *testing.T, k *rsa.PrivateKey) []byte {
	t.Helper()
	b, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestSCEPChallenge fails if challenges repeat, compare equal when they
// differ, or an empty challenge matches an empty stored one.
func TestSCEPChallenge(t *testing.T) {
	a, b := NewSCEPChallenge(), NewSCEPChallenge()
	if a == b || len(a) != 32 || strings.Trim(a, "abcdefghijklmnopqrstuvwxyz234567") != "" {
		t.Fatalf("challenges %q %q", a, b)
	}
	if !ChallengeEqual(a, a) || ChallengeEqual(a, b) || ChallengeEqual("", "") {
		t.Fatal("ChallengeEqual")
	}
}
