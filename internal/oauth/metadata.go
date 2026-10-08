package oauth

import (
	"encoding/json"
	"html"
	"net/http"

	"github.com/azrtydxb/hello/internal/auth"
)

// scopesSupported are the scopes a client may request (session is the
// console's own and never granted).
func scopesSupported() []string {
	out := make([]string, len(auth.AllScopes))
	for i, s := range auth.AllScopes {
		out[i] = string(s)
	}
	return out
}

// serverMetadata is the RFC 8414 authorization server metadata (S-7).
func (s *Server) serverMetadata(w http.ResponseWriter, _ *http.Request) {
	m := map[string]any{
		"issuer":                                         s.public,
		"authorization_endpoint":                         s.public + "/oauth/authorize",
		"token_endpoint":                                 s.public + "/oauth/token",
		"revocation_endpoint":                            s.public + "/oauth/revoke",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token", "client_credentials"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none", "client_secret_basic", "client_secret_post"},
		"revocation_endpoint_auth_methods_supported":     []string{"none", "client_secret_basic", "client_secret_post"},
		"scopes_supported":                               scopesSupported(),
		"client_id_metadata_document_supported":          true,
		"authorization_response_iss_parameter_supported": true,
	}
	if s.o.DCR {
		m["registration_endpoint"] = s.public + "/oauth/register"
	}
	writeMetadata(w, m)
}

// resourceMetadata is the RFC 9728 protected resource metadata of resource.
func (s *Server) resourceMetadata(resource, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeMetadata(w, map[string]any{
			"resource":                 resource,
			"authorization_servers":    []string{s.public},
			"scopes_supported":         scopesSupported(),
			"bearer_methods_supported": []string{"header"},
			"resource_name":            name,
		})
	}
}

func writeMetadata(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_ = json.NewEncoder(w).Encode(v)
}

// errorPage answers an authorization error that must not be redirected
// (before the redirect URI is validated): a plain page, never framed.
func errorPage(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>Hello: authorization failed</title>` +
		`<h1>Authorization failed</h1><p>` + html.EscapeString(msg) + `</p>`))
}
