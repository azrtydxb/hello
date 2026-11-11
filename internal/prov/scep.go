package prov

// SCEP (RFC 8894) on the provisioning listener (spec phone-feature-parity
// S-25): Hello issues device certificates from the Hello device CA to the
// phones of a phone group with SCEP switched on. A phone reaches the server
// at /p/<token>/scep, so the token says which phone asks; a first enrolment
// (PKCSReq) must carry the phone's current one-time challenge password, which
// the enrolment consumes; a renewal (RenewalReq) is signed with a live
// certificate Hello issued to the same phone. The PKCS#7 and SCEP message
// handling is github.com/smallstep/scep (the library step-ca uses); this file
// only decides who gets what certificate.

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/smallstep/pkcs7"
	"github.com/smallstep/scep"
)

func init() {
	// The CertRep envelope (the issued certificate, encrypted to the
	// phone's transient key) uses AES-128-CBC, which GetCACaps advertises
	// ("AES"), instead of the library default DES-CBC. The value is a
	// package variable of smallstep/pkcs7; nothing else in Hello encrypts
	// PKCS#7.
	pkcs7.ContentEncryptionAlgorithm = pkcs7.EncryptionAlgorithmAES128CBC
}

// SCEP limits and defaults.
const (
	// SCEPMaxMessage bounds a PKIOperation message (a CSR in a signed
	// envelope is a few KiB).
	SCEPMaxMessage = 64 << 10
	// SCEPCAKeyBits is the RSA size of a generated CA key. SCEP clients
	// encrypt their request to the CA certificate, so the CA key must be
	// RSA (RFC 8894 section 3.5.1).
	SCEPCAKeyBits = 3072
	// SCEPDefaultValidityDays is a device certificate's lifetime unless the
	// phone group says otherwise; the phone renews before it ends
	// (Grandstream P8662, 20 days by default).
	SCEPDefaultValidityDays = 365
	// SCEPCRLValidity is how long a served CRL is valid (its NextUpdate).
	SCEPCRLValidity = 24 * time.Hour
	// SCEPRetryWindow is how long a request with the same phone, CA and
	// key gets the certificate already issued back (a phone retrying a
	// transaction whose answer it lost) instead of a new one.
	SCEPRetryWindow = 10 * time.Minute
	// scepBackdate covers a phone whose clock runs a little behind.
	scepBackdate = 5 * time.Minute
)

// SCEPCaps is the GetCACaps answer (RFC 8894 section 3.5.2): POST, AES,
// SHA-256 and renewal, and SCEPStandard, which implies the first three.
const SCEPCaps = "AES\nPOSTPKIOperation\nRenewal\nSCEPStandard\nSHA-256\nSHA-512\n"

// SCEPCA is the active Hello device CA with its opened private key.
type SCEPCA struct {
	ID   int64
	Cert *x509.Certificate
	Key  *rsa.PrivateKey
}

// SCEPPhone is what the SCEP endpoint needs to know about the asking phone.
type SCEPPhone struct {
	// Enabled is the phone group's SCEP switch; a phone without a group has
	// SCEP off.
	Enabled bool
	// ValidityDays is the group's certificate lifetime.
	ValidityDays int
	// CA is the active CA, nil when none has been created.
	CA *SCEPCA
}

// SCEPIssue is one issuance the store authorises and records atomically.
type SCEPIssue struct {
	PhoneID int64
	MAC     string
	CAID    int64
	// Challenge is the challenge password of a first enrolment (PKCSReq);
	// it must be the phone's current one, which the issue consumes.
	Challenge string
	// RenewalSerial is the hex serial of the certificate that signed a
	// renewal (RenewalReq); it must be a live certificate of the same
	// phone from the same CA. Exactly one of Challenge and RenewalSerial is
	// set.
	RenewalSerial string
	// KeySHA256 is the hex SHA-256 of the requested public key (DER).
	KeySHA256 string
	// Sign makes the certificate; called only once the request is
	// authorised, inside the store's transaction.
	Sign func() (*x509.Certificate, error)
}

// ErrSCEPRefused is a SCEPIssue the store refuses: a wrong, used or missing
// challenge, or a renewal signed by a certificate that is not a live one of
// the phone. It never says which, to the phone.
var ErrSCEPRefused = errors.New("prov: scep request refused")

