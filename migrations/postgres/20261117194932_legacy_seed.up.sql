-- Hand-written migration. Write the SQL below; end each statement with a semicolon.
-- Directives (-- momo:no-transaction, -- momo:split, -- momo:destructive) go in
-- this leading comment block, before the first statement.
-- If the SQL changes the schema, change the models too and run momo migrate diff.
-- After editing this file, run: momo migrate hash

-- What the frozen goose migrations (migrations/legacy) built beyond init: the
-- deferrable unique constraints below, the rows they inserted, and the
-- position of every sequence, from pg_dump --data-only of a database goose
-- built (CI job momo-bootstrap). A database taken over from goose records
-- this migration as applied without running it (internal/migrate).

-- The deferrable unique constraints the goose migrations made (00003, 00004,
-- 00040): a reorder swaps positions inside one transaction. momo v0.16.0
-- does not model deferrability, so init created them plain; drift and
-- baseline do not compare it. TestMigrateInitParity compares the catalog.
ALTER TABLE outbound_routes DROP CONSTRAINT outbound_routes_position,
  ADD CONSTRAINT outbound_routes_position UNIQUE (position) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE inbound_routes DROP CONSTRAINT inbound_routes_position,
  ADD CONSTRAINT inbound_routes_position UNIQUE (position) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE ring_group_members DROP CONSTRAINT ring_group_members_group_id_position_key,
  ADD CONSTRAINT ring_group_members_group_id_position_key UNIQUE (group_id, position) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE queue_members DROP CONSTRAINT queue_members_queue_id_position_key,
  ADD CONSTRAINT queue_members_queue_id_position_key UNIQUE (queue_id, position) DEFERRABLE INITIALLY DEFERRED;


INSERT INTO ring_tags (name, builtin, bellcore_slot, description, created_at, updated_at) VALUES ('internal', true, 1, 'A call from another extension', now(), now());
INSERT INTO ring_tags (name, builtin, bellcore_slot, description, created_at, updated_at) VALUES ('external', true, 2, 'A call that entered on a trunk', now(), now());
INSERT INTO ring_tags (name, builtin, bellcore_slot, description, created_at, updated_at) VALUES ('ringgroup', true, 3, 'A call through a ring group', now(), now());
INSERT INTO ring_tags (name, builtin, bellcore_slot, description, created_at, updated_at) VALUES ('page', true, 1, 'A page or intercom call', now(), now());
INSERT INTO ring_tags (name, builtin, bellcore_slot, description, created_at, updated_at) VALUES ('doorbell', true, 4, 'A door station call', now(), now());
INSERT INTO ring_tags (name, builtin, bellcore_slot, description, created_at, updated_at) VALUES ('alarm', true, 5, 'An alarm call', now(), now());
INSERT INTO ring_tags (name, builtin, bellcore_slot, description, created_at, updated_at) VALUES ('emergency-callback', true, 1, 'A callback to an extension that just called an emergency number', now(), now());

INSERT INTO auth_settings (id, require_2fa_roles, sso_required) VALUES (true, '{}', false);

INSERT INTO multicast_channels (id, group_id, "position", label, address, port, codec, role, priority, enabled, kind) VALUES (1, NULL, NULL, 'PTT', '224.0.1.116', 5001, 'PCMU', NULL, 1, true, 'ptt');

INSERT INTO emergency_settings (id, notify_group, updated_at) VALUES (1, '', now());

INSERT INTO message_settings (id, enabled, ttl_hours, retention_days, rate_per_minute, max_queued_per_extension) VALUES (true, true, 24, 30, 30, 100);

INSERT INTO messaging_settings (id, retention_sms_days, retention_chat_days, reopen_days, email_notify, first_contact, first_contact_user_hour, first_contact_address_day, ai_enabled, ai_auto_enabled, ai_business_name, ai_intents, ai_sensitive_topics, retention_media_days, sla_first_response_seconds, sla_resolution_seconds) VALUES (true, 365, 90, 7, true, 'off', 20, 200, true, true, '', '{question,sales,support,billing,appointment,complaint}', '{medical,legal,emergency,self-harm,harassment,"payment dispute","personal data"}', 90, 900, 86400);

INSERT INTO originate_settings (id, destinations, country_prefixes, auto_answer_allowed) VALUES (true, 'inherit', '{}', false);

