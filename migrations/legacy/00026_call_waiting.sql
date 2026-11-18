-- +goose Up
-- Call waiting (phone-feature-parity S-18): with the flag on (the default) a
-- call to a busy extension rings its devices and the phone shows call
-- waiting; with it off the caller gets the extension's forward-busy target or
-- 486 Busy Here. Additive, so a rolling upgrade works.
ALTER TABLE extensions ADD COLUMN call_waiting BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE feature_codes DROP CONSTRAINT feature_codes_action_check;
ALTER TABLE feature_codes ADD CONSTRAINT feature_codes_action_check
  CHECK (action IN ('forward_always','forward_busy','forward_no_answer','dnd_on','dnd_off',
                    'voicemail','blind_transfer','attended_transfer','announcement','page','intercom',
                    'call_waiting_on','call_waiting_off'));

-- Existing installs get the default toggle codes; a fresh install (empty
-- table) gets them from the control process's defaults instead, so the
-- "seed only an empty table" rule still holds. A site that already uses *70
-- or *71 keeps its code.
INSERT INTO feature_codes (code, action, argument)
SELECT '*71', 'call_waiting_on', '' WHERE EXISTS (SELECT 1 FROM feature_codes)
ON CONFLICT (code) DO NOTHING;
INSERT INTO feature_codes (code, action, argument)
SELECT '*70', 'call_waiting_off', '' WHERE EXISTS (SELECT 1 FROM feature_codes)
ON CONFLICT (code) DO NOTHING;

-- +goose Down
DELETE FROM feature_codes WHERE action IN ('call_waiting_on', 'call_waiting_off');
ALTER TABLE feature_codes DROP CONSTRAINT feature_codes_action_check;
ALTER TABLE feature_codes ADD CONSTRAINT feature_codes_action_check
  CHECK (action IN ('forward_always','forward_busy','forward_no_answer','dnd_on','dnd_off',
                    'voicemail','blind_transfer','attended_transfer','announcement','page','intercom'));
ALTER TABLE extensions DROP COLUMN call_waiting;
