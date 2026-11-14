package sso

import (
	"context"
	"crypto/subtle"
	"net/url"

	"golang.org/x/oauth2"
)

// Consents is what a directory connector's administrator consent needs of the
// directory engine (Google oauth_admin, I-21).
type Consents interface {
	ConsentURL(ctx context.Context, id int64, redirectURI, state, verifier string) (string, error)
	CompleteConsent(ctx context.Context, actor string, id int64, code, verifier, redirectURI string) error
}

// StartConsent begins connector id's consent for the administrator actor: it
// returns the URL to send the browser to and the binding cookie value. The
// state, PKCE verifier and binding are single use, like a sign-in's.
func (s *Service) StartConsent(ctx context.Context, c Consents, actor string, id int64) (string, string, error) {
	if !s.available() {
		return "", "", ErrUnavailable
	}
	state, binding := random(32), random(32)
	verifier := oauth2.GenerateVerifier()
	u, err := c.ConsentURL(ctx, id, s.ConsentRedirectURI(), state, verifier)
	if err != nil {
		return "", "", err
	}
	st := State{Kind: KindConsent, Verifier: verifier, Binding: bindingHash(binding), ConnectorID: id, Actor: actor}
	if err := s.States.Put(ctx, state, st, StateTTL); err != nil {
		return "", "", ErrUnavailable
	}
	return u, binding, nil
}

// FinishConsent completes a consent callback and returns the connector id.
func (s *Service) FinishConsent(ctx context.Context, c Consents, q url.Values, binding string) (int64, error) {
	if !s.available() {
		return 0, ErrUnavailable
	}
	stateParam := q.Get("state")
	if stateParam == "" || len(stateParam) > 256 {
		return 0, ErrState
	}
	st, err := s.States.Take(ctx, stateParam)
	if err != nil {
		return 0, err
	}
	if st.Kind != KindConsent || binding == "" || subtle.ConstantTimeCompare([]byte(bindingHash(binding)), []byte(st.Binding)) != 1 {
		return 0, ErrState
	}
	code := q.Get("code")
	if q.Get("error") != "" || code == "" || len(code) > 4096 {
		return st.ConnectorID, ErrDenied
	}
	if err := c.CompleteConsent(ctx, st.Actor, st.ConnectorID, code, st.Verifier, s.ConsentRedirectURI()); err != nil {
		return st.ConnectorID, err
	}
	return st.ConnectorID, nil
}
