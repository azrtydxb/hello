package api

// Messaging media (spec messaging-channels M-18): an agent's uploads to a
// conversation, the authorised download of an attachment (only for people
// who can see its conversation), a chat site's logo, and the removal of an
// erased customer's files. The bytes are sniffed and checked against the
// allowlist here, before anything is stored; nothing logs a file name or a
// type a customer sent.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/messaging/media"
	"github.com/azrtydxb/hello/internal/store"
)

// MediaStore is the attachment persistence the API needs; *store.Store
// implements it. The inbox store carries it.
type MediaStore interface {
	BeginInboxUpload(ctx context.Context, acc store.InboxAccess, convID int64, in store.MediaUploadInput) (store.MediaUpload, error)
	FinishMediaUpload(ctx context.Context, id int64) (store.ConvMedia, error)
	AbortMediaUpload(ctx context.Context, id int64) error
	InboxMedia(ctx context.Context, acc store.InboxAccess, id int64) (store.MediaFile, error)
	DropMedia(ctx context.Context, g media.Garbage) error
	SetChatSiteLogo(ctx context.Context, actor string, id int64, ctype string) (store.ChatSite, error)
	ClearChatSiteLogo(ctx context.Context, actor string, id int64) error
}

var _ MediaStore = (*store.Store)(nil)

// mediaStore returns the store and the object store, or answers 503.
func (s *server) mediaStore(w http.ResponseWriter) (MediaStore, media.Objects, bool) {
	ms, ok := s.Inbox.(MediaStore)
	if s.Inbox == nil || !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the messaging inbox is not configured")
		return nil, nil, false
	}
	if s.MediaObjects == nil {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the media store (MinIO) is not configured")
		return nil, nil, false
	}
	return ms, s.MediaObjects, true
}

// mediaForm is a multipart upload: one file and the named text fields.
type mediaForm struct {
	data     []byte
	filename string
	declared string
	fields   map[string]string
}

// readMediaForm reads a multipart body with one part "file" of at most max
// bytes and short text fields. It answers 400 or 413 itself (ok false).
func readMediaForm(w http.ResponseWriter, r *http.Request, max int64) (mediaForm, bool) {
	f := mediaForm{fields: map[string]string{}}
	tooBig := func() {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "the file is larger than "+media.Size(max))
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		badRequest(w, "send the file as multipart/form-data with a part named file")
		return f, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, max+64<<10)
	mr, err := r.MultipartReader()
	if err != nil {
		badRequest(w, "invalid multipart body")
		return f, false
	}
	seenFile := false
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var mb *http.MaxBytesError
			if errors.As(err, &mb) {
				tooBig()
			} else {
				badRequest(w, "invalid multipart body")
			}
			return f, false
		}
		name := part.FormName()
		switch {
		case name == "file" && !seenFile:
			seenFile = true
			b, err := readUpload(part, max+1, r.ContentLength)
			if err != nil {
				var mb *http.MaxBytesError
				if errors.As(err, &mb) {
					tooBig()
				} else {
					badRequest(w, "could not read the file")
				}
				return f, false
			}
			if int64(len(b)) > max {
				tooBig()
				return f, false
			}
			f.data, f.filename, f.declared = b, part.FileName(), part.Header.Get("Content-Type")
		case name != "" && name != "file":
			b, err := io.ReadAll(io.LimitReader(part, 257))
			if err != nil || len(b) > 256 {
				badRequest(w, "the "+name+" field is too long")
				return f, false
			}
			f.fields[name] = strings.TrimSpace(string(b))
		}
		_ = part.Close()
	}
	if !seenFile {
		writeFields(w, fieldErrs{{Path: "file", Message: "is required"}})
		return f, false
	}
	return f, true
}

// writeRefusal answers a file the allowlist or the size cap refused.
func writeRefusal(w http.ResponseWriter, ref *media.Refusal) {
	if ref.Reason == media.ReasonTooLarge {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", ref.Detail)
		return
	}
	writeError(w, http.StatusUnprocessableEntity, "unsupported_media", ref.Detail)
}

