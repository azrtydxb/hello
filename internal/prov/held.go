package prov

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/azrtydxb/hello/internal/location"
)

// The HELD server (spec phone-feature-parity S-28 d, RFC 5985): a phone with
// E911 on posts a locationRequest to /p/<token>/held and gets the location of
// its own phone back as a PIDF-LO. The token in the URL is the only identity;
// the request body names no one and is never used to pick a location, so a
// phone cannot ask for another's. Every denial is the handler's empty 404 as
// for the rest of the listener; a phone with no location gets a HELD error
// document, which is what the phone's E911 client expects.

// ErrNoLocation: the phone has no location assigned (HELD answers
// locationUnavailable).
var ErrNoLocation = errors.New("prov: the phone has no location")

// HELDLocation is the location of one phone.
type HELDLocation struct {
	Place location.Place
	// Extension is the phone's extension number, for the PIDF-LO entity;
	// empty when the phone has none.
	Extension string
}

// LocationSource is what a Store provides for the HELD server. internal/store
// implements it; a Store without it answers every request 404.
type LocationSource interface {
	// HELDLocation returns the location the phone resolves to (its own, else
	// its extension's, else its group's). ErrNotFound when the phone is gone,
	// ErrNoLocation when it has none.
	HELDLocation(ctx context.Context, phoneID int64) (HELDLocation, error)
}

// maxHELDRequest bounds a locationRequest body; a real one is a few hundred
// bytes.
const maxHELDRequest = 8 << 10

func (h *handler) serveHELD(w http.ResponseWriter, r *http.Request, ev *event, token string, https bool) {
	ctx := r.Context()
	rec, ok := h.authorize(w, r, ev, token, https)
	if !ok {
		return
	}
	src, ok := h.s.(LocationSource)
	if !ok {
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	}
	if !h.l.Phone(ctx, rec.ID) {
		h.empty(w, ev, http.StatusTooManyRequests, ResultRateLimited)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHELDRequest+1))
	if err != nil || len(body) > maxHELDRequest {
		h.held(w, ev, location.HELDError(location.ErrRequest, "the request is unreadable or too large"))
		return
	}
	req, err := location.ParseRequest(body)
	if err != nil {
		h.held(w, ev, location.HELDError(location.ErrRequest, err.Error()))
		return
	}
	loc, err := src.HELDLocation(ctx, rec.ID)
	switch {
	case errors.Is(err, ErrNotFound):
		h.empty(w, ev, http.StatusNotFound, ResultNotFound)
		return
	case errors.Is(err, ErrNoLocation):
		h.held(w, ev, location.HELDError(location.ErrLocationUnavailable, "no location is assigned to this phone"))
		return
	case err != nil:
		h.outage(w, ev, "held location", err)
		return
	}
	if !req.Answerable(loc.Place) {
		h.held(w, ev, location.HELDError(location.ErrLocationUnavailable, "the requested location type is not available"))
		return
	}
	host := ""
	if h.opt.PublicURL != nil {
		host = h.opt.PublicURL.Hostname()
	}
	entity := "pres:" + orDefault(loc.Extension, "phone") + "@" + orDefault(host, "hello")
	h.held(w, ev, location.HELDResponse(loc.Place, entity, time.Now()))
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// held answers a HELD document (RFC 5985 section 6.2: errors travel in a 200
// response as well).
func (h *handler) held(w http.ResponseWriter, ev *event, doc []byte) {
	w.Header().Set("Cache-Control", "no-store")
	h.body(w, ev, ResultServed, location.ContentTypeHELD, doc)
}
