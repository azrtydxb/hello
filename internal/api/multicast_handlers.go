package api

// The multicast channel catalog (Paging > Multicast channels): the paging
// channels phone groups listen on and paging groups send to, and the
// Grandstream PTT channels a WP8x6's PTT button talks on.

import (
	"net/http"

	"github.com/azrtydxb/hello/internal/phonebook"
)

func (s *server) listMulticastChannels(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Store.ListMulticastChannels(r.Context())
	if err != nil {
		s.internal(w, "list multicast channels", err)
		return
	}
	writeJSON(w, http.StatusOK, items(cs))
}

func (s *server) getMulticastChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := s.Store.GetMulticastChannel(r.Context(), id)
	if err != nil {
		s.configError(w, "multicast channel", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// channelBody decodes and checks a catalog channel; it answers 400 itself.
// Priority defaults to 1 and kind to paging.
func channelBody(w http.ResponseWriter, r *http.Request) (phonebook.ChannelInput, bool) {
	in := phonebook.ChannelInput{Priority: 1, Kind: phonebook.KindPaging}
	if !decode(w, r, &in) {
		return in, false
	}
	if f := phonebook.ValidateChannel(in); len(f) > 0 {
		writePhonebookFields(w, f)
		return in, false
	}
	return in, true
}

func (s *server) createMulticastChannel(w http.ResponseWriter, r *http.Request) {
	in, ok := channelBody(w, r)
	if !ok {
		return
	}
	c, err := s.Store.CreateMulticastChannel(r.Context(), actor(r).String(), in)
	if err != nil {
		s.configError(w, "multicast channel", err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *server) putMulticastChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	in, ok := channelBody(w, r)
	if !ok {
		return
	}
	c, err := s.Store.UpdateMulticastChannel(r.Context(), actor(r).String(), id, in)
	if err != nil {
		s.configError(w, "multicast channel", err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *server) deleteMulticastChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteMulticastChannel(r.Context(), actor(r).String(), id); err != nil {
		s.configError(w, "multicast channel", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
