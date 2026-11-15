package directory

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
)

// signRS256 builds a compact JWS (RS256) of header and claims: the signed
// assertions of Google's service-account grant (RFC 7523) and Entra's
// certificate credential.
func signRS256(key *rsa.PrivateKey, header, claims map[string]any) (string, error) {
	header["alg"], header["typ"] = "RS256", "JWT"
	h, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	c, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	input := enc.EncodeToString(h) + "." + enc.EncodeToString(c)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return input + "." + enc.EncodeToString(sig), nil
}

// parseRSAKey reads an RSA private key from PEM (PKCS #8 or PKCS #1).
func parseRSAKey(p string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(p))
	if block == nil {
		return nil, errors.New("not a PEM private key")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("the private key is not an RSA key")
		}
		return rk, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	return nil, errors.New("not a PKCS #8 or PKCS #1 RSA private key")
}

// randomID is a JWT id.
func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
