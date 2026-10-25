package prov

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/prov/phonebook"
)

// MaxBookBytes is the largest directory the handler serves (spec P-17): a
// render past it is refused with a log line and an empty 404, so a phone
// keeps the directory it already has.
const MaxBookBytes = 2 << 20

// DefaultBookRefresh is HELLO_PROV_BOOK_REFRESH's default.
const DefaultBookRefresh = 15 * time.Minute

// bookSlotRE is the name after /p/<token>/pb/: a slot, 0 the merged set and
// 1 to 5 the separate sources.
var bookSlotRE = regexp.MustCompile(`^([0-5])\.xml$`)

// NewBookInfo is the BookInfo of a phone whose delivered books are named
// names, in order: base is the phone's own https://…/p/<token>/ URL. It is
// nil when there are no books or no base URL (provisioning off). A vendor
// that takes one merged file uses URL; Yealink uses Sources.
func NewBookInfo(base string, names []string, refresh time.Duration) *BookInfo {
	if base == "" || len(names) == 0 {
		return nil
	}
	if refresh <= 0 {
		refresh = DefaultBookRefresh
	}
	srcs := make([]phonebook.Book, len(names))
	for i, n := range names {
		srcs[i] = phonebook.Book{Name: n}
	}
	srcs = phonebook.Group(srcs, phonebook.MaxSources)
	b := &BookInfo{
		URL:            base + "pb/0.xml",
		HostPath:       strings.TrimPrefix(base, "https://") + "pb/0.xml",
		Sources:        make([]BookSource, phonebook.MaxSources),
		RefreshSeconds: int(refresh / time.Second),
		RefreshMinutes: max(int(refresh/time.Minute), 1),
	}
	for i, s := range srcs {
		b.Sources[i] = BookSource{Name: s.Name, URL: base + "pb/" + strconv.Itoa(i+1) + ".xml"}
	}
	return b
}

// serveBookSlot answers /p/<token>/pb/<name> (spec P-4).
func (h *handler) serveBookSlot(w http.ResponseWriter, r *http.Request, ev *event, token, name string, https bool) {
	ev.rec.Kind = KindPhonebook
	rec, ok := h.authorize(w, r, ev, token, https)
	if !ok {
		return
	}
	m := bookSlotRE.FindStringSubmatch(name)
	if m == nil {
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	}
	slot, _ := strconv.Atoi(m[1])
	h.serveBook(w, r, ev, rec, token, slot)
}

// serveBook renders the phone's directory for a slot in its vendor's
// format. The slot is mapped to books through the token's phone, never
// through a book id in the URL, so a phone cannot name another group's
// book (spec P-5). It is rate-limited per source address like every
// request but not by the per-phone config limit: a Yealink phone fetches up
// to five sources on each refresh.
func (h *handler) serveBook(w http.ResponseWriter, r *http.Request, ev *event, rec PhoneRecord, token string, slot int) {
	ctx := r.Context()
	ev.rec.Kind = KindPhonebook
	ev.rec.MACClaimed = ""
	f, ok := phonebook.FormatFor(string(rec.Vendor))
	if !ok {
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	}
	books, err := h.s.DeliveredBooks(ctx, rec.ID)
	switch {
	case errors.Is(err, ErrNotFound):
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	case err != nil:
		h.outage(w, ev, "delivered books", err)
		return
	}
	if slot > 0 {
		books = phonebook.Group(books, f.Sources)
		if f.Sources < 2 || slot > len(books) {
			h.empty(w, ev, http.StatusNotFound, ResultNotFound)
			return
		}
		books = books[slot-1 : slot]
	}
	if len(books) == 0 {
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	}
	body, truncated, err := phonebook.Render(string(rec.Vendor), phonebook.Merge(books))
	if err != nil || len(body) > MaxBookBytes {
		h.opt.Log.Error("provisioning: directory not served", "phone", rec.ID, "vendor", rec.Vendor, "bytes", len(body), "error", err)
		h.empty(w, ev, http.StatusNotFound, ResultRenderError)
		return
	}
	if truncated {
		h.opt.Log.Warn("provisioning: directory cut to the vendor's capacity", "phone", rec.ID, "vendor", rec.Vendor, "max", f.MaxEntries)
	}
	ua := r.UserAgent()
	hash := HashToken(token)
	st := FetchState{At: ev.rec.At, IP: ev.rec.IP, UserAgent: ev.rec.UserAgent, File: "pb/" + strconv.Itoa(slot) + ".xml",
		FirmwareSeen: uaFirmware(ua), UAMismatch: ev.rec.UAMismatch}
	if m := uaMAC(ua); m != "" && m != rec.MAC {
		st.UAMismatch, ev.rec.UAMismatch = true, true
	}
	if err := h.s.MarkFetched(ctx, rec.ID, hash, st); err != nil {
		h.outage(w, ev, "mark fetched", err)
		return
	}
	etag := etagOf(body)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-store")
	if etagMatch(r.Header.Get("If-None-Match"), etag) {
		ev.rec.Status, ev.rec.Result = http.StatusNotModified, ResultNotModified
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.body(w, ev, ResultServed, f.ContentType, body)
}
