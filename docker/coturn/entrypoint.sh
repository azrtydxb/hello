#!/bin/sh
# Writes turnserver.conf from the environment and runs coturn in the
# foreground. Settings (all but the secret are plain values):
#
#   TURN_SECRET          the TURN REST API shared secret (static-auth-secret);
#                        the same value as HELLO_TURN_SECRET on hello-control
#   TURN_REALM           the realm (the SIP domain)
#   TURN_EXTERNAL_IP     the address clients reach this server at (also the
#                        relay address); required
#   TURN_PORT            3478        plain STUN/TURN, UDP and TCP
#   TURN_TLS_PORT        5349        TURN over TLS, used when TURN_TLS_CERT is set
#   TURN_TLS_CERT/KEY    certificate and key files; without them, no TLS
#   TURN_MIN_PORT/MAX    relay port range (49152-49999)
#   TURN_ALLOWED_PEERS   comma separated IPs or ranges (a-b) clients may relay
#                        to: the hello-sip media addresses. Every private,
#                        loopback and link-local range is denied except these,
#                        so TURN cannot be used to reach the rest of the LAN.
#   TURN_USER_QUOTA      allocations per credential (12)
#   TURN_TOTAL_QUOTA     allocations in total (1200)
#   TURN_PROMETHEUS      "true" adds the prometheus exporter (port 9641)
#   TURN_CONF            where to write the file (/tmp/turnserver.conf)
#   TURN_BIN             the binary (turnserver); tests replace it
set -eu

: "${TURN_SECRET:?TURN_SECRET is required}"
: "${TURN_REALM:?TURN_REALM is required}"
: "${TURN_EXTERNAL_IP:?TURN_EXTERNAL_IP is required}"
conf="${TURN_CONF:-/tmp/turnserver.conf}"

# The file holds the secret: owner only.
umask 077
{
  echo "listening-port=${TURN_PORT:-3478}"
  echo "listening-ip=0.0.0.0"
  echo "external-ip=${TURN_EXTERNAL_IP}"
  echo "min-port=${TURN_MIN_PORT:-49152}"
  echo "max-port=${TURN_MAX_PORT:-49999}"
  echo "realm=${TURN_REALM}"
  # TURN REST API: credentials are "<expiry>:<name>" with an HMAC of that
  # under the shared secret; coturn keeps no user database.
  echo "use-auth-secret"
  echo "static-auth-secret=${TURN_SECRET}"
  echo "fingerprint"
  echo "stale-nonce=600"
  echo "no-tcp-relay"
  echo "no-multicast-peers"
  echo "no-cli"
  echo "no-software-attribute"
  echo "user-quota=${TURN_USER_QUOTA:-12}"
  echo "total-quota=${TURN_TOTAL_QUOTA:-1200}"
  # A client reaches only the allowed peers. allowed-peer-ip overrides
  # denied-peer-ip for the addresses it names.
  for r in 0.0.0.0-0.255.255.255 10.0.0.0-10.255.255.255 100.64.0.0-100.127.255.255 \
    127.0.0.0-127.255.255.255 169.254.0.0-169.254.255.255 172.16.0.0-172.31.255.255 \
    192.0.0.0-192.0.0.255 192.168.0.0-192.168.255.255 198.18.0.0-198.19.255.255 \
    224.0.0.0-255.255.255.255 ::1 fc00::-fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff \
    fe80::-febf:ffff:ffff:ffff:ffff:ffff:ffff:ffff; do
    echo "denied-peer-ip=${r}"
  done
  old_ifs=$IFS
  IFS=,
  for p in ${TURN_ALLOWED_PEERS:-}; do
    [ -n "$p" ] && echo "allowed-peer-ip=${p}"
  done
  IFS=$old_ifs
  if [ -n "${TURN_TLS_CERT:-}" ]; then
    echo "tls-listening-port=${TURN_TLS_PORT:-5349}"
    echo "cert=${TURN_TLS_CERT}"
    echo "pkey=${TURN_TLS_KEY:?TURN_TLS_KEY is required with TURN_TLS_CERT}"
    echo "no-tlsv1"
    echo "no-tlsv1_1"
    echo "no-dtls"
  else
    echo "no-tls"
    echo "no-dtls"
  fi
  if [ "${TURN_PROMETHEUS:-}" = "true" ]; then
    echo "prometheus"
  fi
  echo "log-file=stdout"
  echo "simple-log"
} >"$conf"

exec "${TURN_BIN:-turnserver}" -c "$conf"
