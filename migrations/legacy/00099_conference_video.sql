-- +goose Up
-- Conference-room video (clients-webrtc-mobile-video S-45). Additive.
--
-- conference_rooms.video: the room forwards the active speaker's video to the
-- participants that bring video (default on); off, every video line a
-- participant offers is answered with port 0 and the room is audio only.

ALTER TABLE conference_rooms ADD COLUMN video BOOLEAN NOT NULL DEFAULT TRUE;

-- +goose Down
ALTER TABLE conference_rooms DROP COLUMN video;
