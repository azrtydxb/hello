-- +goose Up
-- DTLS-SRTP and ICE-lite on the anchored media path (spec phone-feature-parity
-- S-16): which key exchange Hello offers a device (SDES keys in the SDP, or a
-- DTLS-SRTP handshake on the media path), whether Hello offers it ICE-lite and
-- provisions its ICE setting, and what the call used, in the CDR. Additive, so
-- a rolling upgrade works: an older hello-sip ignores the new columns.
--
-- The key exchange only matters when Hello is the offerer: an offer from the
-- phone is answered in kind (SDES to SDES, DTLS to DTLS). Both stay limited to
-- a leg whose signalling is TLS, whatever the setting says.
ALTER TABLE devices
    ADD COLUMN srtp_keyex TEXT NOT NULL DEFAULT 'sdes' CHECK (srtp_keyex IN ('sdes', 'dtls')),
    ADD COLUMN media_ice BOOLEAN NOT NULL DEFAULT FALSE;

-- How the call's encrypted legs agreed their keys (none when the call was not
-- encrypted, mixed when one leg used each) and whether a leg ran ICE-lite.
ALTER TABLE cdrs
    ADD COLUMN srtp_keyex TEXT NOT NULL DEFAULT 'none' CHECK (srtp_keyex IN ('none', 'sdes', 'dtls', 'mixed')),
    ADD COLUMN ice BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE cdrs DROP COLUMN ice, DROP COLUMN srtp_keyex;
ALTER TABLE devices DROP COLUMN media_ice, DROP COLUMN srtp_keyex;