// SCEPStore is the SCEP endpoint's view of the database; internal/store
// implements it.
type SCEPStore interface {
	// SCEPState returns the phone's group switch and lifetime and the
	// active CA (nil when none). ErrSealed when the CA key does not open.
	SCEPState(ctx context.Context, phoneID int64) (SCEPPhone, error)
	// SCEPActiveCA returns the active CA; ErrNotFound when there is none.
	SCEPActiveCA(ctx context.Context) (*SCEPCA, error)
	// SCEPIssue authorises and records one issuance under the phone's row
	// lock. A live certificate of the phone from the same CA with the same
	// key hash, issued within SCEPRetryWindow, is returned as it is, without
	// consuming anything or calling Sign (a phone retrying a transaction
	// whose answer it lost).
	// Otherwise the challenge or renewal serial is checked (ErrSCEPRefused),
	// the challenge consumed, Sign called and its certificate recorded.
	SCEPIssue(ctx context.Context, in SCEPIssue) (*x509.Certificate, error)
	// SCEPRevoked lists the revoked certificates of a CA for its CRL.
	SCEPRevoked(ctx context.Context, caID int64) ([]x509.RevocationListEntry, error)
}

// Sealing contexts of the SCEP and 802.1X secrets.

// SCEPCAKeyAAD is the context of a CA's sealed private key.
func SCEPCAKeyAAD(caID int64) string { return "scep-ca:" + strconv.FormatInt(caID, 10) }

// SCEPChallengeAAD is the context of a phone's sealed challenge password.
func SCEPChallengeAAD(phoneID int64) string {
	return "scep-challenge:" + strconv.FormatInt(phoneID, 10)
}

// Dot1XPasswordAAD is the context of an 802.1X profile's sealed password.
func Dot1XPasswordAAD(profileID int64) string {
	return "dot1x:" + strconv.FormatInt(profileID, 10)
}

// NewSCEPChallenge returns a fresh one-time challenge password: 20 random
// bytes as 32 base32 characters (letters and digits only, so it survives
// every configuration format unescaped).
func NewSCEPChallenge() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// ChallengeEqual compares two challenges in constant time.
func ChallengeEqual(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// GenerateSCEPCA makes a self-signed device CA: RSA 3072, the common name
// given (default "Hello device CA"), valid for years (1 to 30, default 10).
// It returns the certificate as PEM and the key.
func GenerateSCEPCA(commonName string, years int, now time.Time) (string, *rsa.PrivateKey, error) {
	if commonName == "" {
		commonName = "Hello device CA"
	}
	if years <= 0 {
		years = 10
	}
	key, err := rsa.GenerateKey(rand.Reader, SCEPCAKeyBits)
	if err != nil {
		return "", nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return "", nil, err
	}
	ski := keyID(&key.PublicKey)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"Kuvryn Hello"}},
		NotBefore:             now.Add(-scepBackdate),
		NotAfter:              now.AddDate(years, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
		SubjectKeyId:          ski,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", nil, err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), key, nil
}

// ParseSCEPCA checks an imported CA: one PEM certificate that is a CA
// allowed to sign certificates and is valid at now, and its RSA private key
// (PKCS#1 or PKCS#8 PEM). The messages never quote the input.
func ParseSCEPCA(certPEM, keyPEM string, now time.Time) (*x509.Certificate, *rsa.PrivateKey, error) {
	cb, _ := pem.Decode([]byte(certPEM))
	if cb == nil || cb.Type != "CERTIFICATE" {
		return nil, nil, errors.New("certPem must be one PEM certificate")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, errors.New("certPem does not parse as an X.509 certificate")
	}
	if !cert.IsCA || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, nil, errors.New("certPem is not a CA certificate allowed to sign certificates")
	}
	if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
		return nil, nil, errors.New("certPem is not valid now")
	}
	key, err := ParseRSAKey(keyPEM)
	if err != nil {
		return nil, nil, err
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, nil, errors.New("keyPem is not the key of certPem (SCEP needs an RSA CA key)")
	}
	return cert, key, nil
}

// ParseRSAKey reads an RSA private key from PEM (PKCS#1 or PKCS#8).
func ParseRSAKey(keyPEM string) (*rsa.PrivateKey, error) {
	kb, _ := pem.Decode([]byte(keyPEM))
	if kb == nil {
		return nil, errors.New("keyPem must be one PEM private key")
	}
	if k, err := x509.ParsePKCS1PrivateKey(kb.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return nil, errors.New("keyPem does not parse as a private key")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("keyPem must be an RSA key (SCEP needs an RSA CA key)")
	}
	return rk, nil
}

// MarshalRSAKey is the PKCS#8 DER of a key, the form a CA key is sealed in.
func MarshalRSAKey(k *rsa.PrivateKey) ([]byte, error) { return x509.MarshalPKCS8PrivateKey(k) }

