package sip

import (
	"context"
	"strings"
	"time"
)

// CallerNamer finds the name of a caller by number in a CRM (internal/crm).
// Name waits at most budget for the CRM; a cached answer returns at once.
type CallerNamer interface {
	Name(ctx context.Context, number string, budget time.Duration) (string, bool)
}

// ExtensionCallerNamer is implemented by a CallerNamer that also searches the
// contacts of the user who owns the called extension (the Outlook and Google
// connectors, spec integrations P18).
type ExtensionCallerNamer interface {
	NameFor(ctx context.Context, ext, number string, budget time.Duration) (string, bool)
}

// DefaultCallerNameBudget is how long an inbound call may wait for a CRM
// caller name on a cold cache. The CRM query itself continues in the
// background up to its own timeout and fills the cache, so the next call from
// the number is answered at once.
const DefaultCallerNameBudget = 250 * time.Millisecond

// crmCallerName returns the display name for an inbound call: the CRM's name
// for the caller's number when the carrier sent no name (or only the number),
// otherwise what the carrier sent. It never waits longer than the budget.
func (s *Server) crmCallerName(num, carrierName, calleeExt string) string {
	if s.deps.CallerNames == nil || num == "" {
		return carrierName
	}
	if carrierName != "" && !numberLike(carrierName) {
		return carrierName
	}
	budget := s.deps.CallerNameBudget
	if budget <= 0 {
		budget = DefaultCallerNameBudget
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget+50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if name, ok := s.deps.CallerNames.Name(ctx, num, budget); ok {
		return name
	}
	// The callee's own contacts come after the organisation's CRM, within
	// what is left of the same budget.
	if en, ok := s.deps.CallerNames.(ExtensionCallerNamer); ok && calleeExt != "" {
		if left := budget - time.Since(start); left > 0 {
			if name, ok := en.NameFor(ctx, calleeExt, num, left); ok {
				return name
			}
		}
	}
	return carrierName
}

// numberLike reports whether a display name is just a phone number.
func numberLike(s string) bool {
	for _, r := range strings.TrimSpace(s) {
		if (r < '0' || r > '9') && !strings.ContainsRune("+ -.()", r) {
			return false
		}
	}
	return true
}
