package auth

// Derived device credentials (clients-webrtc-mobile-video S-14). A web
// softphone's SIP username is <w|m><deviceId>.<expiryUnix> and its digest
// secret is base64url(HMAC-SHA256(key, "<username>:<realm>")). Nothing about
// the secret is stored: hello-control hands it out when the user enrols or
// refreshes a device, and hello-sip recomputes the digest HA1 from the same
// key when the client registers. A name that has passed its expiry is a bad
// credential, so a leaked username and secret stop working by themselves.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// Device kinds with derived credentials, and the username prefix of each.
const (
	KindWeb    = "web"
	KindMobile = "mobile"
)

// DerivedBase is the stable username of a derived device, w<id> or m<id>.
func DerivedBase(kind string, id int64) string {
	p := "w"
	if kind == KindMobile {
		p = "m"
	}
	return p + strconv.FormatInt(id, 10)
}

// DerivedUsername is the expiring username a client presents.
func DerivedUsername(base string, expires time.Time) string {
	return base + "." + strconv.FormatInt(expires.Unix(), 10)
}

// SplitDerived splits a presented username into its base and expiry. ok is
// false for a name that is not of the form <w|m><digits>.<digits>.
func SplitDerived(username string) (base string, expires time.Time, ok bool) {
	dot := strings.IndexByte(username, '.')
	if dot < 2 || username[0] != 'w' && username[0] != 'm' || !allDigits(username[1:dot]) || !allDigits(username[dot+1:]) {
		return "", time.Time{}, false
	}
	n, err := strconv.ParseInt(username[dot+1:], 10, 64)
	if err != nil || n <= 0 {
		return "", time.Time{}, false
	}
	return username[:dot], time.Unix(n, 0), true
}

func allDigits(s string) bool {
	if s == "" || len(s) > 20 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// DerivedSecret is the digest secret of username in realm under key.
func DerivedSecret(key []byte, username, realm string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(username + ":" + realm))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// DerivedHA1 is the digest HA1 pair (MD5, SHA-256) of a derived credential.
func DerivedHA1(key []byte, username, realm string) (md5Hex, sha256Hex string) {
	return HA1(username, realm, DerivedSecret(key, username, realm))
}
