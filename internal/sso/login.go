package sso

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/directory"
)

// LDAP bind login throttle (I-30): failures per user and per address.
const (
	BindFailLimit  = 5
	BindFailWindow = 10 * time.Minute
)

// OIDC start and callback throttle per address (T-15).
const (
	OIDCLimit  = 20
	OIDCWindow = time.Minute
)

// AllowOIDC records one OIDC start or callback from addr and reports whether
// it is within the limit. Without Valkey it refuses (fails closed).
func (s *Service) AllowOIDC(ctx context.Context, addr string) bool {
	if s.Throttle == nil {
		return false
	}
	n, err := s.Throttle.Hit(ctx, "oidc:"+addr, OIDCWindow)
	return err == nil && n <= OIDCLimit
}

// BindLogin checks a synced user's password by an LDAP bind (I-30). A user
// or address over the failure limit is refused before any bind; every
// failure counts against both. The answer never says which check failed.
func (s *Service) BindLogin(ctx context.Context, connectorID int64, externalID string, userID int64, password, addr string) error {
	if s.Directory == nil || s.Throttle == nil {
		return ErrUnavailable
	}
	if password == "" {
		return directory.ErrBindRefused
	}
	ukey, akey := "bind:user:"+strconv.FormatInt(userID, 10), "bind:addr:"+addr
	for _, k := range []string{ukey, akey} {
		n, err := s.Throttle.Count(ctx, k)
		if err != nil {
			return ErrUnavailable
		}
		if n >= BindFailLimit {
			s.bindMetric("throttled")
			return ErrThrottled
		}
	}
	err := s.Directory.BindLogin(ctx, connectorID, externalID, password)
	if err == nil {
		s.bindMetric("ok")
		return nil
	}
	for _, k := range []string{ukey, akey} {
		_, _ = s.Throttle.Hit(ctx, k, BindFailWindow)
	}
	if errors.Is(err, directory.ErrBindRefused) {
		s.bindMetric("refused")
		return directory.ErrBindRefused
	}
	s.bindMetric("error")
	s.log().Warn("sso: ldap bind login could not reach the directory", "connector", connectorID, "err", err)
	return err
}

func (s *Service) bindMetric(result string) {
	if s.Metrics != nil {
		s.Metrics.BindLogins.WithLabelValues(result).Inc()
	}
}

// LoginMetric counts one OIDC sign-in outcome.
func (s *Service) LoginMetric(provider, result string) {
	if s.Metrics != nil {
		if !ValidProvider(provider) {
			provider = "unknown"
		}
		s.Metrics.OIDCLogins.WithLabelValues(provider, result).Inc()
	}
}
