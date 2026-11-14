-- +goose Up
-- One schedule per messaging address (spec messaging-channels M-14, M-28).
-- A chat site kept its own opening hours (chat_sites.hours) next to the
-- weekly schedule of its address (msg_addresses.schedule, which the router's
-- after-hours decision reads), so a site could be "open" for the widget and
-- "closed" for the router at the same moment. The address's schedule is now
-- the only one: a site's hours are read from and written to it.
--
-- The JSON shape of both was the same ({"zone": ..., "days": [{"day", "from",
-- "to"}]}), so a site's hours move over unchanged. A site that set hours wins
-- over a schedule set on its address through the routing page; a site with no
-- hours (always open) keeps whatever its address had.

UPDATE msg_addresses a SET schedule = s.hours
FROM chat_sites s
WHERE s.address_id = a.id AND s.hours <> '{}'::jsonb AND jsonb_array_length(coalesce(s.hours -> 'days', '[]'::jsonb)) > 0;

ALTER TABLE chat_sites DROP COLUMN hours;

-- +goose Down
ALTER TABLE chat_sites ADD COLUMN hours JSONB NOT NULL DEFAULT '{}';
UPDATE chat_sites s SET hours = a.schedule FROM msg_addresses a WHERE a.id = s.address_id;
