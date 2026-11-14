package api

import (
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/messaging/provider"
	"github.com/azrtydxb/hello/internal/store"
)

func checklistStates(items []checklistItem) map[string]string {
	m := map[string]string{}
	for _, it := range items {
		m[it.ID] = it.State
	}
	return m
}

// TestBuildChecklist fails if the checklist calls a step done that the
// carrier did not approve, hides a rejected or pending one, treats a section
// the carrier would not answer as done or as missing, asks for a step that
// does not apply to the account's numbers, or lets a missing HELP answer
// pass.
func TestBuildChecklist(t *testing.T) {
	ten := func(state string) provider.NumberReg {
		return provider.NumberReg{Number: "+12125550100", Kind: provider.RegKind10DLC, State: state}
	}
	toll := func(state string) provider.NumberReg {
		return provider.NumberReg{Number: "+18005550100", Kind: provider.RegKindTollFree, State: state}
	}
	addr := func(enabled bool, help string) store.MsgAddress {
		return store.MsgAddress{Enabled: enabled, HelpText: help}
	}
	approved := provider.Registration{
		Brands:    []provider.Brand{{State: provider.RegApproved}},
		Campaigns: []provider.Campaign{{State: provider.RegApproved}},
		Numbers:   []provider.NumberReg{ten(provider.RegApproved), toll(provider.RegApproved)},
	}
	cases := []struct {
		name      string
		supported bool
		reg       provider.Registration
		addrs     []store.MsgAddress
		want      map[string]string
	}{
		{"everything approved", true, approved, []store.MsgAddress{addr(true, "help")},
			map[string]string{"brand": "done", "campaign": "done", "numbers": "done", "tollfree": "done", "samples": "manual", "stophelp": "done"}},
		{"nothing registered", true, provider.Registration{Numbers: []provider.NumberReg{ten(provider.RegUnregistered), toll(provider.RegUnregistered)}}, nil,
			map[string]string{"brand": "todo", "campaign": "todo", "numbers": "todo", "tollfree": "todo", "stophelp": "todo"}},
		{"pending and rejected", true, provider.Registration{
			Brands:    []provider.Brand{{State: provider.RegPending}},
			Campaigns: []provider.Campaign{{State: provider.RegRejected}},
			Numbers:   []provider.NumberReg{ten(provider.RegRejected), toll(provider.RegPending)}}, []store.MsgAddress{addr(true, "")},
			map[string]string{"brand": "pending", "campaign": "attention", "numbers": "attention", "tollfree": "pending", "stophelp": "todo"}},
		{"a second approved campaign wins over a rejected one", true, provider.Registration{
			Brands:    []provider.Brand{{State: provider.RegApproved}},
			Campaigns: []provider.Campaign{{State: provider.RegRejected}, {State: provider.RegApproved}},
			Numbers:   []provider.NumberReg{ten(provider.RegApproved)}}, nil,
			map[string]string{"campaign": "done", "tollfree": "not_applicable"}},
		{"outside the US nothing applies", true, provider.Registration{Numbers: []provider.NumberReg{{Number: "+3270123456", Kind: provider.RegKindOther, State: provider.RegUnknown}}}, nil,
			map[string]string{"brand": "not_applicable", "campaign": "not_applicable", "numbers": "not_applicable", "tollfree": "not_applicable"}},
		{"sections the carrier would not answer", true, provider.Registration{
			Unavailable: []string{"brands", "campaigns", "tollfree"},
			Numbers:     []provider.NumberReg{ten(provider.RegUnknown), toll(provider.RegUnknown)}}, nil,
			map[string]string{"brand": "unknown", "campaign": "unknown", "numbers": "unknown", "tollfree": "unknown", "samples": "manual"}},
		{"a carrier that reports nothing", false, provider.Registration{}, nil,
			map[string]string{"brand": "unknown", "campaign": "unknown", "numbers": "unknown", "tollfree": "unknown"}},
		{"a disabled number needs no HELP answer", true, approved, []store.MsgAddress{addr(true, "help"), addr(false, "")},
			map[string]string{"stophelp": "done"}},
		{"one number without a HELP answer", true, approved, []store.MsgAddress{addr(true, "help"), addr(true, "")},
			map[string]string{"stophelp": "todo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checklistStates(buildChecklist(messaging.ProviderTwilio, tc.supported, tc.reg, tc.addrs))
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
				}
			}
		})
	}
	for _, prov := range []string{messaging.ProviderTwilio, messaging.ProviderTelnyx} {
		for _, it := range buildChecklist(prov, true, approved, nil) {
			if !strings.HasPrefix(it.Link, "https://") || it.Title == "" || it.Detail == "" {
				t.Errorf("%s item %+v is incomplete", prov, it)
			}
		}
	}
	if a, _ := registrationLinks(messaging.ProviderTelnyx); !strings.Contains(a, "telnyx") {
		t.Errorf("Telnyx links to %s", a)
	}
}
