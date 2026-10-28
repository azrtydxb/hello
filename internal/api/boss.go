package api

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/azrtydxb/hello/internal/store"
)

// Boss and secretary links (pbx-gaps S-18, S-19). The list is replaced as a
// whole, like the feature codes.

func (s *server) listBossLinks(w http.ResponseWriter, r *http.Request) {
	ls, err := s.Store.ListBossLinks(r.Context())
	if err != nil {
		s.internal(w, "list boss links", err)
		return
	}
	writeJSON(w, http.StatusOK, items(ls))
}

// BossLinkIn is one PUT /api/v1/boss-links item.
type BossLinkIn struct {
	BossExtensionID      int64  `json:"bossExtensionId"`
	AssistantExtensionID int64  `json:"assistantExtensionId"`
	RingMode             string `json:"ringMode"`
	Screening            bool   `json:"screening"`
	Enabled              *bool  `json:"enabled"`
	DivertOn             bool   `json:"divertOn"`
	OutboundAsBoss       bool   `json:"outboundAsBoss"`
}

func (s *server) putBossLinks(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Items []BossLinkIn `json:"items"`
	}
	if !decode(w, r, &in) {
		return
	}
	var f fieldErrs
	if len(in.Items) > store.MaxBossLinks {
		f.add("items", "at most %d links", store.MaxBossLinks)
	}
	bosses := map[int64]bool{}
	outbound := map[int64]bool{} // assistants that already carry an outbound-as-boss link
	for i, l := range in.Items {
		p := fmt.Sprintf("items[%d]", i)
		if l.BossExtensionID <= 0 {
			f.add(p+".bossExtensionId", "must be an extension id")
		}
		if l.AssistantExtensionID <= 0 {
			f.add(p+".assistantExtensionId", "must be an extension id")
		}
		if l.BossExtensionID == l.AssistantExtensionID && l.BossExtensionID > 0 {
			f.add(p+".assistantExtensionId", "must differ from the boss")
		}
		if !slices.Contains([]string{store.BossRingBoth, store.BossRingAssistantFirst}, l.RingMode) {
			f.add(p+".ringMode", `must be "both" or "assistant_first"`)
		}
		if bosses[l.BossExtensionID] {
			f.add(p+".bossExtensionId", "a boss has one assistant")
		}
		bosses[l.BossExtensionID] = true
		// An assistant with two outbound-as-boss links would not know whose
		// caller ID to present.
		if l.OutboundAsBoss {
			if outbound[l.AssistantExtensionID] {
				f.add(p+".outboundAsBoss", "an assistant may present the caller ID of one boss only")
			}
			outbound[l.AssistantExtensionID] = true
		}
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	links := make([]store.BossLinkInput, len(in.Items))
	for i, l := range in.Items {
		enabled := l.Enabled == nil || *l.Enabled
		links[i] = store.BossLinkInput{
			BossExtensionID: l.BossExtensionID, AssistantExtensionID: l.AssistantExtensionID, RingMode: l.RingMode,
			Screening: l.Screening, Enabled: enabled, DivertOn: l.DivertOn, OutboundAsBoss: l.OutboundAsBoss,
		}
	}
	if err := s.Store.PutBossLinks(r.Context(), actor(r).String(), links); err != nil {
		s.configError(w, "boss link", err)
		return
	}
	ls, err := s.Store.ListBossLinks(r.Context())
	if err != nil {
		s.internal(w, "list boss links", err)
		return
	}
	writeJSON(w, http.StatusOK, items(ls))
}
