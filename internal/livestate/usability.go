package livestate

import (
	"strconv"

	"github.com/azrtydxb/hello/internal/routing"
)

// DestinationKey is how a destination is named in DestinationHealth:
// host:port exactly as configured (port 0 stays 0).
func DestinationKey(d routing.Destination) string { return d.Host + ":" + strconv.Itoa(d.Port) }

// Usability is Hello's rule for whether trunk t can take another call given
// its shared status s. hello-sip's call path and hello-control's route
// tester both use it, so a test and a real call trace the same reasons:
//
//   - "disabled": t is not enabled.
//   - "misconfigured": the lease holder published the registration state
//     misconfigured (its password does not open).
//   - "unhealthy": every configured destination's last OPTIONS result is
//     down. A destination with no result yet counts as up; registration
//     state does not count (spec S-3: health is OPTIONS).
//   - "full": not an emergency, t has a limit and the cluster's active call
//     count has reached it. Emergency calls ignore the limit and use the
//     last known health.
//
// Pass the zero TrunkStatus when nothing is known about t. Two things are
// the caller's: an unreachable trunk state (hello-sip refuses non-emergency
// calls with "state unavailable"; the tester assumes usable and says so),
// and the call slot itself, which hello-sip takes per attempt
// (AcquireTrunkCall). The tester cannot predict that, so a trunk it shows
// usable may still be found full when a real call tries it.
func (s TrunkStatus) Usability(t routing.Trunk, emergency bool) (ok bool, reason string) {
	switch {
	case !t.Enabled:
		return false, "disabled"
	case s.Registration != nil && s.Registration.State == "misconfigured":
		return false, "misconfigured"
	}
	if len(t.Destinations) > 0 {
		down := 0
		for _, d := range t.Destinations {
			if h, known := s.health(DestinationKey(d)); known && !h.Up {
				down++
			}
		}
		if down == len(t.Destinations) {
			return false, "unhealthy"
		}
	}
	if !emergency && t.MaxCalls > 0 && s.ActiveCalls >= t.MaxCalls {
		return false, "full"
	}
	return true, ""
}

func (s TrunkStatus) health(dest string) (DestinationHealth, bool) {
	for _, h := range s.Destinations {
		if h.Destination == dest {
			return h, true
		}
	}
	return DestinationHealth{}, false
}
