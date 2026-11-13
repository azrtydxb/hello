-- +goose Up
-- One 802.1X model for wired ports and Wi-Fi (phone-feature-parity S-23 and
-- S-25). An enterprise Wi-Fi profile (00074) used to carry its own EAP method,
-- identity, sealed password and server certificate check, which duplicated the
-- 802.1X profiles of 00076. It now names an 802.1X profile (dot1x_id); the
-- method, identity, password and CA live there only.
--
-- The old columns (eap_method, identity, anonymous_identity, password_enc,
-- ca_cert) stay, unread, so a rolling upgrade works; a later migration drops
-- them. hello-control moves the settings of existing enterprise profiles into
-- 802.1X profiles at start-up (store.AdoptWifiEAP): the sealed password cannot
-- be re-sealed in SQL. Additive.
ALTER TABLE wifi_profiles
    ADD COLUMN dot1x_id BIGINT REFERENCES dot1x_profiles (id) ON DELETE RESTRICT;
CREATE INDEX wifi_profiles_dot1x ON wifi_profiles (dot1x_id) WHERE dot1x_id IS NOT NULL;

-- +goose Down
DROP INDEX wifi_profiles_dot1x;
ALTER TABLE wifi_profiles DROP COLUMN dot1x_id;
