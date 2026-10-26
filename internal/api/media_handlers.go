package api

// Phase 5 media handlers (spec contract 6): call recordings (listed, played
// and deleted; the rows and audio are written by hello-sip) and announcements
// (uploaded as multipart WAV, listed, deleted). Every mutation is audited and
// bumps the configuration revision like every other change.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/azrtydxb/hello/internal/media"
)

// Announcement upload bounds: a 64 MiB WAV at most, plus headroom for the
// multipart frame and the name field. That holds a ~5 minute 48 kHz stereo
// 16-bit song (about 55 MB) with room to spare; the stored copy is mono and
// half that size. The reverse proxies in front of the console allow 520 MiB,
// and MinIO takes the object as a multipart upload past 16 MiB on its own.
const (
	maxAnnouncementBytes  = 64 << 20
	maxAnnouncementUpload = maxAnnouncementBytes + (1 << 20)
)

// Recordings.

// listRecordings is GET /api/v1/recordings?extension=&before=&limit=, paged
// like the CDRs (contract 6).
func (s *server) listRecordings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	extension := q.Get("extension")
	if len(extension) > 32 || (extension != "" && !printable(extension, 32, false)) {
		badRequest(w, "extension must be at most 32 printable characters without spaces")
		return
	}
	var before int64
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			badRequest(w, "before must be a positive recording id")
			return
		}
		before = n
	}
	limit := defaultCDRLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxCDRLimit {
			badRequest(w, "limit must be 1-200")
			return
		}
		limit = n
	}
	rs, next, err := s.Store.ListRecordings(r.Context(), extension, before, limit)
	if err != nil {
		s.internal(w, "list recordings", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rs, "next": next})
}

// deleteRecording removes the row and then the MinIO object. The row wins:
// if the object removal fails it is logged, because a second delete attempt
// cannot find the row again, and an orphaned object harms nothing.
func (s *server) deleteRecording(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	object, err := s.Store.DeleteRecording(r.Context(), actor(r).String(), id)
	if err != nil {
		s.configError(w, "recording", err)
		return
	}
	if s.Objects != nil && object != "" {
		if err := s.Objects.RemoveRecording(r.Context(), object); err != nil {
			s.Log.Warn("media: remove recording audio", "error", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// recordingAudio is GET /api/v1/recordings/{id}/audio: the audio, streamed
// through hello-control (Range supported). The recording must exist before
// the store is touched. ?download=1 sends it as an attachment
// (recording-<id>.wav) so the browser saves it instead of playing it.
func (s *server) recordingAudio(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	download := ""
	switch r.URL.Query().Get("download") {
	case "", "0", "false":
	case "1", "true":
		download = fmt.Sprintf("recording-%d.wav", id)
	default:
		badRequest(w, "download must be true or false")
		return
	}
	rec, err := s.Store.GetRecording(r.Context(), id)
	if err != nil {
		s.configError(w, "recording", err)
		return
	}
	if s.Objects == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "object storage is unavailable; try again shortly")
		return
	}
	s.serveAudio(w, r, RecordingsBucket, rec.Object, download, "media: recording audio")
}

// Announcements.

func (s *server) listAnnouncements(w http.ResponseWriter, r *http.Request) {
	as, err := s.Store.ListAnnouncements(r.Context())
	if err != nil {
		s.internal(w, "list announcements", err)
		return
	}
	writeJSON(w, http.StatusOK, items(as))
}

// createAnnouncement is POST /api/v1/announcements: multipart with a name
// field and a WAV file part named "file" (at most 64 MiB). The
// bytes decide: a client-declared audio content type is not trusted.
func (s *server) createAnnouncement(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		badRequest(w, "upload as multipart form data with name and file parts")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAnnouncementUpload)
	var (
		name string
		data []byte
	)
	if err := readAnnouncementUpload(r, &name, &data); err != nil {
		badRequest(w, err.Error())
		return
	}
	var f fieldErrs
	if !usernameRe.MatchString(name) {
		f.add("name", "must be 1-64 of A-Z a-z 0-9 . _ -")
	}
	data = checkAnnouncementAudio(&f, data)
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	// A name the set already has is a conflict; checked before the upload, so
	// a duplicate POST cannot overwrite the existing audio (the object key is
	// the name).
	if _, ok, err := s.Store.AnnouncementByName(r.Context(), name); err != nil {
		s.internal(w, "read announcement", err)
		return
	} else if ok {
		writeError(w, http.StatusConflict, "conflict", "announcement: already exists")
		return
	}
	if s.Objects == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "object storage is unavailable; try again shortly")
		return
	}
	object := fmt.Sprintf("ann/%s.wav", name)
	if err := s.Objects.PutAnnouncement(r.Context(), object, bytes.NewReader(data), int64(len(data))); err != nil {
		s.Log.Error("media: upload announcement", "error", err)
		writeError(w, http.StatusBadGateway, "upstream", "object storage upload failed; try again shortly")
		return
	}
	a, err := s.Store.CreateAnnouncement(r.Context(), actor(r).String(), name, object, s.check())
	if err != nil {
		// The row was refused; do not leave the audio behind. The only way the
		// insert fails here is a name that raced in between the check and the
		// insert, whose object this upload has just overwritten - remove ours,
		// not theirs (same key, so theirs is gone regardless; log it).
		if rmErr := s.Objects.RemoveAnnouncement(r.Context(), object); rmErr != nil {
			s.Log.Warn("media: remove refused announcement audio", "error", rmErr)
		}
		s.configError(w, "announcement", err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

// replaceAnnouncement is PUT /api/v1/announcements/{id}: multipart with a
// WAV file part named "file" (validated like the upload) that overwrites the
// announcement's audio at the same key, ann/<name>.wav. The name cannot
// change (destinations name it, and the key follows it): an optional name
// part must match. The row's updated_at moves, audited with a revision bump
// and NOTIFY like every announcement change, so hello-sip reloads the audio.
func (s *server) replaceAnnouncement(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		badRequest(w, "upload as multipart form data with a file part")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAnnouncementUpload)
	var (
		name string
		data []byte
	)
	if err := readAnnouncementUpload(r, &name, &data); err != nil {
		badRequest(w, err.Error())
		return
	}
	a, err := s.Store.GetAnnouncement(r.Context(), id)
	if err != nil {
		s.configError(w, "announcement", err)
		return
	}
	var f fieldErrs
	if name != "" && name != a.Name {
		f.add("name", "cannot change; upload a new announcement under the other name")
	}
	data = checkAnnouncementAudio(&f, data)
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	if s.Objects == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "object storage is unavailable; try again shortly")
		return
	}
	object := fmt.Sprintf("ann/%s.wav", a.Name)
	if err := s.Objects.PutAnnouncement(r.Context(), object, bytes.NewReader(data), int64(len(data))); err != nil {
		s.Log.Error("media: replace announcement", "error", err)
		writeError(w, http.StatusBadGateway, "upstream", "object storage upload failed; try again shortly")
		return
	}
	// The audio is already replaced; a row that vanished in between (a
	// concurrent delete) answers 404 and leaves the object for its delete to
	// have removed or to be overwritten by a later upload of the same name.
	updated, err := s.Store.ReplaceAnnouncement(r.Context(), actor(r).String(), id)
	if err != nil {
		s.configError(w, "announcement", err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// announcementAudio is GET /api/v1/announcements/{id}/audio: the audio,
// streamed like the recording audio route.
func (s *server) announcementAudio(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	a, err := s.Store.GetAnnouncement(r.Context(), id)
	if err != nil {
		s.configError(w, "announcement", err)
		return
	}
	if s.Objects == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "object storage is unavailable; try again shortly")
		return
	}
	s.serveAudio(w, r, AnnouncementsBucket, a.Object, "", "media: announcement audio")
}

