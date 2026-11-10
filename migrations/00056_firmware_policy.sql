-- +goose Up
-- Firmware policy (spec phone-feature-parity S-26). A minimum is the floor a
-- vendor's models may be pinned to or provisioned with; recommended is the
-- documented lab version (Hello ships no vendor firmware, the admin uploads
-- the file). The upgrade hour of a pin is the daily window a Grandstream WP
-- fetches it in. Additive: no minimum row and no hour behave as before.
CREATE TABLE prov_firmware_minimums (
    vendor      TEXT NOT NULL CHECK (vendor IN ('yealink','poly','grandstream','snom','fanvil','generic')),
    model_glob  TEXT NOT NULL CHECK (length(model_glob) BETWEEN 1 AND 64),
    min_version TEXT NOT NULL DEFAULT '' CHECK (length(min_version) <= 64),
    recommended TEXT NOT NULL DEFAULT '' CHECK (length(recommended) <= 64),
    PRIMARY KEY (vendor, model_glob)
);

-- WP822 floor 1.0.11.22 and lab build 1.0.11.78; WP8x6 lab build 1.0.3.39.
INSERT INTO prov_firmware_minimums (vendor, model_glob, min_version, recommended) VALUES
    ('grandstream', 'WP822*', '1.0.11.22', '1.0.11.78'),
    ('grandstream', 'WP8?6*', '', '1.0.3.39');

ALTER TABLE prov_firmware_pins
    ADD COLUMN upgrade_hour SMALLINT CHECK (upgrade_hour BETWEEN 0 AND 23);

-- +goose Down
ALTER TABLE prov_firmware_pins DROP COLUMN upgrade_hour;
DROP TABLE prov_firmware_minimums;
