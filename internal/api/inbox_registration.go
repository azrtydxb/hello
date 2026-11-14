package api

// Sender registration (spec messaging-channels M-1, "Registration", and the
// channels page's compliance checklist): what the carrier says about the
// customer's A2P 10DLC brand and campaign and toll-free verification, per
// number, plus a checklist of what remains. Hello only reads the state; the
// customer registers in the carrier's own console.

import (
	"context"
	"net/http"
	"time"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/messaging/provider"
	"github.com/azrtydxb/hello/internal/store"
)

// Checklist item states.
const (
	checkDone          = "done"
	checkPending       = "pending"
	checkTodo          = "todo"
	checkAttention     = "attention"
	checkUnknown       = "unknown"
	checkManual        = "manual"
	checkNotApplicable = "not_applicable"
)

// registrationNumber is one carrier number and where its registration stands.
type registrationNumber struct {
	Number string `json:"number"`
	Kind   string `json:"kind"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	// Added is whether the number is a number of the channel in Hello.
	Added bool `json:"added"`
}

// checklistItem is one step of getting a sender ready for US traffic.
type checklistItem struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	Link   string `json:"link"`
}

// registrationReport is the answer of the registration route.
type registrationReport struct {
	Provider    string               `json:"provider"`
	Supported   bool                 `json:"supported"`
	ReadAt      time.Time            `json:"readAt"`
	Brands      []provider.Brand     `json:"brands"`
	Campaigns   []provider.Campaign  `json:"campaigns"`
	Numbers     []registrationNumber `json:"numbers"`
	Unavailable []string             `json:"unavailable"`
	Checklist   []checklistItem      `json:"checklist"`
}

// registrationLinks are the carriers' own documentation of the steps; Hello
// links to them instead of paraphrasing what changes often.
func registrationLinks(prov string) (tenDLC, tollFree string) {
	switch prov {
	case messaging.ProviderTelnyx:
		return "https://developers.telnyx.com/docs/messaging/10dlc/quickstart", "https://developers.telnyx.com/docs/messaging"
	default:
		return "https://www.twilio.com/docs/messaging/compliance/a2p-10dlc", "https://www.twilio.com/docs/messaging/compliance/toll-free"
	}
}

// worstState folds the states of several records into one checklist state.
func worstState(states []string) string {
	rank := map[string]int{provider.RegApproved: 0, provider.RegUnknown: 1, provider.RegPending: 2, provider.RegUnregistered: 3, provider.RegRejected: 4}
	worst, out := -1, ""
	for _, s := range states {
		if r, ok := rank[s]; ok && r > worst {
			worst, out = r, s
		}
	}
	return out
}

// buildChecklist derives the checklist from what the carrier reported and
// from the channel's own numbers. Items that need a human to judge the
// content (sample messages) are manual: Hello cannot read them.
func buildChecklist(prov string, supported bool, reg provider.Registration, addrs []store.MsgAddress) []checklistItem {
	tenLink, tollLink := registrationLinks(prov)
	var tenStates, tollStates []string
	has10, hasToll := false, false
	for _, n := range reg.Numbers {
		switch n.Kind {
		case provider.RegKind10DLC:
			has10 = true
			tenStates = append(tenStates, n.State)
		case provider.RegKindTollFree:
			hasToll = true
			tollStates = append(tollStates, n.State)
		}
	}
	unavailable := func(section string) bool {
		for _, u := range reg.Unavailable {
			if u == section {
				return true
			}
		}
		return false
	}
	item := func(id, title, state, detail, link string) checklistItem {
		return checklistItem{ID: id, Title: title, State: state, Detail: detail, Link: link}
	}
	out := []checklistItem{}
	na := "The account has no US 10DLC numbers, so nothing is needed."
	// Brand.
	switch {
	case !supported:
		out = append(out, item("brand", "Register the brand (A2P 10DLC)", checkUnknown, "This carrier does not report registrations; check its console.", tenLink))
	case !has10:
		out = append(out, item("brand", "Register the brand (A2P 10DLC)", checkNotApplicable, na, tenLink))
	case unavailable("brands"):
		out = append(out, item("brand", "Register the brand (A2P 10DLC)", checkUnknown, "The carrier would not list brands for this key; check its console.", tenLink))
	default:
		states := []string{}
		for _, b := range reg.Brands {
			states = append(states, b.State)
		}
		out = append(out, item("brand", "Register the brand (A2P 10DLC)", brandItemState(states), brandDetail(states), tenLink))
	}
	// Campaign.
	switch {
	case !supported:
		out = append(out, item("campaign", "Register a campaign for the brand", checkUnknown, "This carrier does not report registrations; check its console.", tenLink))
	case !has10:
		out = append(out, item("campaign", "Register a campaign for the brand", checkNotApplicable, na, tenLink))
	case unavailable("campaigns"):
		out = append(out, item("campaign", "Register a campaign for the brand", checkUnknown, "The carrier would not list campaigns for this key; check its console.", tenLink))
	default:
		states := []string{}
		for _, c := range reg.Campaigns {
			states = append(states, c.State)
		}
		out = append(out, item("campaign", "Register a campaign for the brand", brandItemState(states), campaignDetail(states), tenLink))
	}
	// Numbers on a campaign.
	switch {
	case !supported:
		out = append(out, item("numbers", "Attach every US number to a campaign", checkUnknown, "This carrier does not report registrations; check its console.", tenLink))
	case !has10:
		out = append(out, item("numbers", "Attach every US number to a campaign", checkNotApplicable, na, tenLink))
	case unavailable("campaigns"):
		out = append(out, item("numbers", "Attach every US number to a campaign", checkUnknown, "The carrier would not say which numbers are on a campaign.", tenLink))
	default:
		out = append(out, item("numbers", "Attach every US number to a campaign", numbersState(worstState(tenStates)), numbersDetail(worstState(tenStates)), tenLink))
	}
	// Toll-free.
	switch {
	case !supported:
		out = append(out, item("tollfree", "Verify toll-free numbers", checkUnknown, "This carrier does not report registrations; check its console.", tollLink))
	case !hasToll:
		out = append(out, item("tollfree", "Verify toll-free numbers", checkNotApplicable, "The account has no toll-free numbers, so nothing is needed.", tollLink))
	case unavailable("tollfree"):
		out = append(out, item("tollfree", "Verify toll-free numbers", checkUnknown, "The carrier would not list verifications for this key; check its console.", tollLink))
	default:
		out = append(out, item("tollfree", "Verify toll-free numbers", numbersState(worstState(tollStates)), tollDetail(worstState(tollStates)), tollLink))
	}
	// Content the carrier reviews, which Hello cannot read.
	out = append(out, item("samples", "Sample messages that carry opt-out language", checkManual,
		"Carriers reject a campaign whose sample messages lack \"Reply STOP to opt out\". Check them in the carrier's console.", tenLink))
	// STOP and HELP text: Hello's own settings.
	enabled, withHelp := 0, 0
	for _, a := range addrs {
		if !a.Enabled {
			continue
		}
		enabled++
		if a.HelpText != "" {
			withHelp++
		}
	}
	switch {
	case enabled == 0:
		out = append(out, item("stophelp", "Answer HELP, and confirm STOP", checkTodo, "Add a number to the channel, then set its answer to HELP.", tenLink))
	case withHelp == enabled:
		out = append(out, item("stophelp", "Answer HELP, and confirm STOP", checkDone,
			"Every enabled number answers HELP. Hello records STOP and confirms it itself.", tenLink))
	default:
		out = append(out, item("stophelp", "Answer HELP, and confirm STOP", checkTodo,
			"Some enabled numbers have no answer to HELP; carriers expect one. Set it on the number.", tenLink))
	}
	return out
}

// brandItemState turns the states of brands or campaigns into the state of
// the step: one approved record is enough.
func brandItemState(states []string) string {
	if len(states) == 0 {
		return checkTodo
	}
	best := checkAttention
	for _, s := range states {
		switch s {
		case provider.RegApproved:
			return checkDone
		case provider.RegPending:
			best = checkPending
		case provider.RegUnknown:
			if best == checkAttention {
				best = checkUnknown
			}
		}
	}
	return best
}

func brandDetail(states []string) string {
	switch brandItemState(states) {
	case checkDone:
		return "A brand is approved."
	case checkPending:
		return "A brand is waiting for the carrier's review."
	case checkTodo:
		return "No brand is registered. Register it in the carrier's console."
	case checkUnknown:
		return "The carrier reports a state Hello does not know."
	}
	return "The brand was rejected or failed. Fix it in the carrier's console."
}

func campaignDetail(states []string) string {
	switch brandItemState(states) {
	case checkDone:
		return "A campaign is approved."
	case checkPending:
		return "A campaign is waiting for the carrier's review."
	case checkTodo:
		return "No campaign is registered. Register it in the carrier's console."
	case checkUnknown:
		return "The carrier reports a state Hello does not know."
	}
	return "The campaign was rejected or failed. Fix it in the carrier's console."
}

// numbersState turns the worst state among numbers into a step state.
func numbersState(worst string) string {
	switch worst {
	case provider.RegApproved:
		return checkDone
	case provider.RegPending:
		return checkPending
	case provider.RegUnregistered:
		return checkTodo
	case provider.RegRejected:
		return checkAttention
	}
	return checkUnknown
}

func numbersDetail(worst string) string {
	switch worst {
	case provider.RegApproved:
		return "Every US number is on an approved campaign."
	case provider.RegPending:
		return "A number is waiting to be attached or for the campaign's review."
	case provider.RegUnregistered:
		return "A US number is not on a campaign; carriers filter or surcharge its texts."
	case provider.RegRejected:
		return "A number's campaign was rejected or its attachment failed."
	}
	return "The carrier reports no state for the numbers."
}

func tollDetail(worst string) string {
	switch worst {
	case provider.RegApproved:
		return "Every toll-free number is verified."
	case provider.RegPending:
		return "A toll-free verification is in review."
	case provider.RegUnregistered:
		return "A toll-free number has no verification; carriers block unverified toll-free texts."
	case provider.RegRejected:
		return "A toll-free verification was rejected. Fix and resubmit it in the carrier's console."
	}
	return "The carrier reports no state for the toll-free numbers."
}

// getMessagingChannelRegistration reads the sender registration of a carrier
// channel's account. Nothing is sent and nothing is stored.
func (s *server) getMessagingChannelRegistration(w http.ResponseWriter, r *http.Request) {
	is, ok := s.inboxStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := is.GetMsgChannel(r.Context(), id)
	if err != nil {
		s.storeError(w, "messaging channel", err)
		return
	}
	if c.Provider == messaging.ProviderSIPTrunk {
		writeError(w, http.StatusConflict, "not_a_carrier_channel", "a SIP trunk channel has no carrier account to read a registration from")
		return
	}
	p, ok := s.channelAdapter(r.Context(), w, is, c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	addrs, err := is.ListMsgAddresses(ctx, id)
	if err != nil {
		s.internal(w, "list messaging addresses", err)
		return
	}
	rep := registrationReport{Provider: c.Provider, ReadAt: time.Now().UTC(), Brands: []provider.Brand{},
		Campaigns: []provider.Campaign{}, Numbers: []registrationNumber{}, Unavailable: []string{}}
	reg := provider.Registration{Brands: rep.Brands, Campaigns: rep.Campaigns, Unavailable: rep.Unavailable}
	if rg, ok := p.(provider.Registrar); ok {
		rep.Supported = true
		numbers, err := p.Numbers(ctx)
		if err != nil {
			s.carrierError(w, err)
			return
		}
		if reg, err = rg.Registration(ctx, numbers); err != nil {
			s.carrierError(w, err)
			return
		}
	}
	added := map[string]bool{}
	for _, a := range addrs {
		added[a.Address] = true
	}
	rep.Brands, rep.Campaigns, rep.Unavailable = nonNil(reg.Brands), nonNil(reg.Campaigns), nonNil(reg.Unavailable)
	for _, n := range reg.Numbers {
		rep.Numbers = append(rep.Numbers, registrationNumber{Number: n.Number, Kind: n.Kind, State: n.State, Detail: n.Detail, Added: added[n.Number]})
	}
	rep.Checklist = buildChecklist(c.Provider, rep.Supported, reg, addrs)
	writeJSON(w, http.StatusOK, rep)
}

// channelAdapter builds the adapter of a carrier channel from its stored
// credentials, answering the failure itself.
func (s *server) channelAdapter(ctx context.Context, w http.ResponseWriter, is InboxStore, c store.MsgChannel) (provider.SMSProvider, bool) {
	cfg, err := is.ChannelProviderConfig(ctx, c.ID)
	if err != nil {
		writeError(w, http.StatusConflict, "credentials_unreadable", "the stored credentials cannot be read; enter them again")
		return nil, false
	}
	p, err := provider.New(c.Provider, cfg)
	if err != nil {
		writeError(w, http.StatusConflict, "credentials_unreadable", "the stored credentials are not usable; enter them again")
		return nil, false
	}
	return p, true
}