// checkAnnouncementAudio adds the file errors of an upload: required, RIFF/WAVE
// by its bytes, and playable. It returns the audio to store: 16-bit mono PCM at
// the file's own sample rate (capped at 48 kHz; the call plane resamples to
// each call's rate when it plays), with stereo down-mixed and 8, 24 and 32-bit
// integer or float PCM converted. Compressed WAVs are refused with the reason.
func checkAnnouncementAudio(f *fieldErrs, data []byte) []byte {
	if len(data) == 0 {
		f.add("file", "a WAV file is required")
		return data
	}
	if !isWAV(data) {
		f.add("file", "the audio must be a RIFF/WAVE file")
		return data
	}
	out, err := media.NormalizeWAV(data)
	if err != nil {
		f.add("file", "%s; %s", strings.TrimPrefix(err.Error(), "media: "), wavAdvice)
		return data
	}
	return out
}

// deleteAnnouncement removes the row and then the MinIO object (the row
// wins, as with recordings).
func (s *server) deleteAnnouncement(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	object, err := s.Store.DeleteAnnouncement(r.Context(), actor(r).String(), id, s.check())
	if err != nil {
		s.configError(w, "announcement", err)
		return
	}
	if s.Objects != nil && object != "" {
		if err := s.Objects.RemoveAnnouncement(r.Context(), object); err != nil {
			s.Log.Warn("media: remove announcement audio", "error", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// readAnnouncementUpload walks a multipart body and fills in the name and
// the WAV bytes. Every field is bounded; unknown parts are skipped.
func readAnnouncementUpload(r *http.Request, name *string, data *[]byte) error {
	mr, err := r.MultipartReader()
	if err != nil {
		return errors.New("invalid multipart body")
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return errors.New("invalid multipart body")
		}
		switch part.FormName() {
		case "name":
			b, err := io.ReadAll(io.LimitReader(part, 65))
			if err != nil {
				return errors.New("invalid name field")
			}
			if v := strings.TrimSpace(string(b)); v != "" {
				*name = v
			}
		case "file":
			b, err := readUpload(part, maxAnnouncementBytes+1, r.ContentLength)
			if err != nil {
				return errors.New("could not read file")
			}
			if len(b) > maxAnnouncementBytes {
				return errors.New("file is larger than 64 MiB")
			}
			*data = b
		}
		_ = part.Close()
	}
}

// wavAdvice tells an uploader what to send instead of a WAV Hello cannot use.
const wavAdvice = "upload an uncompressed PCM or float WAV file (8, 16, 24 or 32 bit, mono or stereo)"

// readUpload reads at most limit bytes of a multipart part. total is the
// request's Content-Length (or -1): it sizes the buffer once, so a 55 MB WAV
// is not copied through a chain of doubling buffers that would hold twice
// its size at the peak.
func readUpload(part io.Reader, limit, total int64) ([]byte, error) {
	var buf bytes.Buffer
	if total > 0 {
		// The request is a little larger than the file; bytes.Buffer needs
		// slack past the last byte or it reallocates at EOF.
		buf.Grow(int(min(total, limit)) + 1024)
	}
	if _, err := buf.ReadFrom(io.LimitReader(part, limit)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
