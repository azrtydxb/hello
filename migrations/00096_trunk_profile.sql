-- +goose Up
-- Trunk profiles (clients-webrtc-mobile-video S-49, docs/teams.md): "generic"
-- is every trunk so far; "sbc-peer" is a trunk to a session border controller
-- the PBX peers with, for example the certified SBC in front of Microsoft
-- Teams Direct Routing. The profile is a set of defaults and limits the API
-- enforces when the trunk is written; hello-sip never reads it. Additive, so
-- a rolling upgrade works.
ALTER TABLE trunks
    ADD COLUMN profile TEXT NOT NULL DEFAULT 'generic' CHECK (profile IN ('generic', 'sbc-peer'));

-- +goose Down
ALTER TABLE trunks DROP COLUMN profile;
