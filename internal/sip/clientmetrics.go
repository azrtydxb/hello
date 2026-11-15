package sip

// The client series of hello-sip (clients-webrtc-mobile-video S-52):
// registrations by device kind and the failed authentications of web and
// mobile devices. Labels are words from a fixed list, never a device name.

import (
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/snapshot"
)

// bindingKind is the kind of the device behind a binding: web or mobile for
// a client, desk otherwise (and when the snapshot does not know it).
func bindingKind(snap *snapshot.Snapshot, b livestate.Binding) string {
	if snap != nil {
		if d, ok := snap.DeviceByUsername(b.Device); ok && (d.Kind == auth.KindWeb || d.Kind == auth.KindMobile) {
			return d.Kind
		}
	}
	return "desk"
}

// isClientName reports whether a digest username is a web or mobile
// device's: an expiring derived name, or the base name of a derived device.
func isClientName(snap *snapshot.Snapshot, username string) bool {
	if looksDerived(username) {
		return true
	}
	d, ok := snap.DeviceByUsername(username)
	return ok && d.AuthMode == snapshot.AuthDerived
}

// clientAuthReason maps the failed digest check of a derived credential to
// hello_client_auth_failures_total's reason.
func clientAuthReason(check string) string {
	switch check {
	case CheckExpired:
		return "expired"
	case CheckUsername:
		return "username"
	default:
		return "credentials"
	}
}

func (s *Server) noteClientAuthFailure(reason string) {
	if s.m != nil && s.m.ClientAuthFailures != nil {
		s.m.ClientAuthFailures.WithLabelValues(reason).Inc()
	}
}

// looksDerived reports whether a digest username has the form of an
// expiring client name (<base>.<expiryUnix>).
func looksDerived(username string) bool {
	_, _, ok := auth.SplitDerived(username)
	return ok
}
