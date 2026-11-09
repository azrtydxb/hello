package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/fax"
	"github.com/azrtydxb/hello/internal/store"
)

// Fax (migration 00052, pbx-gaps S-15 to S-17). The API lists and downloads
// received and sent faxes, queues a PDF to send, and manages the fax
// destinations a route or IVR key can name. The engine that rasterises the
// PDF and carries the call is hello-fax; hello-sip places and answers the
// calls.

// FaxStore is the persistence the fax routes need; *store.Store implements it.
type FaxStore interface {
	ListFaxDestinations(ctx context.Context) ([]fax.Destination, error)
	GetFaxDestination(ctx context.Context, id int64) (fax.Destination, error)
	CreateFaxDestination(ctx context.Context, actor string, in store.FaxDestinationInput) (fax.Destination, error)
	UpdateFaxDestination(ctx context.Context, actor string, id int64, in store.FaxDestinationInput) (fax.Destination, error)
	DeleteFaxDestination(ctx context.Context, actor string, id int64) error

	ListFaxes(ctx context.Context, f fax.ListFilter) ([]fax.Fax, error)
	GetFax(ctx context.Context, id int64) (fax.Fax, error)
	CreateOutboundFax(ctx context.Context, o store.OutboundFax) (fax.Fax, error)
	SetFaxObject(ctx context.Context, id int64, key string) error
	DeleteFax(ctx context.Context, actor string, id int64) (string, error)
	CancelFax(ctx context.Context, actor string, id int64) error
	FaxOwnerExtensions(ctx context.Context, userID int64) ([]string, error)
}

// FaxEngine is the part of hello-fax the API calls: it checks that a PDF can
// be rasterised and says how many pages it has.
type FaxEngine interface {
	Rasterise(ctx context.Context, pdf []byte) (int, error)
}

// FaxConfig wires the fax routes. A nil Store answers 503 on all of them;
// POST /faxes also needs Enabled, Engine and Objects.
type FaxConfig struct {
	Store   FaxStore
	Engine  FaxEngine
	Objects fax.Objects
	Enabled bool
}

func (s *server) faxUnavailable(w http.ResponseWriter) bool {
	if s.Fax.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "fax is not configured")
		return true
	}
	return false
}

// --- fax destinations -------------------------------------------------------

type faxDestBody struct {
	Name      *string `json:"name"`
	Extension *string `json:"extension"`
	Email     *string `json:"email"`
}

func (b faxDestBody) input(base store.FaxDestinationInput) (store.FaxDestinationInput, fieldErrs) {
	if b.Name != nil {
		base.Name = *b.Name
	}
	if b.Extension != nil {
		base.Extension = *b.Extension
	}
	if b.Email != nil {
		base.Email = strings.TrimSpace(*b.Email)
	}
	var f fieldErrs
	if !usernameRe.MatchString(base.Name) || len(base.Name) > 40 {
		f.add("name", "must be 1-40 of A-Z a-z 0-9 . _ -")
	}
	if !numberRe.MatchString(base.Extension) {
		f.add("extension", "must be an extension number (2-10 digits)")
	}
	if base.Email != "" {
		if a, err := mail.ParseAddress(base.Email); err != nil || a.Address != base.Email || len(base.Email) > 254 {
			f.add("email", "must be empty or one email address")
		}
	}
	return base, f
}

func (s *server) listFaxDestinations(w http.ResponseWriter, r *http.Request) {
	if s.faxUnavailable(w) {
		return
	}
	v, err := s.Fax.Store.ListFaxDestinations(r.Context())
	if err != nil {
		s.internal(w, "list fax destinations", err)
		return
	}
	writeJSON(w, http.StatusOK, items(v))
}

