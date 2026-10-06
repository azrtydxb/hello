#!/bin/sh
# Fail the container at startup when nginx's runtime resolver cannot resolve
# the hello-control upstream, instead of starting and answering every /api/
# call with 502 forever.
#
# nginx resolves a variable proxy_pass through its `resolver` directive, which
# applies no resolv.conf search domains: a Kubernetes short Service name
# ("hello-control") does not resolve there, only the FQDN
# ("hello-control.hello.svc.cluster.local") does. Docker's embedded DNS
# (compose) answers bare service names directly. The query below asks the same
# server for the same absolute name (trailing dot: no search list), so it
# fails exactly when nginx would.
set -eu

url="${HELLO_CONTROL_UPSTREAM:?HELLO_CONTROL_UPSTREAM is not set}"
resolver="${HELLO_DNS_RESOLVER:?HELLO_DNS_RESOLVER is not set}"

host="${url#*://}"
host="${host%%/*}"
case "$host" in
\[*) exit 0 ;; # IPv6 literal
esac
host="${host%:*}"

# An IPv4 literal needs no resolution.
if echo "$host" | grep -Eq '^[0-9]+(\.[0-9]+){3}$'; then
	exit 0
fi

attempts="${HELLO_UPSTREAM_CHECK_ATTEMPTS:-15}"
i=1
while :; do
	# busybox nslookup prints a "Name:" line only for an answer; NXDOMAIN and
	# timeouts print "** server can't find" / ";; connection timed out".
	out="$(nslookup -type=a "${host}." "$resolver" 2>&1 || true)"
	if echo "$out" | grep -q '^Name:'; then
		echo "hello: upstream ${host} resolves via ${resolver}"
		exit 0
	fi
	if [ "$i" -ge "$attempts" ]; then
		echo "hello: FATAL: HELLO_CONTROL_UPSTREAM host '${host}' does not resolve via resolver ${resolver}." >&2
		echo "$out" >&2
		echo "hello: nginx's resolver applies no search domains; use a fully-qualified name (e.g. hello-control.<namespace>.svc.cluster.local)." >&2
		exit 1
	fi
	i=$((i + 1))
	sleep 2
done
