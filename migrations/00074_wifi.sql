-- +goose Up
-- Wi-Fi profiles and roaming for Wi-Fi handsets (spec phone-feature-parity
-- S-23 and S-24). A profile is a named network: SSID, security, a sealed
-- pre-shared key or 802.1X identity and sealed password, priority, hidden
-- flag and a "verified on a test phone" mark. Profiles are assigned to phone
-- groups and to single phones; provisioning writes them only when the
-- phone's set holds a verified profile. psk_enc and password_enc are sealed
-- with the store's secret box (internal/secret), context wifi-psk:<id> and
-- wifi-password:<id>. A profile in use cannot be deleted. wifi_radio is a
-- phone group's roaming and radio settings; a NULL column is not written to
-- the phones. Additive, so a rolling upgrade works.
CREATE TABLE wifi_profiles (
    id                 BIGSERIAL   PRIMARY KEY,
    name               TEXT        NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    ssid               TEXT        NOT NULL CHECK (octet_length(ssid) BETWEEN 1 AND 32),
    security           TEXT        NOT NULL CHECK (security IN ('open', 'wpa2-psk', 'wpa3-sae', 'owe', 'wpa2-enterprise', 'wpa3-enterprise')),
    psk_enc            BYTEA,
    eap_method         TEXT        NOT NULL DEFAULT '' CHECK (eap_method IN ('', 'peap')),
    identity           TEXT        NOT NULL DEFAULT '' CHECK (length(identity) <= 128),
    anonymous_identity TEXT        NOT NULL DEFAULT '' CHECK (length(anonymous_identity) <= 128),
    password_enc       BYTEA,
    ca_cert            TEXT        NOT NULL DEFAULT 'system' CHECK (ca_cert IN ('system', 'tofu', 'skip')),
    priority           INT         NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 10),
    hidden             BOOLEAN     NOT NULL DEFAULT FALSE,
    prefer_account     INT         NOT NULL DEFAULT 0 CHECK (prefer_account BETWEEN 0 AND 3),
    verified           BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX wifi_profiles_name ON wifi_profiles (lower(name));

CREATE TABLE wifi_assignments (
    id         BIGSERIAL PRIMARY KEY,
    profile_id BIGINT    NOT NULL REFERENCES wifi_profiles (id) ON DELETE RESTRICT,
    group_id   BIGINT    REFERENCES phone_groups (id) ON DELETE CASCADE,
    phone_id   BIGINT    REFERENCES phones (id) ON DELETE CASCADE,
    CHECK ((group_id IS NULL) <> (phone_id IS NULL))
);
CREATE UNIQUE INDEX wifi_assignments_group ON wifi_assignments (group_id, profile_id) WHERE group_id IS NOT NULL;
CREATE UNIQUE INDEX wifi_assignments_phone ON wifi_assignments (phone_id, profile_id) WHERE phone_id IS NOT NULL;
CREATE INDEX wifi_assignments_profile ON wifi_assignments (profile_id);

CREATE TABLE wifi_radio (
    group_id   BIGINT      PRIMARY KEY REFERENCES phone_groups (id) ON DELETE CASCADE,
    band       TEXT        CHECK (band IN ('both', '2.4', '5')),
    roam_mode  TEXT        CHECK (roam_mode IN ('default', 'boost', 'fast', 'custom')),
    threshold  INT         CHECK (threshold BETWEEN -100 AND -30),
    poor_scan  INT         CHECK (poor_scan BETWEEN 5 AND 300),
    good_scan  INT         CHECK (good_scan BETWEEN 5 AND 600),
    gain       INT         CHECK (gain BETWEEN 4 AND 20),
    dot11r     BOOLEAN,
    dot11v     BOOLEAN,
    prompt     BOOLEAN,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE wifi_radio;
DROP TABLE wifi_assignments;
DROP TABLE wifi_profiles;