func (s *server) getFaxDestination(w http.ResponseWriter, r *http.Request) {
	if s.faxUnavailable(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := s.Fax.Store.GetFaxDestination(r.Context(), id)
	if err != nil {
		s.storeError(w, "fax destination", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) createFaxDestination(w http.ResponseWriter, r *http.Request) {
	if s.faxUnavailable(w) {
		return
	}
	var b faxDestBody
	if !decode(w, r, &b) {
		return
	}
	if b.Name == nil || b.Extension == nil {
		badRequest(w, "name and extension are required")
		return
	}
	in, f := b.input(store.FaxDestinationInput{})
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	v, err := s.Fax.Store.CreateFaxDestination(r.Context(), actor(r).String(), in)
	if err != nil {
		s.configError(w, "fax destination", err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *server) putFaxDestination(w http.ResponseWriter, r *http.Request) {
	if s.faxUnavailable(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var b faxDestBody
	if !decode(w, r, &b) {
		return
	}
	cur, err := s.Fax.Store.GetFaxDestination(r.Context(), id)
	if err != nil {
		s.storeError(w, "fax destination", err)
		return
	}
	if b.Name != nil && *b.Name != cur.Name {
		badRequest(w, "the name of a fax destination cannot change")
		return
	}
	in, f := b.input(store.FaxDestinationInput{Name: cur.Name, Extension: cur.Extension, Email: cur.Email})
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	v, err := s.Fax.Store.UpdateFaxDestination(r.Context(), actor(r).String(), id, in)
	if err != nil {
		s.configError(w, "fax destination", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) deleteFaxDestination(w http.ResponseWriter, r *http.Request) {
	if s.faxUnavailable(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Fax.Store.DeleteFaxDestination(r.Context(), actor(r).String(), id); err != nil {
		s.configError(w, "fax destination", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- faxes --------------------------------------------------------------------

func (s *server) faxList(w http.ResponseWriter, r *http.Request, owners []string) {
	limit, before, ok := pageParams(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	fl := fax.ListFilter{Direction: q.Get("direction"), Status: q.Get("status"), Owners: owners, Limit: limit, Before: before}
	if fl.Direction != "" && fl.Direction != fax.DirInbound && fl.Direction != fax.DirOutbound {
		badRequest(w, `direction must be "inbound" or "outbound"`)
		return
	}
	if fl.Status != "" && !slices.Contains(fax.Statuses, fl.Status) {
		badRequest(w, "status must be one of "+strings.Join(fax.Statuses, ", "))
		return
	}
	v, err := s.Fax.Store.ListFaxes(r.Context(), fl)
	if err != nil {
		s.internal(w, "list faxes", err)
		return
	}
	var next int64
	if len(v) == limit && len(v) > 0 {
		next = v[len(v)-1].ID
	}
	writePage(w, v, next)
}

func (s *server) listFaxes(w http.ResponseWriter, r *http.Request) {
	if s.faxUnavailable(w) {
		return
	}
	s.faxList(w, r, nil)
}

func (s *server) getFax(w http.ResponseWriter, r *http.Request) {
	if s.faxUnavailable(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := s.Fax.Store.GetFax(r.Context(), id)
	if err != nil {
		s.storeError(w, "fax", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *server) sendFaxPDF(w http.ResponseWriter, r *http.Request, f fax.Fax) {
	if s.Fax.Objects == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "fax storage is not configured")
		return
	}
	if f.ObjectKey == "" {
		writeError(w, http.StatusNotFound, "not_found", "this fax has no document")
		return
	}
	pdf, err := s.Fax.Objects.Get(r.Context(), f.ObjectKey)
	if err != nil {
		s.Log.Error("fax pdf", "fax_id", f.ID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the document is unavailable; try again shortly")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+fax.AttachmentName(f.ID, f.CreatedAt)+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(pdf)
}

func (s *server) faxPDF(w http.ResponseWriter, r *http.Request) {
	if s.faxUnavailable(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	f, err := s.Fax.Store.GetFax(r.Context(), id)
	if err != nil {
		s.storeError(w, "fax", err)
		return
	}
	s.sendFaxPDF(w, r, f)
}

// maxFaxForm bounds a send request: the PDF plus the form fields.
const maxFaxForm = fax.MaxSendBytes + 64<<10

// maxFaxJSONPDF is the PDF a JSON request can carry (base64 of 700 KiB fits
// the 1 MiB request limit).
const maxFaxJSONPDF = 700 << 10

// readFaxUpload reads the multipart form of POST /faxes: file (the PDF), to
// and callerId.
func readFaxUpload(w http.ResponseWriter, r *http.Request) (pdf []byte, to, callerID string, ok bool) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		// The form an agent uses: its requests are at most 1 MiB, so up to
		// 700 KiB of PDF.
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var in struct {
			To        string `json:"to"`
			CallerID  string `json:"callerId"`
			PDFBase64 []byte `json:"pdfBase64"`
		}
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				badRequest(w, "the JSON body is larger than 1 MiB; send a larger PDF as multipart form data")
			} else {
				badRequest(w, "invalid JSON body: "+jsonProblem(err))
			}
			return nil, "", "", false
		}
		if len(in.PDFBase64) > maxFaxJSONPDF {
			var f fieldErrs
			f.add("pdfBase64", "at most 700 KiB of PDF fits a JSON request; send a larger PDF as multipart form data")
			writeFields(w, f)
			return nil, "", "", false
		}
		return in.PDFBase64, strings.TrimSpace(in.To), strings.TrimSpace(in.CallerID), true
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFaxForm)
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		badRequest(w, "upload as multipart form data with a file part, to and optionally callerId, or as JSON with to, callerId and pdfBase64")
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		badRequest(w, "invalid multipart body")
		return
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return pdf, to, callerID, true
		}
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("the PDF is larger than %d MB", fax.MaxSendBytes>>20))
			} else {
				badRequest(w, "invalid multipart body")
			}
			return nil, "", "", false
		}
		switch part.FormName() {
		case "to", "callerId":
			b, err := io.ReadAll(io.LimitReader(part, 65))
			if err != nil {
				badRequest(w, "invalid "+part.FormName()+" field")
				return nil, "", "", false
			}
			if part.FormName() == "to" {
				to = strings.TrimSpace(string(b))
			} else {
				callerID = strings.TrimSpace(string(b))
			}
		case "file":
			b, err := readUpload(part, fax.MaxSendBytes+1, r.ContentLength)
			if err != nil {
				var tooBig *http.MaxBytesError
				if errors.As(err, &tooBig) {
					writeError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("the PDF is larger than %d MB", fax.MaxSendBytes>>20))
				} else {
					badRequest(w, "could not read file")
				}
				return nil, "", "", false
			}
			if len(b) > fax.MaxSendBytes {
				writeError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("the PDF is larger than %d MB", fax.MaxSendBytes>>20))
				return nil, "", "", false
			}
			pdf = b
		}
		_ = part.Close()
	}
}

func (s *server) sendFax(w http.ResponseWriter, r *http.Request) {
	if s.faxUnavailable(w) {
		return
	}
	if !s.Fax.Enabled || s.Fax.Engine == nil || s.Fax.Objects == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "sending faxes is not enabled on this system")
		return
	}
	pdf, to, callerID, ok := readFaxUpload(w, r)
	if !ok {
		return
	}
	var f fieldErrs
	if !fax.ValidDestinationNumber(to) {
		f.add("to", "must be an E.164 number such as +41441234567")
	}
	if callerID != "" && !fax.ValidCallerID(callerID) {
		f.add("callerId", "must be empty or 1-20 digits with an optional leading +")
	}
	if len(pdf) == 0 {
		f.add("file", "the PDF is required")
	} else if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		f.add("file", "must be a PDF")
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	pages, err := s.Fax.Engine.Rasterise(ctx, pdf)
	switch {
	case errors.Is(err, fax.ErrRejected), errors.Is(err, fax.ErrTooManyPages), errors.Is(err, fax.ErrTooLarge):
		writeError(w, http.StatusUnprocessableEntity, "unprocessable", "the PDF cannot be sent as a fax: "+strings.TrimPrefix(err.Error(), "fax: "))
		return
	case err != nil:
		s.Log.Error("fax rasterise", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the fax engine is unavailable; try again shortly")
		return
	}
	if pages > fax.MaxSendPages {
		writeError(w, http.StatusUnprocessableEntity, "unprocessable", fmt.Sprintf("the PDF has more than %d pages", fax.MaxSendPages))
		return
	}
	row, err := s.Fax.Store.CreateOutboundFax(ctx, store.OutboundFax{Peer: to, CallerID: callerID, Actor: actor(r).String(), Pages: pages})
	if err != nil {
		s.internal(w, "queue fax", err)
		return
	}
	key := fax.ObjectKey(row.ID, row.CreatedAt)
	if err := fax.PutRetry(ctx, s.Fax.Objects, key, pdf); err == nil {
		err = s.Fax.Store.SetFaxObject(ctx, row.ID, key)
		if err != nil {
			s.Log.Error("fax object key", "fax_id", row.ID, "error", err)
		}
	} else {
		s.Log.Error("fax store pdf", "fax_id", row.ID, "error", err)
		_, _ = s.Fax.Store.DeleteFax(context.WithoutCancel(ctx), actor(r).String(), row.ID)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the document could not be stored; try again shortly")
		return
	}
	row.ObjectKey, row.HasPDF = key, true
	writeJSON(w, http.StatusCreated, row)
}

func (s *server) deleteFax(w http.ResponseWriter, r *http.Request) {
	if s.faxUnavailable(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	who := actor(r).String()
	key, err := s.Fax.Store.DeleteFax(r.Context(), who, id)
	if errors.Is(err, fax.ErrNotCancellable) {
		// A fax being sent is cancelled instead: its node drops the call at
		// its next lease renewal, and the row stays as the record.
		if cerr := s.Fax.Store.CancelFax(r.Context(), who, id); cerr == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, http.StatusConflict, "conflict", "the fax is in progress; try again when the call has ended")
		return
	}
	if err != nil {
		s.storeError(w, "fax", err)
		return
	}
	if key != "" && s.Fax.Objects != nil {
		if err := s.Fax.Objects.Remove(r.Context(), key); err != nil {
			s.Log.Warn("fax: remove document", "fax_id", id, "error", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- self service -----------------------------------------------------------------

func (s *server) listMyFaxes(w http.ResponseWriter, r *http.Request) {
	if s.faxUnavailable(w) {
		return
	}
	uid, ok := s.selfUser(w, r)
	if !ok {
		return
	}
	owners, err := s.Fax.Store.FaxOwnerExtensions(r.Context(), uid)
	if err != nil {
		s.internal(w, "own fax extensions", err)
		return
	}
	if owners == nil {
		owners = []string{} // none is none: a nil filter would mean every fax
	}
	s.faxList(w, r, owners)
}

func (s *server) myFaxPDF(w http.ResponseWriter, r *http.Request) {
	if s.faxUnavailable(w) {
		return
	}
	uid, ok := s.selfUser(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	owners, err := s.Fax.Store.FaxOwnerExtensions(r.Context(), uid)
	if err != nil {
		s.internal(w, "own fax extensions", err)
		return
	}
	f, err := s.Fax.Store.GetFax(r.Context(), id)
	if err != nil || f.OwnerExtension == "" || !slices.Contains(owners, f.OwnerExtension) {
		// A fax that is not the caller's reads as one that does not exist.
		writeError(w, http.StatusNotFound, "not_found", "fax: not found")
		return
	}
	s.sendFaxPDF(w, r, f)
}
