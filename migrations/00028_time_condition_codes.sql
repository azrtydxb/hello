-- +goose Up
-- Feature codes for time conditions: the action time_condition routes a
-- dialled code to a time condition (the argument names it) and tc_override
-- is the day/night toggle *28<n>. This is separate from 00027 because
-- 00026 (call waiting) re-creates the feature_codes CHECK from its own list;
-- this migration widens it again with every action added meanwhile, the IVR
-- action of 00018 included. Additive, so a rolling upgrade works.
ALTER TABLE feature_codes DROP CONSTRAINT feature_codes_action_check;
ALTER TABLE feature_codes ADD CONSTRAINT feature_codes_action_check
  CHECK (action IN ('forward_always','forward_busy','forward_no_answer','dnd_on','dnd_off',
                    'voicemail','blind_transfer','attended_transfer','announcement','page','intercom',
                    'call_waiting_on','call_waiting_off','ivr','time_condition','tc_override'));

-- Existing installs get the default toggle code; a fresh install (empty
-- table) gets it from the control process's defaults instead, so the
-- "seed only an empty table" rule still holds. A site that already uses *28
-- keeps its code.
INSERT INTO feature_codes (code, action, argument)
SELECT '*28', 'tc_override', 'toggle' WHERE EXISTS (SELECT 1 FROM feature_codes)
ON CONFLICT (code) DO NOTHING;

-- +goose Down
DELETE FROM feature_codes WHERE action IN ('time_condition', 'tc_override');
ALTER TABLE feature_codes DROP CONSTRAINT feature_codes_action_check;
ALTER TABLE feature_codes ADD CONSTRAINT feature_codes_action_check
  CHECK (action IN ('forward_always','forward_busy','forward_no_answer','dnd_on','dnd_off',
                    'voicemail','blind_transfer','attended_transfer','announcement','page','intercom',
                    'call_waiting_on','call_waiting_off','ivr'));