func (s *server) uploadInboxMediaCommon(w http.ResponseWriter, r *http.Request, all bool) {
	is, acc, ok := s.inboxCaller(w, r, all, true)
	if !ok {
		return
	}
	ms, objs, ok := s.mediaStore(w)
	if !ok {
		return
	}
	// Only a website chat carries an agent's attachments in this build, so
	// its rules (images and PDF, 5 MiB) are the ones an upload meets.
	rules := media.ChatRules()
	form, ok := readMediaForm(w, r, rules.Max)
	if !ok {
		return
	}
	conv, err := strconv.ParseInt(form.fields["conversationId"], 10, 64)
	if err != nil || conv <= 0 {
		writeFields(w, fieldErrs{{Path: "conversationId", Message: "is required: the conversation the file is for"}})
		return
	}
	c, err := is.GetConversation(r.Context(), acc, conv)
	if err != nil {
		s.inboxError(w, "conversation", err)
		return
	}
	if c.ChannelKind != messaging.KindWebchat {
		s.inboxError(w, "conversation", store.ErrMediaUnsupported)
		return
	}
	in, refusal := media.Inspect(form.data, form.declared, rules)
	if refusal != nil {
		writeRefusal(w, refusal)
		return
	}
	up, err := ms.BeginInboxUpload(r.Context(), acc, conv, store.MediaUploadInput{Inspected: in, Filename: form.filename, Declared: form.declared})
	if err != nil {
		s.inboxError(w, "attachment", err)
		return
	}
	if err := objs.Put(r.Context(), up.Key, bytes.NewReader(form.data), in.Size, in.ContentType); err != nil {
		_ = ms.AbortMediaUpload(context.WithoutCancel(r.Context()), up.ID)
		_ = objs.Remove(context.WithoutCancel(r.Context()), up.Key)
		s.Log.Error("attachment not stored", "error", err)
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the media store is unavailable; try again shortly")
		return
	}
	m, err := ms.FinishMediaUpload(r.Context(), up.ID)
	if err != nil {
		s.inboxError(w, "attachment", err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *server) uploadInboxMedia(w http.ResponseWriter, r *http.Request) {
	s.uploadInboxMediaCommon(w, r, true)
}

func (s *server) uploadMyInboxMedia(w http.ResponseWriter, r *http.Request) {
	s.uploadInboxMediaCommon(w, r, false)
}

func (s *server) getInboxMediaCommon(w http.ResponseWriter, r *http.Request, all bool) {
	_, acc, ok := s.inboxCaller(w, r, all, false)
	if !ok {
		return
	}
	ms, objs, ok := s.mediaStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	f, err := ms.InboxMedia(r.Context(), acc, id)
	if err != nil {
		s.inboxError(w, "attachment", err)
		return
	}
	media.Serve(w, r, objs, f.Stored(), s.Log)
}

func (s *server) getInboxMedia(w http.ResponseWriter, r *http.Request) {
	s.getInboxMediaCommon(w, r, true)
}

func (s *server) getMyInboxMedia(w http.ResponseWriter, r *http.Request) {
	s.getInboxMediaCommon(w, r, false)
}

// eraseMediaObjects removes the files of an erased customer's attachments,
// checks each is gone and drops its row (spec M-20). A file the store could
// not remove now stays for the media sweeper, and the report says so.
func (s *server) eraseMediaObjects(ctx context.Context, is InboxStore, rep *store.EraseReport) {
	rep.MediaObjects, rep.MediaObjectsPending = s.removeMediaObjects(ctx, is, rep.Media, "erase")
}

// removeMediaObjects removes the files of attachments an erase or a
// redaction detached, checks each is gone and drops its row. It returns how
// many files went and how many the media sweeper removes later.
func (s *server) removeMediaObjects(ctx context.Context, is InboxStore, gs []media.Garbage, what string) (removed, pending int) {
	ms, ok := is.(MediaStore)
	for _, g := range gs {
		if g.Key != "" {
			if s.MediaObjects == nil || !ok {
				pending++
				continue
			}
			if err := media.RemoveVerified(ctx, s.MediaObjects, g.Key); err != nil {
				s.Log.Warn(what+": attachment not removed yet", "media", g.ID, "error", err)
				pending++
				continue
			}
			removed++
		}
		if ok {
			if err := ms.DropMedia(ctx, g); err != nil {
				s.Log.Warn(what+": attachment row not dropped", "media", g.ID, "error", err)
			}
		}
	}
	return removed, pending
}

// Chat site logos.

func (s *server) putChatSiteLogo(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.chatStore(w)
	if !ok {
		return
	}
	ms, objs, ok := s.mediaStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := cs.GetChatSite(r.Context(), id); err != nil {
		s.inboxError(w, "chat site", err)
		return
	}
	rules := media.LogoRules()
	form, ok := readMediaForm(w, r, rules.Max)
	if !ok {
		return
	}
	in, refusal := media.Inspect(form.data, form.declared, rules)
	if refusal != nil {
		writeRefusal(w, refusal)
		return
	}
	if err := objs.Put(r.Context(), media.LogoKey(id), bytes.NewReader(form.data), in.Size, in.ContentType); err != nil {
		s.Log.Error("chat site logo not stored", "error", err)
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the media store is unavailable; try again shortly")
		return
	}
	site, err := ms.SetChatSiteLogo(r.Context(), actor(r).String(), id, in.ContentType)
	if err != nil {
		s.inboxError(w, "chat site", err)
		return
	}
	writeJSON(w, http.StatusOK, viewChatSite(site))
}

func (s *server) deleteChatSiteLogo(w http.ResponseWriter, r *http.Request) {
	ms, objs, ok := s.mediaStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := ms.ClearChatSiteLogo(r.Context(), actor(r).String(), id); err != nil {
		s.inboxError(w, "chat site logo", err)
		return
	}
	if err := objs.Remove(r.Context(), media.LogoKey(id)); err != nil {
		s.Log.Warn("chat site logo object not removed", "site", id, "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) getChatSiteLogo(w http.ResponseWriter, r *http.Request) {
	cs, ok := s.chatStore(w)
	if !ok {
		return
	}
	_, objs, ok := s.mediaStore(w)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	site, err := cs.GetChatSite(r.Context(), id)
	if err != nil {
		s.inboxError(w, "chat site", err)
		return
	}
	if !site.Logo {
		writeError(w, http.StatusNotFound, "not_found", "the site has no logo")
		return
	}
	media.Serve(w, r, objs, media.Stored{Key: media.LogoKey(id), ContentType: site.LogoType,
		Filename: fmt.Sprintf("chat-site-%d-logo%s", id, media.Extension(site.LogoType))}, s.Log)
}
