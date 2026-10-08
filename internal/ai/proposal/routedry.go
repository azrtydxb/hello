package proposal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/store"
)

// dryRun compiles the outbound and inbound tables as they would be after
// the route actions and rejects a table with an error the current one does
// not have (spec S-12). Proposals without route actions skip it.
func (v *SchemaValidator) dryRun(ctx context.Context, actions []Action) error {
	if !slices.ContainsFunc(actions, func(a Action) bool { return isRouteOp(a.OperationID) }) {
		return nil
	}
	if v.routes == nil {
		return errors.New("route dry run: no routing configuration")
	}
	snap, err := v.routes.RoutingConfig(ctx)
	if err != nil {
		return fmt.Errorf("route dry run: %w", err)
	}
	_, base := routing.Compile(snap.Config)
	cfg := snap.Config
	cfg.Outbound = slices.Clone(cfg.Outbound)
	cfg.Inbound = slices.Clone(cfg.Inbound)
	for i, a := range actions {
		if !isRouteOp(a.OperationID) {
			continue
		}
		if err := applyRoute(&cfg, a, 1<<40+int64(i)); err != nil {
			return fmt.Errorf("action %d (%s): route dry run: %w", i, a.OperationID, err)
		}
	}
	_, after := routing.Compile(cfg)
	known := map[string]bool{}
	for _, e := range base {
		known[e.Message] = true
	}
	for _, e := range after {
		if !known[e.Message] {
			return fmt.Errorf("the routing table would not compile: %s: %s", e.Path, e.Message)
		}
	}
	return nil
}

func isRouteOp(id string) bool {
	switch id {
	case "createOutboundRoute", "updateOutboundRoute", "deleteOutboundRoute",
		"createInboundRoute", "updateInboundRoute", "deleteInboundRoute":
		return true
	}
	return false
}

func applyRoute(cfg *routing.Config, a Action, tmpID int64) error {
	id := tmpID
	if s, ok := a.PathParams["id"]; ok {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return errors.New("the id is not a number")
		}
		id = n
	}
	switch a.OperationID {
	case "createOutboundRoute", "updateOutboundRoute":
		var o store.OutboundRoute
		if err := json.Unmarshal(a.After, &o); err != nil {
			return err
		}
		r := routing.OutboundRoute{
			ID: id, Position: o.Position, Name: o.Name, MatchKind: o.MatchKind, Match: o.Match,
			SourceExtensions: o.SourceExtensions, Schedule: o.Schedule, Number: o.NumberTransform,
			CallerID: o.CallerIDTransform, Trunks: o.Trunks, FailoverCodes: o.FailoverCodes,
			Emergency: o.Emergency, Enabled: o.Enabled,
		}
		if a.OperationID == "createOutboundRoute" {
			if r.Position == 0 {
				r.Position = len(cfg.Outbound) + 1
			}
			cfg.Outbound = append(cfg.Outbound, r)
			return nil
		}
		return replaceOutbound(cfg, id, &r)
	case "deleteOutboundRoute":
		return replaceOutbound(cfg, id, nil)
	case "createInboundRoute", "updateInboundRoute":
		var in store.InboundRoute
		if err := json.Unmarshal(a.After, &in); err != nil {
			return err
		}
		r := routing.InboundRoute{
			ID: id, Position: in.Position, Name: in.Name, DIDKind: in.DIDKind, DID: in.DID,
			SIPDomain: in.SIPDomain, HeaderName: in.HeaderName, HeaderRegex: in.HeaderRegex, Schedule: in.Schedule,
			CallerID: in.CallerIDTransform, DestinationKind: in.DestinationKind, Destination: in.Destination,
			Enabled: in.Enabled,
		}
		if in.TrunkID != nil {
			r.TrunkID = *in.TrunkID
		}
		if a.OperationID == "createInboundRoute" {
			if r.Position == 0 {
				r.Position = len(cfg.Inbound) + 1
			}
			cfg.Inbound = append(cfg.Inbound, r)
			return nil
		}
		return replaceInbound(cfg, id, &r)
	default: // deleteInboundRoute
		return replaceInbound(cfg, id, nil)
	}
}

// replaceOutbound swaps route id for r, or removes it when r is nil.
func replaceOutbound(cfg *routing.Config, id int64, r *routing.OutboundRoute) error {
	i := slices.IndexFunc(cfg.Outbound, func(o routing.OutboundRoute) bool { return o.ID == id })
	if i < 0 {
		return errors.New("the outbound route does not exist")
	}
	if r == nil {
		cfg.Outbound = slices.Delete(cfg.Outbound, i, i+1)
		return nil
	}
	cfg.Outbound[i] = *r
	return nil
}

func replaceInbound(cfg *routing.Config, id int64, r *routing.InboundRoute) error {
	i := slices.IndexFunc(cfg.Inbound, func(o routing.InboundRoute) bool { return o.ID == id })
	if i < 0 {
		return errors.New("the inbound route does not exist")
	}
	if r == nil {
		cfg.Inbound = slices.Delete(cfg.Inbound, i, i+1)
		return nil
	}
	cfg.Inbound[i] = *r
	return nil
}
