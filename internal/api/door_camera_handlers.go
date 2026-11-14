package api

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/azrtydxb/hello/internal/safehttp"
	"github.com/azrtydxb/hello/internal/store"
)

// Door station cameras (spec phone-feature-parity S-49). An administrator
// gives a door station the address of its picture, an MJPEG stream or a
// snapshot, with the camera's credentials, which are sealed and never shown
// again. hello-control fetches the picture itself and hands it to the console
// on /camera/view, so the browser never needs a route to the camera or its
// credentials, and the fetch goes through the SSRF-guarded transport: only
// addresses in HELLO_DOOR_CAMERA_ALLOW (and public ones) can be reached.

const (
	// cameraStreamMax ends one proxied MJPEG stream.
	cameraStreamMax = 10 * time.Minute
	// cameraSnapshotMax bounds a snapshot's size and its fetch.
	cameraSnapshotMax = 4 << 20
	cameraSnapshotTTL = 10 * time.Second
	// cameraStreamsMax is how many pictures one node proxies at once.
	cameraStreamsMax = 8
)

// cameraStreams counts the pictures being proxied now.
var cameraStreams atomic.Int32

// doorCameraBody is the PUT body. password absent keeps the stored one and ""
// clears it.
type doorCameraBody struct {
	Kind     *string `json:"kind"`
	URL      *string `json:"url"`
	Username *string `json:"username"`
	Password *string `json:"password"`
}

// cameraStation resolves {id} to a door station: a missing extension is 404, an
// ordinary user extension 409.
func (s *server) cameraStation(w http.ResponseWriter, r *http.Request) (store.Extension, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return store.Extension{}, false
	}
	e, err := s.Store.GetExtension(r.Context(), id)
	if err != nil {
		s.storeError(w, "extension", err)
		return store.Extension{}, false
	}
	if e.Kind != store.ExtensionDoor {
		writeError(w, http.StatusConflict, "conflict", "only a door station has a camera")
		return store.Extension{}, false
	}
	return e, true
}

func (s *server) getDoorCamera(w http.ResponseWriter, r *http.Request) {
	e, ok := s.cameraStation(w, r)
	if !ok {
		return
	}
	c, err := s.Store.GetDoorCamera(r.Context(), e.ID)
	if err != nil {
		s.storeError(w, "door camera", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *server) putDoorCamera(w http.ResponseWriter, r *http.Request) {
	e, ok := s.cameraStation(w, r)
	if !ok {
		return
	}
	var b doorCameraBody
	if !decode(w, r, &b) {
		return
	}
	if b.Kind == nil || b.URL == nil {
		badRequest(w, "kind and url are required")
		return
	}
	in := store.DoorCameraInput{Kind: *b.Kind, URL: *b.URL, Password: b.Password}
	if b.Username != nil {
		in.Username = *b.Username
	}
	c, err := s.Store.SetDoorCamera(r.Context(), actor(r).String(), e.ID, in)
	if err != nil {
		s.configError(w, "door camera", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *server) deleteDoorCamera(w http.ResponseWriter, r *http.Request) {
	e, ok := s.cameraStation(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteDoorCamera(r.Context(), actor(r).String(), e.ID); err != nil {
		s.storeError(w, "door camera", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// viewDoorCamera is GET /extensions/{id}/camera/view: the camera's picture,
// fetched here with its credentials. A snapshot is one JPEG; an MJPEG stream
// is passed through until the client leaves or cameraStreamMax is up.
func (s *server) viewDoorCamera(w http.ResponseWriter, r *http.Request) {
	e, ok := s.cameraStation(w, r)
	if !ok {
		return
	}
	cam, found, err := s.Store.DoorCameraForView(r.Context(), e.ID)
	if err != nil {
		s.internal(w, "read door camera", err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "the door station has no camera")
		return
	}
	if s.CameraHTTP == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the camera proxy is not available")
		return
	}
	if cameraStreams.Add(1) > cameraStreamsMax {
		cameraStreams.Add(-1)
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many camera pictures are being shown; try again shortly")
		return
	}
	defer cameraStreams.Add(-1)

	limit := cameraStreamMax
	if cam.Kind == store.CameraSnapshot {
		limit = cameraSnapshotTTL
	}
	ctx, cancel := context.WithTimeout(r.Context(), limit)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cam.URL, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream", "the camera address is not usable")
		return
	}
	req.Header.Set("Accept", "multipart/x-mixed-replace, image/jpeg")
	if cam.Username != "" || cam.Password != "" {
		req.SetBasicAuth(cam.Username, cam.Password)
	}
	resp, err := s.CameraHTTP.Do(req)
	if err != nil {
		// The error text of the client names the URL; the response does not.
		msg := "the camera could not be reached"
		if errors.Is(err, safehttp.ErrBlockedAddress) {
			msg = "the camera address is not allowed; add its network to HELLO_DOOR_CAMERA_ALLOW"
		}
		s.Log.Warn("door camera fetch failed", "extension", e.Number, "blocked", errors.Is(err, safehttp.ErrBlockedAddress))
		writeError(w, http.StatusBadGateway, "upstream", msg)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		writeError(w, http.StatusBadGateway, "upstream", "the camera did not give a picture (it answered "+http.StatusText(resp.StatusCode)+")")
		return
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch {
	case ct == "image/jpeg", ct == "multipart/x-mixed-replace" && cam.Kind == store.CameraMJPEG:
	default:
		writeError(w, http.StatusBadGateway, "upstream", "the camera did not give a JPEG or MJPEG picture")
		return
	}
	h := w.Header()
	h.Set("Content-Type", resp.Header.Get("Content-Type"))
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if ct == "image/jpeg" {
		_, _ = io.Copy(w, io.LimitReader(resp.Body, cameraSnapshotMax))
		return
	}
	rc := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rc.Flush()
		}
		if err != nil {
			return
		}
	}
}
