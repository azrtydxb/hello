#!/bin/sh
# Hand checks of the lab's HA edges (plan Task 5), with timings:
#
#   deploy/verify/failover.sh sip [node]   stop a hello-sip node (default
#       hello-sip-2), time until Kamailio's dispatcher marks it inactive, run
#       deploy/verify through the survivor, then start the node again and
#       time its return to active.
#   deploy/verify/failover.sh valkey       kill the Valkey primary, time the
#       Sentinel promotion, then restart the old primary and show it rejoin
#       as a replica.
#
# HELLO_LAB_PROJECT selects the compose project (default hello).
set -eu

cd "$(dirname "$0")/../.."
project=${HELLO_LAB_PROJECT:-hello}
dc() { docker compose -p "$project" -f deploy/docker-compose/compose.yaml "$@"; }

# dispatcher prints "uri=flags" for each destination (AP active, IP
# inactive, TP trying; P is probing).
dispatcher() {
	dc exec -T kamailio kamcmd dispatcher.list |
		awk '/URI:/ {u = $2} /FLAGS:/ {printf "%s=%s ", u, $2} END {print ""}'
}

primary() {
	dc exec -T sentinel-1 valkey-cli -p 26379 sentinel get-master-addr-by-name hello | tr '\n' ' '
	echo
}

role() {
	dc exec -T "$1" valkey-cli info replication | tr -d '\r' | grep -E '^(role|master_host|master_link_status|connected_slaves)'
}

since() { echo $(($(date +%s) - $1)); }

case "${1:-}" in
sip)
	node=${2:-hello-sip-2}
	echo "dispatcher before: $(dispatcher)"
	t0=$(date +%s)
	dc stop -t 0 "$node" >/dev/null 2>&1
	echo "+0s stopped $node"
	until dispatcher | grep -q "sip:$node:5060=I"; do
		[ "$(since "$t0")" -gt 60 ] && {
			echo "FAIL: $node still active after 60s"
			exit 1
		}
		sleep 1
	done
	echo "+$(since "$t0")s inactive: $(dispatcher)"
	go run ./deploy/verify -phones 4
	echo "+$(since "$t0")s REGISTER and calls through Kamailio OK with $node down"
	t1=$(date +%s)
	dc start "$node" >/dev/null 2>&1
	until dispatcher | grep -q "sip:$node:5060=A"; do
		[ "$(since "$t1")" -gt 90 ] && {
			echo "FAIL: $node not active 90s after start"
			exit 1
		}
		sleep 1
	done
	echo "+$(since "$t1")s after start, active again: $(dispatcher)"
	;;
valkey)
	before=$(primary)
	echo "primary before: $before"
	old=${before%% *}
	t0=$(date +%s)
	dc kill "$old" >/dev/null 2>&1
	echo "+0s killed $old"
	until p=$(primary) && [ "${p%% *}" != "$old" ]; do
		[ "$(since "$t0")" -gt 60 ] && {
			echo "FAIL: no promotion after 60s"
			exit 1
		}
		sleep 1
	done
	new=${p%% *}
	echo "+$(since "$t0")s promoted: $p"
	role "$new"
	dc start "$old" >/dev/null 2>&1
	t1=$(date +%s)
	until role "$old" 2>/dev/null | grep -q 'master_link_status:up'; do
		[ "$(since "$t1")" -gt 60 ] && {
			echo "FAIL: $old did not rejoin"
			exit 1
		}
		sleep 1
	done
	echo "+$(since "$t1")s after restart, $old rejoined:"
	role "$old"
	;;
*)
	echo "usage: $0 sip [node] | valkey" >&2
	exit 2
	;;
esac
