-- +goose Up
-- Address-book fetches are audited with their own kind (spec phonebooks-ptt,
-- P-4). Owned by hello-control; additive, so a rolling upgrade works: an
-- older hello-control never writes the new kind.
ALTER TABLE prov_fetches DROP CONSTRAINT prov_fetches_kind_check;
ALTER TABLE prov_fetches ADD CONSTRAINT prov_fetches_kind_check
    CHECK (kind IN ('common','device','master','firmware','ca','boot','upload','phonebook','other'));

-- +goose Down
DELETE FROM prov_fetches WHERE kind = 'phonebook';
ALTER TABLE prov_fetches DROP CONSTRAINT prov_fetches_kind_check;
ALTER TABLE prov_fetches ADD CONSTRAINT prov_fetches_kind_check
    CHECK (kind IN ('common','device','master','firmware','ca','boot','upload','other'));
