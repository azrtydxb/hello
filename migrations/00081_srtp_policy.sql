-- +goose Up
-- SRTP with SDES key exchange (spec phone-feature-parity S-15): a media
-- encryption policy per device and per trunk (off, optional, required) and
-- whether a call's legs were encrypted, in the CDR. Additive, so a rolling
-- upgrade works: an older hello-sip ignores the new columns and keeps
-- anchoring plain RTP.
--
-- A device defaults to optional (crypto is offered on its TLS leg and used
-- when the phone answers with it); a trunk defaults to off. Crypto is never
-- offered on a leg whose signalling is not TLS, whatever the policy says.
ALTER TABLE devices
    ADD COLUMN srtp TEXT NOT NULL DEFAULT 'optional' CHECK (srtp IN ('off', 'optional', 'required'));
ALTER TABLE trunks
    ADD COLUMN srtp TEXT NOT NULL DEFAULT 'off' CHECK (srtp IN ('off', 'optional', 'required'));

-- Which legs of the call were SRTP: none, caller, callee or both. Calls whose
-- media was not anchored are none (direct media is not inspected).
ALTER TABLE cdrs
    ADD COLUMN srtp TEXT NOT NULL DEFAULT 'none' CHECK (srtp IN ('none', 'caller', 'callee', 'both'));

-- +goose Down
ALTER TABLE cdrs DROP COLUMN srtp;
ALTER TABLE trunks DROP COLUMN srtp;
ALTER TABLE devices DROP COLUMN srtp;