INSERT INTO park_lots (id, name, slot_start, slot_end, return_secs, voicemail_secs, enabled, created_at, updated_at) VALUES (1, 'default', 701, 720, 120, 60, true, now(), now());

INSERT INTO phonebooks (id, kind, name, owner_user_id, version, created_at, updated_at) VALUES (1, 'company', 'Company', NULL, 1, now(), now());
INSERT INTO phonebooks (id, kind, name, owner_user_id, version, created_at, updated_at) VALUES (2, 'extensions', 'Extensions', NULL, 1, now(), now());

INSERT INTO prov_firmware_minimums (vendor, model_glob, min_version, recommended) VALUES ('grandstream', 'WP822*', '1.0.11.22', '1.0.11.78');
INSERT INTO prov_firmware_minimums (vendor, model_glob, min_version, recommended) VALUES ('grandstream', 'WP8?6*', '', '1.0.3.39');

INSERT INTO recording_settings (id, video_retention_days, updated_at) VALUES (1, 30, now());

INSERT INTO schema_info (id, config_revision, created_at, prov_drift_checked_at) VALUES (true, 0, now(), NULL);

INSERT INTO tts_settings (id, api_key_enc, key_hint, model, voice, updated_at) VALUES (1, NULL, '', 'elevenlabs/eleven-multilingual-v2', '', now());

SELECT pg_catalog.setval('ai_agent_runs_id_seq', 1, false);

SELECT pg_catalog.setval('ai_message_runs_id_seq', 1, false);

SELECT pg_catalog.setval('ai_messages_id_seq', 1, false);

SELECT pg_catalog.setval('announcements_id_seq', 1, false);

SELECT pg_catalog.setval('api_tokens_id_seq', 1, false);

SELECT pg_catalog.setval('audit_events_id_seq', 1, false);

SELECT pg_catalog.setval('backup_runs_id_seq', 1, false);

SELECT pg_catalog.setval('boss_links_id_seq', 1, false);

SELECT pg_catalog.setval('calendar_entries_id_seq', 1, false);

SELECT pg_catalog.setval('calendars_id_seq', 1, false);

SELECT pg_catalog.setval('call_blocklist_id_seq', 1, false);

SELECT pg_catalog.setval('cdrs_id_seq', 1, false);

SELECT pg_catalog.setval('chat_blocks_id_seq', 1, false);

SELECT pg_catalog.setval('chat_events_id_seq', 1, false);

SELECT pg_catalog.setval('chat_sites_id_seq', 1, false);

SELECT pg_catalog.setval('chat_visitors_id_seq', 1, false);

SELECT pg_catalog.setval('conference_rooms_id_seq', 1, false);

SELECT pg_catalog.setval('conversation_assignments_id_seq', 1, false);

SELECT pg_catalog.setval('conversation_events_id_seq', 1, false);

SELECT pg_catalog.setval('conversation_messages_id_seq', 1, false);

SELECT pg_catalog.setval('conversations_id_seq', 1, false);

SELECT pg_catalog.setval('crm_call_logs_id_seq', 1, false);

SELECT pg_catalog.setval('crm_connections_id_seq', 1, false);

SELECT pg_catalog.setval('crm_connectors_id_seq', 1, false);

SELECT pg_catalog.setval('devices_id_seq', 1, false);

SELECT pg_catalog.setval('directory_connectors_id_seq', 1, false);

SELECT pg_catalog.setval('directory_sync_changes_id_seq', 1, false);

SELECT pg_catalog.setval('directory_sync_runs_id_seq', 1, false);

SELECT pg_catalog.setval('door_webhooks_id_seq', 1, false);

SELECT pg_catalog.setval('dot1x_profiles_id_seq', 1, false);

SELECT pg_catalog.setval('extensions_id_seq', 1, false);

SELECT pg_catalog.setval('fax_destinations_id_seq', 1, false);

SELECT pg_catalog.setval('faxes_id_seq', 1, false);

SELECT pg_catalog.setval('flow_canvases_id_seq', 1, false);

SELECT pg_catalog.setval('inbound_routes_id_seq', 1, false);

SELECT pg_catalog.setval('ivr_menus_id_seq', 1, false);

SELECT pg_catalog.setval('locations_id_seq', 1, false);

SELECT pg_catalog.setval('media_links_id_seq', 1, false);

