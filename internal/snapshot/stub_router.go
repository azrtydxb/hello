package snapshot

import (
	"fmt"
	"net/netip"
	"slices"

	"github.com/azrtydxb/hello/internal/routing"
)

// stubRouter routes calls between internal extensions only; every other
// call is rejected with 404. It is the router of a snapshot built by New
// (tests), and of a configuration whose very first revision does not
// compile (there is no last good table to keep yet).
type stubRouter struct {
	snap *Snapshot
	cfg  routing.Config
}

func (r stubRouter) isExtension(n string) bool {
	if r.snap != nil {
		return r.snap.HasExtension(n)
	}
	_, ok := r.cfg.Extensions[n]
	return ok
}

// Decide finds an internal extension or rejects with 404.
func (r stubRouter) Decide(c routing.Call, _ routing.TrunkUsability) routing.Decision {
	var d routing.Decision
	if c.FromExtension != "" {
		if r.isExtension(c.Number) {
			d.Trace.Add(fmt.Sprintf("Internal extension lookup %q -> extension %s", c.Number, c.Number))
			d.Kind, d.Extension = routing.KindInternal, c.Number
			return d
		}
		d.Trace.Add(fmt.Sprintf("Internal extension lookup %q -> no match", c.Number))
	}
	d.Kind, d.RejectCode, d.Reason = routing.KindReject, 404, "no route matched"
	d.Trace.Add("No route matched; 404 Not Found")
	return d
}

// Trunk returns a configured trunk.
func (r stubRouter) Trunk(id int64) (*routing.Trunk, bool) {
	for i := range r.cfg.Trunks {
		if r.cfg.Trunks[i].ID == id {
			return &r.cfg.Trunks[i], true
		}
	}
	return nil, false
}

// TrunkForSource is the contract's source validation: the lowest-ID
// enabled trunk whose source CIDRs or resolved destination IPs contain ip.
func (r stubRouter) TrunkForSource(ip netip.Addr, _ string) (*routing.Trunk, bool) {
	ip = ip.Unmap()
	for i := range r.cfg.Trunks { // loaded ORDER BY id
		t := &r.cfg.Trunks[i]
		if !t.Enabled {
			continue
		}
		for _, p := range t.SourceCIDRs {
			if p.Contains(ip) {
				return t, true
			}
		}
		if slices.Contains(r.cfg.ResolvedIPs[t.ID], ip) {
			return t, true
		}
	}
	return nil, false
}