// UnmarshalRSAKey reverses MarshalRSAKey.
func UnmarshalRSAKey(der []byte) (*rsa.PrivateKey, error) {
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("prov: the CA key is not RSA")
	}
	return rk, nil
}

// CertFingerprint is the uppercase colon-separated SHA-256 of a
// certificate's DER, as administrators compare it.
func CertFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	parts := make([]string, 0, len(sum))
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

// SerialHex is a certificate serial as stored: lowercase hex.
func SerialHex(n *big.Int) string { return n.Text(16) }

// PublicKeySHA256 is the hex SHA-256 of a public key's DER.
func PublicKeySHA256(pub crypto.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:]), nil
}

func randomSerial() (*big.Int, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	return n.Add(n, big.NewInt(1)), nil
}

func keyID(pub crypto.PublicKey) []byte {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(der)
	return sum[:20]
}

// checkCSRKey refuses a key too weak for a device certificate: RSA under
// 2048 bits, or an EC curve other than P-256 and P-384.
func checkCSRKey(pub crypto.PublicKey) error {
	switch k := pub.(type) {
	case *rsa.PublicKey:
		if k.N.BitLen() < 2048 {
			return errors.New("RSA key under 2048 bits")
		}
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P256() && k.Curve != elliptic.P384() {
			return errors.New("EC key not on P-256 or P-384")
		}
	default:
		return fmt.Errorf("unsupported key type %T", pub)
	}
	return nil
}