SELECT pg_catalog.setval('message_media_id_seq', 1, false);

SELECT pg_catalog.setval('messages_id_seq', 1, false);

SELECT pg_catalog.setval('moh_classes_id_seq', 1, false);

SELECT pg_catalog.setval('msg_addresses_id_seq', 1, false);

SELECT pg_catalog.setval('msg_channels_id_seq', 1, false);

SELECT pg_catalog.setval('msg_faq_id_seq', 1, false);

SELECT pg_catalog.setval('msg_provider_redactions_id_seq', 1, false);

SELECT pg_catalog.setval('multicast_channels_id_seq', 1, true);

SELECT pg_catalog.setval('oauth_client_secrets_id_seq', 1, false);

SELECT pg_catalog.setval('oauth_grants_id_seq', 1, false);

SELECT pg_catalog.setval('outbound_routes_id_seq', 1, false);

SELECT pg_catalog.setval('paging_groups_id_seq', 1, false);

SELECT pg_catalog.setval('park_lots_id_seq', 1, true);

SELECT pg_catalog.setval('phone_groups_id_seq', 1, false);

SELECT pg_catalog.setval('phone_pushes_id_seq', 1, false);

SELECT pg_catalog.setval('phonebook_entries_id_seq', 1, false);

SELECT pg_catalog.setval('phonebooks_id_seq', 2, true);

SELECT pg_catalog.setval('phones_id_seq', 1, false);

SELECT pg_catalog.setval('pickup_groups_id_seq', 1, false);

SELECT pg_catalog.setval('prov_fetches_id_seq', 1, false);

SELECT pg_catalog.setval('prov_firmware_id_seq', 1, false);

SELECT pg_catalog.setval('prov_redirect_jobs_seq', 1, false);

SELECT pg_catalog.setval('prov_setting_overrides_id_seq', 1, false);

SELECT pg_catalog.setval('prov_templates_id_seq', 1, false);

SELECT pg_catalog.setval('queue_callbacks_id_seq', 1, false);

SELECT pg_catalog.setval('queues_id_seq', 1, false);

SELECT pg_catalog.setval('recording_links_id_seq', 1, false);

SELECT pg_catalog.setval('recording_streams_id_seq', 1, false);

SELECT pg_catalog.setval('recordings_id_seq', 1, false);

SELECT pg_catalog.setval('ring_groups_id_seq', 1, false);

SELECT pg_catalog.setval('ring_map_id_seq', 1, false);

SELECT pg_catalog.setval('ring_rules_id_seq', 1, false);

SELECT pg_catalog.setval('ringtones_id_seq', 1, false);

SELECT pg_catalog.setval('scep_ca_id_seq', 1, false);

SELECT pg_catalog.setval('scep_certificates_id_seq', 1, false);

SELECT pg_catalog.setval('schedules_id_seq', 1, false);

SELECT pg_catalog.setval('scim_tokens_id_seq', 1, false);

SELECT pg_catalog.setval('slack_destinations_id_seq', 1, false);

SELECT pg_catalog.setval('slack_posts_id_seq', 1, false);

SELECT pg_catalog.setval('sms_opt_outs_id_seq', 1, false);

SELECT pg_catalog.setval('time_conditions_id_seq', 1, false);

SELECT pg_catalog.setval('trunk_destinations_id_seq', 1, false);

SELECT pg_catalog.setval('trunks_id_seq', 1, false);

SELECT pg_catalog.setval('user_identities_id_seq', 1, false);

SELECT pg_catalog.setval('users_id_seq', 1, false);

SELECT pg_catalog.setval('voicemail_boxes_id_seq', 1, false);

SELECT pg_catalog.setval('voicemail_groups_id_seq', 1, false);

SELECT pg_catalog.setval('voicemail_messages_id_seq', 1, false);

SELECT pg_catalog.setval('voicemail_notifications_id_seq', 1, false);

SELECT pg_catalog.setval('webhook_attempts_id_seq', 1, false);

SELECT pg_catalog.setval('webhook_deliveries_id_seq', 1, false);

SELECT pg_catalog.setval('webhook_endpoints_id_seq', 1, false);

SELECT pg_catalog.setval('wifi_assignments_id_seq', 1, false);

SELECT pg_catalog.setval('wifi_profiles_id_seq', 1, false);

