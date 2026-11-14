package telnyx

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/azrtydxb/hello/internal/messaging/provider"
)

var _ provider.Registrar = (*Client)(nil)

// get reads one resource into v; failures are the adapter's errors.
func (c *Client) get(ctx context.Context, path string, v any) error {
	status, b, err := c.do(ctx, http.MethodGet, path, nil)
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

// Registration implements provider.Registrar from Telnyx's 10DLC brand,
// campaign and phone-number-campaign records and its toll-free verification
// requests. The field names are those of Telnyx's published API
// [unverified against a live account].
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

	var brands struct {
		Records []struct {
			ID       string `json:"brandId"`
			Display  string `json:"displayName"`
			Company  string `json:"companyName"`
			Entity   string `json:"entityType"`
			Status   string `json:"status"`
			Identity string `json:"identityStatus"`
		} `json:"records"`
	}
	if section("brands", c.get(ctx, "/10dlc/brand?recordsPerPage=500", &brands)) {
		for _, b := range brands.Records {
			name := b.Display
			if name == "" {
				name = b.Company
			}
			reg.Brands = append(reg.Brands, provider.Brand{ID: provider.Clean(b.ID, 40), Name: provider.Clean(name, 80),
				Type: provider.Clean(strings.ToLower(b.Entity), 32), State: brandState(b.Status, b.Identity),
				Detail: provider.Clean(b.Identity, 60)})
		}
	}

	var campaigns struct {
		Records []struct {
			ID         string `json:"campaignId"`
			BrandID    string `json:"brandId"`
			UseCase    string `json:"usecase"`
			Status     string `json:"status"`
			Submission string `json:"submissionStatus"`
			Failures   any    `json:"failureReasons"`
		} `json:"records"`
	}
	campaignState := map[string]string{}
	campaignsOK := section("campaigns", c.get(ctx, "/10dlc/campaign?recordsPerPage=500", &campaigns))
	if campaignsOK {
		for _, k := range campaigns.Records {
			st := campaignStatus(k.Status, k.Submission)
			detail := ""
			if s, ok := k.Failures.(string); ok {
				detail = provider.Clean(s, 200)
			}
			campaignState[k.ID] = st
			reg.Campaigns = append(reg.Campaigns, provider.Campaign{ID: provider.Clean(k.ID, 40), BrandID: provider.Clean(k.BrandID, 40),
				UseCase: provider.Clean(strings.ToLower(k.UseCase), 40), State: st, Detail: detail})
		}
	}

	var assigned struct {
		Records []struct {
			PhoneNumber string `json:"phoneNumber"`
			CampaignID  string `json:"campaignId"`
			Assignment  string `json:"assignmentStatus"`
		} `json:"records"`
	}
	assignOK := section("campaigns", c.get(ctx, "/10dlc/phone_number_campaigns?recordsPerPage=500", &assigned))
	byNumber := map[string]string{}
	if assignOK && campaignsOK {
		for _, a := range assigned.Records {
			st := assignmentState(a.Assignment)
			if st == provider.RegApproved {
				if cs, ok := campaignState[a.CampaignID]; ok {
					st = cs
				} else {
					st = provider.RegUnknown
				}
			}
			byNumber[a.PhoneNumber] = st
		}
	}

	var toll struct {
		Records []struct {
			PhoneNumbers []struct {
				PhoneNumber string `json:"phoneNumber"`
			} `json:"phoneNumbers"`
			Status string `json:"verificationStatus"`
			Reason string `json:"reason"`
		} `json:"records"`
	}
	tollOK := section("tollfree", c.get(ctx, "/messaging_tollfree/verification/requests?page%5Bsize%5D=100", &toll))
	tollState := map[string]provider.NumberReg{}
	if tollOK {
		for _, v := range toll.Records {
			st := tollFreeState(v.Status)
			for _, p := range v.PhoneNumbers {
				if prev, ok := tollState[p.PhoneNumber]; !ok || rank(st) > rank(prev.State) {
					tollState[p.PhoneNumber] = provider.NumberReg{State: st, Detail: provider.Clean(v.Reason, 200)}
				}
			}
		}
	}

	if len(failed) >= 3 {
		return provider.Registration{}, firstErr
	}
	tenDLC := campaignsOK && assignOK
	for _, n := range numbers {
		nr := provider.NumberReg{Number: n.E164, Kind: provider.KindOf(n.E164), State: provider.RegUnknown}
		switch nr.Kind {
		case provider.RegKindTollFree:
			if tollOK {
				if v, ok := tollState[n.E164]; ok {
					nr.State, nr.Detail = v.State, v.Detail
				} else {
					nr.State, nr.Detail = provider.RegUnregistered, "no toll-free verification was submitted for this number"
				}
			}
		case provider.RegKind10DLC:
			if tenDLC {
				if st, ok := byNumber[n.E164]; ok {
					nr.State = st
				} else {
					nr.State, nr.Detail = provider.RegUnregistered, "the number is not assigned to a 10DLC campaign"
				}
			}
		default:
			nr.Detail = "no registration is read for numbers outside the US and Canada"
		}
		reg.Numbers = append(reg.Numbers, nr)
	}
	return reg, nil
}

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

// brandState maps a brand's status, falling back on its identity status.
func brandState(status, identity string) string {
	s := strings.ToUpper(status)
	switch {
	case s == "OK":
		return provider.RegApproved
	case strings.Contains(s, "PEND"):
		return provider.RegPending
	case strings.Contains(s, "FAIL") || strings.Contains(s, "REJECT"):
		return provider.RegRejected
	}
	switch strings.ToUpper(identity) {
	case "VERIFIED", "VETTED_VERIFIED", "SELF_DECLARED":
		return provider.RegApproved
	case "UNVERIFIED":
		return provider.RegPending
	}
	return provider.RegUnknown
}

// campaignStatus maps a campaign's lifecycle and submission status.
func campaignStatus(status, submission string) string {
	sub := strings.ToUpper(submission)
	switch {
	case strings.Contains(sub, "FAIL"):
		return provider.RegRejected
	case strings.Contains(sub, "PEND"):
		return provider.RegPending
	}
	switch strings.ToUpper(status) {
	case "ACTIVE":
		return provider.RegApproved
	case "EXPIRED", "DEACTIVATED":
		return provider.RegRejected
	case "PENDING":
		return provider.RegPending
	}
	return provider.RegUnknown
}

// assignmentState maps a number's assignment to its campaign; a settled
// assignment defers to the campaign's own state.
func assignmentState(s string) string {
	u := strings.ToUpper(s)
	switch {
	case u == "ASSIGNED" || u == "PROVISIONED":
		return provider.RegApproved
	case strings.Contains(u, "FAIL"):
		return provider.RegRejected
	case strings.Contains(u, "PEND"):
		return provider.RegPending
	}
	return provider.RegUnknown
}

// tollFreeState maps a toll-free verification request's status.
func tollFreeState(s string) string {
	u := strings.ToLower(s)
	switch {
	case u == "verified":
		return provider.RegApproved
	case strings.Contains(u, "reject"):
		return provider.RegRejected
	case strings.Contains(u, "waiting") || strings.Contains(u, "progress") || strings.Contains(u, "pending"):
		return provider.RegPending
	}
	return provider.RegUnknown
}