// DeviceCertificate is the certificate Hello issues for a phone: subject
// CN = the phone's MAC (12 lowercase hex digits), client authentication,
// valid for days (capped at the CA's own end), with the CRL at crlURL.
func DeviceCertificate(ca *SCEPCA, csr *x509.CertificateRequest, mac string, days int, crlURL string, now time.Time) (*x509.Certificate, error) {
	if days <= 0 {
		days = SCEPDefaultValidityDays
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	notAfter := now.AddDate(0, 0, days)
	if notAfter.After(ca.Cert.NotAfter) {
		notAfter = ca.Cert.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber:   serial,
		Subject:        pkix.Name{CommonName: mac, Organization: ca.Cert.Subject.Organization},
		NotBefore:      now.Add(-scepBackdate),
		NotAfter:       notAfter,
		KeyUsage:       x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:    []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		SubjectKeyId:   keyID(csr.PublicKey),
		AuthorityKeyId: ca.Cert.SubjectKeyId,
	}
	if crlURL != "" {
		tmpl.CRLDistributionPoints = []string{crlURL}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, csr.PublicKey, ca.Key)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

// SCEPCRL is the CA's certificate revocation list (DER), valid for
// SCEPCRLValidity from now; its number is the Unix time, so it grows.
func SCEPCRL(ca *SCEPCA, revoked []x509.RevocationListEntry, now time.Time) ([]byte, error) {
	return x509.CreateRevocationList(rand.Reader, &x509.RevocationList{
		Number:                    big.NewInt(now.Unix()),
		ThisUpdate:                now,
		NextUpdate:                now.Add(SCEPCRLValidity),
		RevokedCertificateEntries: revoked,
	}, ca.Cert, ca.Key)
}

// SCEPURL is the SCEP server URL a phone is given: its own provisioning URL
// (which carries its token) followed by "scep".
func SCEPURL(phoneBase string) string {
	if phoneBase == "" {
		return ""
	}
	return strings.TrimSuffix(phoneBase, "/") + "/scep"
}

// SCEPCRLURL is the plain-HTTP URL of the device CA's CRL, which a RADIUS
// server can fetch without a token.
func SCEPCRLURL(public *url.URL) string {
	if public == nil {
		return ""
	}
	return "http://" + public.Host + "/p/scep-ca.crl"
}

// isSCEPName reports whether a per-device name is the SCEP endpoint: "scep"
// itself or the CGI path SCEP clients may append to the URL.
func isSCEPName(name string) bool {
	switch name {
	case "scep", "scep/pkiclient.exe", "scep/cgi-bin/pkiclient.exe":
		return true
	}
	return false
}

// serveSCEP answers /p/<token>/scep. Every request is authorised like a
// configuration fetch (token, HTTPS, allowlisted phone); a phone whose group
// has SCEP off, or a Hello without a CA, gets the same empty 404 as an
// unknown path.
func (h *handler) serveSCEP(w http.ResponseWriter, r *http.Request, ev *event, token string, https bool) {
	ctx := r.Context()
	ev.rec.Kind = KindSCEP
	if h.opt.SCEP == nil {
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	}
	rec, ok := h.authorize(w, r, ev, token, https)
	if !ok {
		return
	}
	if !h.l.Phone(ctx, rec.ID) {
		h.empty(w, ev, http.StatusTooManyRequests, ResultRateLimited)
		return
	}
	st, err := h.opt.SCEP.SCEPState(ctx, rec.ID)
	switch {
	case errors.Is(err, ErrNotFound):
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	case errors.Is(err, ErrSealed):
		h.opt.Log.Error("provisioning: the SCEP CA key does not open under HELLO_SECRET_KEY")
		h.empty(w, ev, http.StatusNotFound, ResultRenderError)
		return
	case err != nil:
		h.outage(w, ev, "scep state", err)
		return
	}
	if !st.Enabled || st.CA == nil {
		h.scepCount("any", "off")
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	}
	op := r.URL.Query().Get("operation")
	switch {
	case op == "GetCACaps" && r.Method != http.MethodPost:
		h.scepCount("GetCACaps", "ok")
		h.body(w, ev, ResultServed, "text/plain", []byte(SCEPCaps))
	case op == "GetCACert" && r.Method != http.MethodPost:
		h.scepCount("GetCACert", "ok")
		h.body(w, ev, ResultServed, "application/x-x509-ca-cert", st.CA.Cert.Raw)
	case op == "PKIOperation":
		h.scepPKIOperation(w, r, ev, rec, st)
	default:
		h.scepCount("unknown", "bad_request")
		h.empty(w, ev, http.StatusBadRequest, ResultSCEPRefused)
	}
}

// scepPKIOperation handles an enrolment or renewal request.
func (h *handler) scepPKIOperation(w http.ResponseWriter, r *http.Request, ev *event, rec PhoneRecord, st SCEPPhone) {
	ctx := r.Context()
	var raw []byte
	switch r.Method {
	case http.MethodPost:
		b, err := io.ReadAll(io.LimitReader(r.Body, SCEPMaxMessage+1))
		if err != nil || len(b) > SCEPMaxMessage {
			h.scepCount("PKIOperation", "bad_request")
			h.empty(w, ev, http.StatusBadRequest, ResultSCEPRefused)
			return
		}
		raw = b
	default:
		m := r.URL.Query().Get("message")
		if len(m) > SCEPMaxMessage*2 {
			h.scepCount("PKIOperation", "bad_request")
			h.empty(w, ev, http.StatusBadRequest, ResultSCEPRefused)
			return
		}
		b, err := base64.StdEncoding.DecodeString(m)
		if err != nil {
			h.scepCount("PKIOperation", "bad_request")
			h.empty(w, ev, http.StatusBadRequest, ResultSCEPRefused)
			return
		}
		raw = b
	}
	ca := st.CA
	msg, err := scep.ParsePKIMessage(raw)
	if err != nil {
		h.scepCount("PKIOperation", "bad_request")
		h.empty(w, ev, http.StatusBadRequest, ResultSCEPRefused)
		return
	}
	if err := msg.DecryptPKIEnvelope(ca.Cert, ca.Key); err != nil {
		h.scepFail(w, ev, msg, ca, "undecryptable", scep.BadMessageCheck)
		return
	}
	csr := msg.CSR
	if err := csr.CheckSignature(); err != nil {
		h.scepFail(w, ev, msg, ca, "bad_csr", scep.BadMessageCheck)
		return
	}
	if err := checkCSRKey(csr.PublicKey); err != nil {
		h.scepFail(w, ev, msg, ca, "weak_key", scep.BadAlg)
		return
	}
	keyHash, err := PublicKeySHA256(csr.PublicKey)
	if err != nil {
		h.scepFail(w, ev, msg, ca, "bad_csr", scep.BadRequest)
		return
	}
	in := SCEPIssue{PhoneID: rec.ID, MAC: rec.MAC, CAID: ca.ID, KeySHA256: keyHash}
	switch msg.MessageType {
	case scep.PKCSReq:
		in.Challenge = msg.ChallengePassword
		if in.Challenge == "" {
			h.scepFail(w, ev, msg, ca, "no_challenge", scep.BadRequest)
			return
		}
	case scep.RenewalReq, scep.UpdateReq:
		serial, ok := renewalSigner(raw, ca.Cert, time.Now())
		if !ok {
			h.scepFail(w, ev, msg, ca, "bad_renewal", scep.BadMessageCheck)
			return
		}
		in.RenewalSerial = serial
	default:
		h.scepFail(w, ev, msg, ca, "unsupported", scep.BadRequest)
		return
	}
	crlURL := SCEPCRLURL(h.opt.PublicURL)
	in.Sign = func() (*x509.Certificate, error) {
		return DeviceCertificate(ca, csr, rec.MAC, st.ValidityDays, crlURL, time.Now())
	}
	cert, err := h.opt.SCEP.SCEPIssue(ctx, in)
	switch {
	case errors.Is(err, ErrSCEPRefused):
		h.l.Denied(ctx, ev.rec.IP)
		h.scepFail(w, ev, msg, ca, "refused", scep.BadRequest)
		return
	case errors.Is(err, ErrNotFound):
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	case err != nil:
		h.outage(w, ev, "scep issue", err)
		return
	}
	rep, err := msg.Success(ca.Cert, ca.Key, cert)
	if err != nil {
		h.opt.Log.Error("provisioning: build the SCEP answer", "phone", rec.ID, "error", err)
		h.empty(w, ev, http.StatusInternalServerError, ResultRenderError)
		return
	}
	h.scepCount("PKIOperation", "issued")
	h.body(w, ev, ResultSCEPIssued, "application/x-pki-message", rep.Raw)
}

// scepFail answers a refused PKIOperation with a signed CertRep FAILURE
// (HTTP 200, as RFC 8894 section 3.3.2 has it), never saying more than the
// fail info.
func (h *handler) scepFail(w http.ResponseWriter, ev *event, msg *scep.PKIMessage, ca *SCEPCA, why string, info scep.FailInfo) {
	h.scepCount("PKIOperation", why)
	rep, err := msg.Fail(ca.Cert, ca.Key, info)
	if err != nil {
		h.empty(w, ev, http.StatusBadRequest, ResultSCEPRefused)
		return
	}
	w.Header().Set("Content-Type", "application/x-pki-message")
	w.Header().Set("Content-Length", strconv.Itoa(len(rep.Raw)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rep.Raw) //nolint:gosec // G705: a signed PKCS#7 CertRep, application/x-pki-message, not HTML
	ev.rec.Status, ev.rec.Result, ev.rec.Bytes = http.StatusOK, ResultSCEPRefused, int64(len(rep.Raw))
}

func (h *handler) scepCount(op, result string) {
	h.opt.Metrics.SCEP.WithLabelValues(op, result).Inc()
}

// renewalSigner returns the hex serial of the certificate that signed a
// renewal request, when it chains to the CA, is for client authentication
// and is valid at now. The message signature itself was checked by
// ParsePKIMessage.
func renewalSigner(raw []byte, caCert *x509.Certificate, now time.Time) (string, bool) {
	p7, err := pkcs7.Parse(raw)
	if err != nil {
		return "", false
	}
	signer := p7.GetOnlySigner()
	if signer == nil {
		return "", false
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := signer.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return "", false
	}
	return SerialHex(signer.SerialNumber), true
}

// servePublicSCEP serves the device CA (PEM, or DER for
// application/pkix-cert) at scep-ca.crt and its CRL at scep-ca.crl, over
// plain HTTP too: both are public, and a RADIUS server fetches the CRL
// without a token.
func (h *handler) servePublicSCEP(w http.ResponseWriter, r *http.Request, ev *event, name string) {
	ctx := r.Context()
	ev.rec.Kind = KindCA
	if h.opt.SCEP == nil {
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	}
	ca, err := h.opt.SCEP.SCEPActiveCA(ctx)
	switch {
	case errors.Is(err, ErrNotFound):
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	case errors.Is(err, ErrSealed):
		h.opt.Log.Error("provisioning: the SCEP CA key does not open under HELLO_SECRET_KEY")
		h.empty(w, ev, http.StatusNotFound, ResultRenderError)
		return
	case err != nil:
		h.outage(w, ev, "scep ca", err)
		return
	}
	if name == "scep-ca.crt" {
		if strings.Contains(r.Header.Get("Accept"), "application/pkix-cert") {
			h.body(w, ev, ResultServed, "application/pkix-cert", ca.Cert.Raw)
			return
		}
		h.body(w, ev, ResultServed, "application/x-pem-file", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Cert.Raw}))
		return
	}
	revoked, err := h.opt.SCEP.SCEPRevoked(ctx, ca.ID)
	if err != nil {
		h.outage(w, ev, "scep revoked", err)
		return
	}
	crl, err := SCEPCRL(ca, revoked, time.Now())
	if err != nil {
		h.opt.Log.Error("provisioning: sign the SCEP CRL", "error", err)
		h.empty(w, ev, http.StatusInternalServerError, ResultRenderError)
		return
	}
	w.Header().Set("Cache-Control", "max-age=300")
	h.body(w, ev, ResultServed, "application/pkix-crl", crl)
}
