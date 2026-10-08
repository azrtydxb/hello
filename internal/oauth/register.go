package oauth

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"

	"github.com/azrtydxb/hello/internal/auth"
)

// registration is the RFC 7591 client metadata Hello reads.
type registration struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	ClientURI               string   `json:"client_uri"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
}

func registerError(w http.ResponseWriter, status int, code, desc string) {
	noStore(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": desc})
}

// register is POST /oauth/register (RFC 7591, spec S-10): public clients
// only, the redirect URI rules of client ID metadata documents, 10 per
// hour per IP. It is mounted only when DCR is on.
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if ok, err := s.o.Limiter.Allow(r.Context(), "register:"+clientIP(r), registerLimit, registerWindow); err == nil && !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(registerWindow.Seconds())))
		registerError(w, http.StatusTooManyRequests, "slow_down", "too many registrations; retry later")
		return
	}
	var in registration
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxForm))
	if err := dec.Decode(&in); err != nil {
		registerError(w, http.StatusBadRequest, "invalid_client_metadata", "the body must be a JSON object")
		return
	}
	if m := in.TokenEndpointAuthMethod; m != "" && m != "none" {
		registerError(w, http.StatusBadRequest, "invalid_client_metadata", "only public clients (token_endpoint_auth_method none) may register")
		return
	}
	for _, g := range in.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			registerError(w, http.StatusBadRequest, "invalid_client_metadata", "grant_types may be authorization_code and refresh_token")
			return
		}
	}
	if len(in.ResponseTypes) > 0 && !slices.Equal(in.ResponseTypes, []string{"code"}) {
		registerError(w, http.StatusBadRequest, "invalid_client_metadata", "response_types must be code")
		return
	}
	if len(in.RedirectURIs) == 0 {
		registerError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris is required")
		return
	}
	for _, u := range in.RedirectURIs {
		if !validRedirect(u) {
			registerError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect URIs must be https, or http on a loopback address")
			return
		}
	}
	if len(in.ClientName) > 200 || len(in.ClientURI) > 2000 {
		registerError(w, http.StatusBadRequest, "invalid_client_metadata", "client_name or client_uri is too long")
		return
	}
	name := in.ClientName
	if name == "" {
		name = "Unnamed client"
	}
	tok, _ := auth.NewToken()
	c := Client{ID: "hello_dcr_" + tok[:22], Kind: ClientDCR, Name: name, ClientURI: in.ClientURI, RedirectURIs: in.RedirectURIs}
	c.Metadata, _ = json.Marshal(in)
	now := s.now()
	if err := s.o.Store.RegisterClient(r.Context(), "oauth:register", c); err != nil {
		s.o.Log.Error("oauth register", "error", err)
		registerError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "the authorization server is unavailable")
		return
	}
	s.o.Log.Info("oauth client registered", "client_id", c.ID)
	noStore(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"client_id": c.ID, "client_id_issued_at": now.Unix(), "client_name": c.Name, "client_uri": c.ClientURI,
		"redirect_uris": c.RedirectURIs, "token_endpoint_auth_method": "none",
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
	})
}
