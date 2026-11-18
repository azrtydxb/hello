-- +goose Up
-- Grandstream WP side key (phone-feature-parity S-12): a phone group picks
-- what its WP phones' side key does (PTT/paging, the alarm call or
-- nothing), and a phone may override its group (NULL inherits). PTT/paging
-- is the phones' own default. Additive, so a rolling upgrade works.
ALTER TABLE phone_groups
    ADD COLUMN side_key TEXT NOT NULL DEFAULT 'ptt' CHECK (side_key IN ('ptt', 'alarm', 'none'));
ALTER TABLE phones
    ADD COLUMN side_key TEXT CHECK (side_key IN ('ptt', 'alarm', 'none'));

-- +goose Down
ALTER TABLE phones DROP COLUMN side_key;
ALTER TABLE phone_groups DROP COLUMN side_key;
