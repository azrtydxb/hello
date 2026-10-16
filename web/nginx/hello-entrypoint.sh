#!/bin/sh
# Resolve HELLO_DNS_RESOLVER before the stock nginx entrypoint renders the
# config template. When it is empty or unset, use the first nameserver in
# /etc/resolv.conf: on Kubernetes that is the cluster DNS Service, whatever
# the cluster's service CIDR. An explicit value is left untouched.
#
# nginx's `resolver` wants IPv6 addresses in brackets; the bracketed form is
# what HELLO_DNS_RESOLVER then holds (30-hello-check-upstream.sh strips them
# for nslookup).
set -eu

if [ -z "${HELLO_DNS_RESOLVER:-}" ]; then
	ns="$(awk '$1 == "nameserver" { print $2; exit }' "${HELLO_RESOLV_CONF:-/etc/resolv.conf}" 2>/dev/null || true)"
	if [ -z "$ns" ]; then
		echo "hello: FATAL: HELLO_DNS_RESOLVER is empty and no nameserver found in /etc/resolv.conf" >&2
		exit 1
	fi
	case "$ns" in
	*:*) ns="[${ns%%%*}]" ;; # IPv6 (drop any %zone): nginx needs brackets
	esac
	HELLO_DNS_RESOLVER="$ns"
	export HELLO_DNS_RESOLVER
	echo "hello: HELLO_DNS_RESOLVER not set; using ${HELLO_DNS_RESOLVER} from /etc/resolv.conf"
fi

exec /docker-entrypoint.sh "$@"
