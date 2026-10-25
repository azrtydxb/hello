-- +goose Up
-- The "ivr" feature-code action (a code that runs an IVR menu). It belongs to
-- the IVR (00018) but lives here, after 00026 (call waiting) rewrote the
-- action constraint, so the constraint keeps every action. Additive, so a
-- rolling upgrade works.
ALTER TABLE feature_codes DROP CONSTRAINT feature_codes_action_check;
ALTER TABLE feature_codes ADD CONSTRAINT feature_codes_action_check
  CHECK (action IN ('forward_always','forward_busy','forward_no_answer','dnd_on','dnd_off',
                    'voicemail','blind_transfer','attended_transfer','announcement','page','intercom',
                    'call_waiting_on','call_waiting_off','ivr'));

-- +goose Down
DELETE FROM feature_codes WHERE action = 'ivr';
ALTER TABLE feature_codes DROP CONSTRAINT feature_codes_action_check;
ALTER TABLE feature_codes ADD CONSTRAINT feature_codes_action_check
  CHECK (action IN ('forward_always','forward_busy','forward_no_answer','dnd_on','dnd_off',
                    'voicemail','blind_transfer','attended_transfer','announcement','page','intercom',
                    'call_waiting_on','call_waiting_off'));
