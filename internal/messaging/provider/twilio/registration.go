package twilio

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/azrtydxb/hello/internal/messaging/provider"
)

// maxServices bounds how many Messaging Services are read for campaigns.
const maxServices = 20

var _ provider.Registrar = (*Client)(nil)

// getMessaging reads one Messaging API resource into v. An answer of 401 is
// provider.ErrAuth (the account is wrong); any other failure is returned for
// the caller to degrade on.
func (c *Client) getMessaging(ctx context.Context, path string, v any) error {
	status, b, err := c.doAt(ctx, c.messaging, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return refusal(status, b)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return provider.ErrAmbiguous
	}
	return nil
}

// Registration implements provider.Registrar: A2P 10DLC brands from
// BrandRegistrations, campaigns from each Messaging Service's UsAppToPerson,
// the numbers of each service, and toll-free verifications.
func (c *Client) Registration(ctx context.Context, numbers []provider.Number) (provider.Registration, error) {
	reg := provider.Registration{Brands: []provider.Brand{}, Campaigns: []provider.Campaign{}, Numbers: []provider.NumberReg{}, Unavailable: []string{}}
	var firstErr error
	failed := map[string]bool{}
	section := func(name string, err error) bool {
		if err == nil {
			return true
		}
		if !failed[name] {
			reg.Unavailable = append(reg.Unavailable, name)
		}
		failed[name] = true
		if firstErr == nil {
			firstErr = err
		}
		return false
	}

	// Brands.
	var brands struct {
		Data []struct {
			SID      string   `json:"sid"`
			Status   string   `json:"status"`
			Type     string   `json:"brand_type"`
			Failures []string `json:"failure_reason"`
			Name     string   `json:"friendly_name"`
		} `json:"data"`
	}
	brandsOK := section("brands", c.getMessaging(ctx, "/a2p/BrandRegistrations?PageSize=50", &brands))
	if brandsOK {
		for _, b := range brands.Data {
			if strings.EqualFold(b.Status, "DELETED") {
				continue
			}
			name := provider.Clean(b.Name, 80)
			if name == "" {
				name = provider.Clean(b.SID, 40)
			}
			reg.Brands = append(reg.Brands, provider.Brand{ID: provider.Clean(b.SID, 40), Name: name,
				Type: provider.Clean(strings.ToLower(b.Type), 32), State: brandState(b.Status),
				Detail: provider.Clean(strings.Join(b.Failures, "; "), 200)})
		}
	}

	// Messaging Services, their campaigns and their numbers.
	var services struct {
		Services []struct {
			SID string `json:"sid"`
		} `json:"services"`
	}
	campaignsOK := section("campaigns", c.getMessaging(ctx, "/Services?PageSize=100", &services))
	inService := map[string]string{} // E.164 -> service SID
	serviceState := map[string]string{}
	if campaignsOK {
		sids := make([]string, 0, len(services.Services))
		for _, s := range services.Services {
			if ValidServiceSID(s.SID) {
				sids = append(sids, s.SID)
			}
		}
		sort.Strings(sids)
		if len(sids) > maxServices {
			sids = sids[:maxServices]
		}
		for _, sid := range sids {
			var cs struct {
				Compliance []struct {
					SID     string `json:"sid"`
					Brand   string `json:"brand_registration_sid"`
					UseCase string `json:"us_app_to_person_usecase"`
					Status  string `json:"campaign_status"`
					Errors  []struct {
						Description string `json:"description"`
					} `json:"errors"`
				} `json:"compliance"`
			}
			if err := c.getMessaging(ctx, "/Services/"+sid+"/UsAppToPerson?PageSize=50", &cs); err != nil {
				section("campaigns", err)
				continue
			}
			best := ""
			for _, k := range cs.Compliance {
				st := campaignState(k.Status)
				detail := ""
				if len(k.Errors) > 0 {
					detail = provider.Clean(k.Errors[0].Description, 200)
				}
				reg.Campaigns = append(reg.Campaigns, provider.Campaign{ID: provider.Clean(k.SID, 40),
					BrandID: provider.Clean(k.Brand, 40), UseCase: provider.Clean(strings.ToLower(k.UseCase), 40), State: st, Detail: detail})
				best = betterState(best, st)
			}
			serviceState[sid] = best
			var pn struct {
				PhoneNumbers []struct {
					PhoneNumber string `json:"phone_number"`
				} `json:"phone_numbers"`
			}
			if err := c.getMessaging(ctx, "/Services/"+sid+"/PhoneNumbers?PageSize=1000", &pn); err != nil {
				section("campaigns", err)
				continue
			}
			for _, p := range pn.PhoneNumbers {
				// A number in two services counts for the better one.
				if prev, ok := inService[p.PhoneNumber]; !ok || betterState(serviceState[prev], serviceState[sid]) == serviceState[sid] {
					inService[p.PhoneNumber] = sid
				}
			}
		}
	}

	// Toll-free verifications, keyed by the number's SID.
	var tf struct {
		Verifications []struct {
			NumberSID string `json:"tollfree_phone_number_sid"`
			Status    string `json:"status"`
			Reason    string `json:"rejection_reason"`
		} `json:"verifications"`
	}
	tollOK := section("tollfree", c.getMessaging(ctx, "/Tollfree/Verifications?PageSize=100", &tf))
	tollState := map[string]provider.NumberReg{}
	if tollOK {
		for _, v := range tf.Verifications {
			st := tollFreeState(v.Status)
			prev, ok := tollState[v.NumberSID]
			if !ok || betterState(prev.State, st) == st {
				tollState[v.NumberSID] = provider.NumberReg{State: st, Detail: provider.Clean(v.Reason, 200)}
			}
		}
	}

	if len(failed) == 3 {
		if firstErr == nil {
			firstErr = provider.ErrAmbiguous
		}
		return provider.Registration{}, firstErr
	}
	campaignsOK = !failed["campaigns"]
	for _, n := range numbers {
		nr := provider.NumberReg{Number: n.E164, Kind: provider.KindOf(n.E164), State: provider.RegUnknown}
		switch nr.Kind {
		case provider.RegKindTollFree:
			if tollOK {
				if v, ok := tollState[n.ID]; ok {
					nr.State, nr.Detail = v.State, v.Detail
				} else {
					nr.State, nr.Detail = provider.RegUnregistered, "no toll-free verification was submitted for this number"
				}
			}
		case provider.RegKind10DLC:
			if campaignsOK {
				if sid, ok := inService[n.E164]; ok && serviceState[sid] != "" {
					nr.State = serviceState[sid]
				} else {
					nr.State, nr.Detail = provider.RegUnregistered, "the number is not in a Messaging Service with a registered campaign"
				}
			}
		default:
			nr.Detail = "no registration is read for numbers outside the US and Canada"
		}
		reg.Numbers = append(reg.Numbers, nr)
	}
	return reg, nil
}

