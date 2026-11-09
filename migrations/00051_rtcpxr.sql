-- +goose Up
-- RTCP-XR collector (spec phone-feature-parity S-36): the voice-quality
-- reports phones send as SIP PUBLISH (Event: vq-rtcpxr, RFC 6035). A report
-- names the phone's own dialog, which is one leg of a B2BUA call, and it can
-- arrive before the call's CDR is written, so reports live in their own table
-- and are matched to the CDR when read: by the CDR's caller-leg Call-ID
-- (sip_call_id) or its callee-leg Call-ID (peer_call_id). Additive, so a
-- rolling upgrade works.

ALTER TABLE cdrs ADD COLUMN peer_call_id TEXT NOT NULL DEFAULT '';
CREATE INDEX cdrs_peer_call_id_idx ON cdrs (peer_call_id) WHERE peer_call_id <> '';

-- One row per (dialog, device): a later report of the same dialog (an
-- interval report, then the call-end one) replaces the earlier, so the
-- table grows with calls, not with report intervals. Metrics are NULL for a
-- value the report did not carry.
CREATE TABLE cdr_rtcpxr (
    call_id      TEXT        NOT NULL CHECK (length(call_id) BETWEEN 1 AND 256),
    device       TEXT        NOT NULL,
    extension    TEXT        NOT NULL DEFAULT '',
    final        BOOLEAN     NOT NULL DEFAULT FALSE,
    mos_lq       REAL        CHECK (mos_lq BETWEEN 0 AND 5),
    mos_cq       REAL        CHECK (mos_cq BETWEEN 0 AND 5),
    jitter_ms    REAL        CHECK (jitter_ms >= 0),
    loss_pct     REAL        CHECK (loss_pct BETWEEN 0 AND 100),
    discard_pct  REAL        CHECK (discard_pct BETWEEN 0 AND 100),
    rtt_ms       REAL        CHECK (rtt_ms >= 0),
    reported_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (call_id, device)
);
CREATE INDEX cdr_rtcpxr_reported_at_idx ON cdr_rtcpxr (reported_at);

-- +goose Down
DROP TABLE cdr_rtcpxr;
DROP INDEX cdrs_peer_call_id_idx;
ALTER TABLE cdrs DROP COLUMN peer_call_id;
