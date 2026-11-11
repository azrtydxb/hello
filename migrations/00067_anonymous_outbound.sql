-- +goose Up
-- Anonymous outbound calls (phone-feature-parity S-19, RFC 3323/3325).
-- extensions.anonymous_outbound makes every trunk call from the extension
-- anonymous. A trunk's anonymous_policy is "honour" (a device's Privacy
-- request or anonymous From is carried to the carrier) or "always" (every
-- call out of the trunk is anonymous). asserted_identity is the number the
-- trunk asserts for an anonymous call (empty: the call's caller ID) and
-- identity_header is the header it travels in, "pai" (P-Asserted-Identity)
-- or "ppi" (P-Preferred-Identity). Additive, so a rolling upgrade works.
ALTER TABLE extensions ADD COLUMN anonymous_outbound BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE trunks ADD COLUMN anonymous_policy TEXT NOT NULL DEFAULT 'honour'
  CHECK (anonymous_policy IN ('honour', 'always'));
ALTER TABLE trunks ADD COLUMN asserted_identity TEXT NOT NULL DEFAULT '';
ALTER TABLE trunks ADD COLUMN identity_header TEXT NOT NULL DEFAULT 'pai'
  CHECK (identity_header IN ('pai', 'ppi'));

-- +goose Down
ALTER TABLE trunks DROP COLUMN identity_header;
ALTER TABLE trunks DROP COLUMN asserted_identity;
ALTER TABLE trunks DROP COLUMN anonymous_policy;
ALTER TABLE extensions DROP COLUMN anonymous_outbound;