// rank orders states from worst to best for choosing between two records.
func rank(s string) int {
	switch s {
	case provider.RegApproved:
		return 4
	case provider.RegPending:
		return 3
	case provider.RegRejected:
		return 2
	case provider.RegUnknown:
		return 1
	}
	return 0
}

// betterState returns the more advanced of two states.
func betterState(a, b string) string {
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// brandState maps a BrandRegistration status.
func brandState(s string) string {
	switch strings.ToUpper(s) {
	case "APPROVED":
		return provider.RegApproved
	case "PENDING", "IN_REVIEW", "UNVERIFIED":
		return provider.RegPending
	case "FAILED":
		return provider.RegRejected
	}
	return provider.RegUnknown
}

// campaignState maps a UsAppToPerson campaign status.
func campaignState(s string) string {
	switch strings.ToUpper(s) {
	case "VERIFIED":
		return provider.RegApproved
	case "IN_PROGRESS", "PENDING":
		return provider.RegPending
	case "FAILED":
		return provider.RegRejected
	}
	return provider.RegUnknown
}

// tollFreeState maps a toll-free verification status.
func tollFreeState(s string) string {
	u := strings.ToUpper(s)
	switch {
	case u == "TWILIO_APPROVED":
		return provider.RegApproved
	case u == "PENDING_REVIEW" || u == "IN_REVIEW" || u == "EDIT_REQUESTED":
		return provider.RegPending
	case strings.Contains(u, "REJECT"):
		return provider.RegRejected
	}
	return provider.RegUnknown
}
