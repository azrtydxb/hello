package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/store"
)

// The tone library (migration 00052, spec phone-feature-parity S-43): WAV
// files the ring map can choose, kept in MinIO bucket hello-ringtones and
// served to phones under their token at /p/<token>/ring/<name>.wav.

// maxToneUpload bounds the request: the file's 512 KiB, plus the name field and framing.
const maxToneUpload = prov.MaxToneBytes + 64<<10

// maxToneJSON bounds a JSON upload: base64 of a 512 KiB tone is about 700 KiB,
// and an agent's request is at most 1 MiB.
const maxToneJSON = 1 << 20

func (s *server) listRingtones(w http.ResponseWriter, r *http.Request) {
	ts, err := s.Store.ListRingtones(r.Context())
	if err != nil {
		s.internal(w, "list ringtones", err)
		return
	}
	writeJSON(w, http.StatusOK, items(ts))
}

// createRingtone is POST /api/v1/ringtones: multipart with a name field and a
// WAV file part named "file". The bytes decide: PCM, 8 or 16 kHz, mono,
// 16-bit, at most 30 s and 512 KiB, else 400. A taken name is 409.
func (s *server) createRingtone(w http.ResponseWriter, r *http.Request) {
	var (
		name string
		data []byte
	)
	switch ct := r.Header.Get("Content-Type"); {
	case strings.HasPrefix(ct, "application/json"):
		// An agent through MCP sends the same fields as JSON; the audio is base64.
		r.Body = http.MaxBytesReader(w, r.Body, maxToneJSON)
		var in struct {
			Name        string `json:"name"`
			AudioBase64 []byte `json:"audioBase64"`
		}
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			badRequest(w, "invalid JSON body, or the body is larger than 1 MiB")
			return
		}
		name, data = strings.TrimSpace(in.Name), in.AudioBase64
	case strings.HasPrefix(ct, "multipart/"):
		r.Body = http.MaxBytesReader(w, r.Body, maxToneUpload)
		mr, err := r.MultipartReader()
		if err != nil {
			badRequest(w, "invalid multipart body")
			return
		}
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				badRequest(w, "invalid multipart body, or the request is larger than 512 KiB")
				return
			}
			switch part.FormName() {
			case "name":
				b, err := io.ReadAll(io.LimitReader(part, 65))
				if err != nil {
					badRequest(w, "invalid name field")
					return
				}
				name = strings.TrimSpace(string(b))
			case "file":
				b, err := io.ReadAll(io.LimitReader(part, prov.MaxToneBytes+1))
				if err != nil {
					badRequest(w, "could not read file")
					return
				}
				data = b
			}
			_ = part.Close()
		}
	default:
		badRequest(w, "upload as multipart form data with name and file parts, or JSON with name and audioBase64")
		return
	}
	var f fieldErrs
	if !prov.ValidToneName(name) {
		f.add("name", "must be 1 to 32 of a-z, 0-9 and -, starting with a letter or digit")
	}
	info, terr := prov.ParseToneWAV(data)
	if terr != nil {
		f.add("file", "%s; upload an uncompressed PCM WAV, mono, 16-bit, 8 or 16 kHz, at most 30 s and 512 KiB", strings.TrimPrefix(terr.Error(), "tone: "))
	}
	if len(f) > 0 {
		writeFields(w, f)
		return
	}
	// A taken name is refused before anything is uploaded (the key holds the
	// hash, so it could not overwrite audio, but the row is the truth).
	if ok, err := s.Store.RingtoneExists(r.Context(), name); err != nil {
		s.internal(w, "read ringtone", err)
		return
	} else if ok {
		writeError(w, http.StatusConflict, "conflict", "the tone "+name+" already exists; delete it first")
		return
	}
	if s.Objects == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "object storage is unavailable; try again shortly")
		return
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	key := store.RingtoneObjectKey(name, hash)
	if err := s.Objects.PutRingtone(r.Context(), key, bytes.NewReader(data), int64(len(data))); err != nil {
		s.Log.Error("ringtone: upload", "error", err)
		writeError(w, http.StatusBadGateway, "upstream", "object storage upload failed; try again shortly")
		return
	}
	t, err := s.Store.CreateRingtone(r.Context(), actor(r).String(), name, info, int64(len(data)), hash)
	if err != nil {
		// A concurrent upload of the same name won: its row names this very
		// key only when the audio is the same, so remove ours only otherwise.
		var taken *store.InUseError
		if !errors.As(err, &taken) {
			if rmErr := s.Objects.RemoveRingtone(r.Context(), key); rmErr != nil {
				s.Log.Warn("ringtone: remove refused audio", "error", rmErr)
			}
		}
		s.configError(w, "ringtone", err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// deleteRingtone removes the row and then the audio; a tone the ring map
// uses is 409.
func (s *server) deleteRingtone(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	key, err := s.Store.DeleteRingtone(r.Context(), actor(r).String(), id)
	if err != nil {
		s.configError(w, "ringtone", err)
		return
	}
	if s.Objects != nil && key != "" {
		if err := s.Objects.RemoveRingtone(r.Context(), key); err != nil {
			s.Log.Warn("ringtone: remove audio", "error", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
