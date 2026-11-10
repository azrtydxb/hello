package syslogsink

import (
	"regexp"
	"strings"
)

var (
	// authHeader matches the value of an Authorization or Proxy-Authorization
	// header, which carries a digest response.
	authHeader = regexp.MustCompile(`(?i)((?:proxy-)?authorization[ \t]*:)[^\r\n]*`)
	// secretKV matches key=value and key: value pairs whose key names a
	// credential, with a quoted or bare value. Digest parameters
	// (response, nonce, cnonce) and common password names are covered.
	secretKV = regexp.MustCompile(`(?i)\b((?:admin[_ -]?|sip[_ -]?|auth[_ -]?|user[_ -]?)?(?:pass(?:word|wd)?|pwd|secret|token|response|cnonce|nonce|api[_-]?key|credential)s?)(["']?\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&"']+)`)
	// provPath matches the token segment of a provisioning URL anywhere in a
	// line: /p/<token>/ becomes /p/****/. The fixed public names stay.
	provPath = regexp.MustCompile(`(/p/)([^/\s"'?#]+)`)
	// tokenRun is a token-like run, as prov.RedactPath finds: 52 or more
	// base32 characters.
	tokenRun = regexp.MustCompile(`[A-Za-z2-7]{52,}`)
	// userinfo matches user:password@ in a URL.
	userinfo = regexp.MustCompile(`(://[^/\s:@]+:)[^/\s@]+@`)
)

// Redact removes what must not be stored from a log line: Authorization
// header values, credential-named key/value pairs, the token of a
// provisioning URL, token-like runs and URL passwords. It is applied to
// every line before it is stored (phone-feature-parity S-27).
func Redact(s string) string {
	s = authHeader.ReplaceAllString(s, "${1} REDACTED")
	s = secretKV.ReplaceAllString(s, "${1}${2}REDACTED")
	s = userinfo.ReplaceAllString(s, "${1}REDACTED@")
	s = provPath.ReplaceAllStringFunc(s, func(m string) string {
		switch seg := strings.TrimPrefix(m, "/p/"); seg {
		case "boot", "ca.crt", "ca.der":
			return m
		}
		return "/p/****"
	})
	return tokenRun.ReplaceAllString(s, "****")
}
