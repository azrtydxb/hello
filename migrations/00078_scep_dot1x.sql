-- +goose Up
-- SCEP and 802.1X (spec phone-feature-parity S-25, delivery item 37).
--
-- Hello runs a SCEP server (RFC 8894) on the provisioning listener that
-- issues device certificates from the Hello device CA. The CA's private key
-- is sealed under HELLO_SECRET_KEY (additional data "scep-ca:<id>"); only one
-- CA is active at a time and a replaced one stays for the CRL of what it
-- issued. Every phone gets a one-time challenge password, sealed
-- ("scep-challenge:<phone id>"), delivered in its configuration and consumed
-- by the enrollment it authorises. Issued certificates are recorded so they
-- can be listed and revoked; a revoked one is on the CRL. Removing a phone
-- revokes its certificates.
--
-- An 802.1X profile is a reusable EAP identity (method, identity with {mac}
-- placeholders, sealed password, the RADIUS server's CA), referenced by a
-- phone group for the wired port and, later, by Wi-Fi profiles (S-23).
-- Additive, so a rolling upgrade works.
CREATE TABLE scep_ca (
    id          BIGSERIAL   PRIMARY KEY,
    cert_pem    TEXT        NOT NULL CHECK (cert_pem LIKE '-----BEGIN CERTIFICATE-----%'),
    key_enc     BYTEA       NOT NULL,
    source      TEXT        NOT NULL CHECK (source IN ('generated', 'imported')),
    active      BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX scep_ca_one_active ON scep_ca (active) WHERE active;

CREATE TABLE scep_challenges (
    phone_id      BIGINT      PRIMARY KEY REFERENCES phones (id) ON DELETE CASCADE,
    challenge_enc BYTEA       NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE scep_certificates (
    id            BIGSERIAL   PRIMARY KEY,
    ca_id         BIGINT      NOT NULL REFERENCES scep_ca (id) ON DELETE RESTRICT,
    phone_id      BIGINT      REFERENCES phones (id) ON DELETE SET NULL,
    mac           TEXT        NOT NULL CHECK (mac ~ '^[0-9a-f]{12}$'),
    serial        TEXT        NOT NULL UNIQUE CHECK (serial ~ '^[0-9a-f]{1,40}$'),
    subject       TEXT        NOT NULL,
    key_sha256    TEXT        NOT NULL CHECK (key_sha256 ~ '^[0-9a-f]{64}$'),
    cert_pem      TEXT        NOT NULL,
    kind          TEXT        NOT NULL CHECK (kind IN ('enrol', 'renewal')),
    not_before    TIMESTAMPTZ NOT NULL,
    not_after     TIMESTAMPTZ NOT NULL,
    issued_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at    TIMESTAMPTZ,
    revoke_reason TEXT        CHECK (revoke_reason IN ('unspecified', 'key_compromise', 'superseded', 'cessation_of_operation', 'phone_removed')),
    CHECK ((revoked_at IS NULL) = (revoke_reason IS NULL))
);
CREATE INDEX scep_certificates_phone ON scep_certificates (phone_id, issued_at DESC);
CREATE INDEX scep_certificates_revoked ON scep_certificates (ca_id) WHERE revoked_at IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION scep_revoke_removed_phone() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE scep_certificates SET revoked_at = now(), revoke_reason = 'phone_removed'
    WHERE phone_id = OLD.id AND revoked_at IS NULL;
    RETURN OLD;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER phones_scep_revoke BEFORE DELETE ON phones
    FOR EACH ROW EXECUTE FUNCTION scep_revoke_removed_phone();

CREATE TABLE dot1x_profiles (
    id                 BIGSERIAL   PRIMARY KEY,
    name               TEXT        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    method             TEXT        NOT NULL CHECK (method IN ('eap-tls', 'peap', 'eap-md5')),
    identity           TEXT        NOT NULL CHECK (char_length(identity) BETWEEN 1 AND 64),
    anonymous_identity TEXT        NOT NULL DEFAULT '' CHECK (char_length(anonymous_identity) <= 64),
    password_enc       BYTEA,
    ca_cert_pem        TEXT        NOT NULL DEFAULT '' CHECK (ca_cert_pem = '' OR ca_cert_pem LIKE '-----BEGIN CERTIFICATE-----%'),
    server_domain      TEXT        NOT NULL DEFAULT '' CHECK (char_length(server_domain) <= 253),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX dot1x_profiles_name ON dot1x_profiles (lower(name));

-- SCEP is off by default and switched on per phone group; the wired 802.1X
-- profile is per phone group too.
ALTER TABLE phone_groups
    ADD COLUMN scep_enabled       BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN scep_validity_days INTEGER NOT NULL DEFAULT 365 CHECK (scep_validity_days BETWEEN 30 AND 1825),
    ADD COLUMN wired_dot1x_id     BIGINT  REFERENCES dot1x_profiles (id) ON DELETE RESTRICT;

-- The SCEP endpoint is audited like every other provisioning request.
ALTER TABLE prov_fetches DROP CONSTRAINT prov_fetches_kind_check;
ALTER TABLE prov_fetches ADD CONSTRAINT prov_fetches_kind_check
    CHECK (kind IN ('common','device','master','firmware','ca','boot','upload','phonebook','scep','ringtone','other'));
ALTER TABLE prov_fetches DROP CONSTRAINT prov_fetches_result_check;
ALTER TABLE prov_fetches ADD CONSTRAINT prov_fetches_result_check
    CHECK (result IN ('served','not_modified','unknown_token','mac_mismatch',
                      'not_allowlisted','plain_http','rate_limited','no_template',
                      'render_error','boot_served','boot_handoff','boot_reclaim',
                      'boot_denied','upload_discarded','not_found','unavailable',
                      'scep_issued','scep_refused'));

-- +goose Down
DELETE FROM prov_fetches WHERE kind = 'scep' OR result IN ('scep_issued', 'scep_refused');
ALTER TABLE prov_fetches DROP CONSTRAINT prov_fetches_result_check;
ALTER TABLE prov_fetches ADD CONSTRAINT prov_fetches_result_check
    CHECK (result IN ('served','not_modified','unknown_token','mac_mismatch',
                      'not_allowlisted','plain_http','rate_limited','no_template',
                      'render_error','boot_served','boot_handoff','boot_reclaim',
                      'boot_denied','upload_discarded','not_found','unavailable'));
ALTER TABLE prov_fetches DROP CONSTRAINT prov_fetches_kind_check;
ALTER TABLE prov_fetches ADD CONSTRAINT prov_fetches_kind_check
    CHECK (kind IN ('common','device','master','firmware','ca','boot','upload','phonebook','ringtone','other'));
ALTER TABLE phone_groups DROP COLUMN wired_dot1x_id, DROP COLUMN scep_validity_days, DROP COLUMN scep_enabled;
DROP TABLE dot1x_profiles;
DROP TRIGGER phones_scep_revoke ON phones;
DROP FUNCTION scep_revoke_removed_phone();
DROP TABLE scep_certificates;
DROP TABLE scep_challenges;
DROP TABLE scep_ca;
