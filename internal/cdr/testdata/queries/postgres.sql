-- name: cdr.insert
WITH c AS (INSERT INTO cdrs (correlation_id, sip_call_id, source, destination,
    start_time, ring_time, answer_time, end_time, duration_ms, billable_ms,
    sip_node, media_mode, final_status, termination_side, failure_reason,
    direction, original_destination, rewritten_destination, route_name, trunk_name, trace,
    rtp_packets, rtp_lost, rtp_jitter_ms, kind, ring_tag, ring_rule, caller_codec, callee_codec, origin, origin_client, peer_call_id, answered_by, srtp, srtp_keyex, ice, video)
VALUES ($1, $2, $3, $4,
    $5, $6, $7, $8, $9, $10,
    $11, $12, $13, $14, $15,
    $16, $17, $18, $19, $20, $21,
    $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34, $35, $36, $37)
ON CONFLICT (correlation_id) DO NOTHING RETURNING id),
p AS (INSERT INTO cdr_page_legs (cdr_id, position, extension, device, result)
SELECT c.id, l.ord, l.leg->>'extension', l.leg->>'device', l.leg->>'result'
FROM c, jsonb_array_elements($38::jsonb) WITH ORDINALITY AS l(leg, ord) RETURNING 1),
f AS (INSERT INTO cdr_legs (cdr_id, position, role, extension, trunk, result, target_kind)
SELECT c.id, l.ord, 'follow_me', l.leg->>'extension', l.leg->>'trunk', l.leg->>'result', l.leg->>'kind'
FROM c, jsonb_array_elements($39::jsonb) WITH ORDINALITY AS l(leg, ord) RETURNING 1)
INSERT INTO cdr_queue_legs (cdr_id, position, event, queue, extension, wait_ms, result, at)
SELECT c.id, l.ord, l.leg->>'event', l.leg->>'queue', l.leg->>'extension', (l.leg->>'waitMs')::bigint,
       l.leg->>'result', (l.leg->>'at')::timestamptz
FROM c, jsonb_array_elements($40::jsonb) WITH ORDINALITY AS l(leg, ord);

-- name: cdr.select_id
SELECT id FROM cdrs WHERE correlation_id = $1;

