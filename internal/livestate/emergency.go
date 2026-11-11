package livestate

import (
	"context"
	"fmt"
	"time"
)

// Emergency marks (spec phone-feature-parity S-28 f): an extension that
// placed an emergency call is marked for the callback window, so a call back
// to it on any node within the window is told apart.
const emergencyPrefix = "hello:emergency:"

// MarkEmergency marks extension for ttl; a repeated call renews the mark.
func (s *Store) MarkEmergency(ctx context.Context, extension string, ttl time.Duration) error {
	err := s.c.Do(ctx, s.c.B().Set().Key(emergencyPrefix+extension).Value("1").Px(ttl).Build()).Error()
	if err != nil {
		return fmt.Errorf("livestate: mark emergency: %w", err)
	}
	return nil
}

// EmergencyRecent reports whether extension is marked.
func (s *Store) EmergencyRecent(ctx context.Context, extension string) (bool, error) {
	n, err := s.c.Do(ctx, s.c.B().Exists().Key(emergencyPrefix+extension).Build()).AsInt64()
	if err != nil {
		return false, fmt.Errorf("livestate: emergency mark: %w", err)
	}
	return n > 0, nil
}
