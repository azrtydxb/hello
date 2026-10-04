# Kamailio for kw. The official image (ghcr.io/kamailio/kamailio:6.0.8) is
# published for linux/amd64 only — the lab runs it emulated on Apple silicon
# (compose.yaml) — but the kw nodes are arm64, so this builds Kamailio 6.0
# from Debian trixie (native arm64 and amd64) instead. The package set is the
# same 6.0 line; the config (deploy/kamailio/kamailio.cfg) is unchanged.
FROM debian:trixie-slim

# wget is not in the slim base, but the lab's healthchecks (compose.yaml)
# and an operator's readyz probe both use it the way the Hello images do.
RUN apt-get update && apt-get install -y --no-install-recommends kamailio wget \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

# The kw manifest pins runAsUser/runAsGroup 102 (the official image's
# kamailio uid); normalize Debian's dynamically-assigned system user to it.
RUN groupmod -g 102 kamailio 2>/dev/null || groupadd -g 102 kamailio \
    ; usermod -u 102 -g 102 kamailio \
    ; mkdir -p /run/kamailio && chown 102:102 /run/kamailio \
    ; chown -R 102:102 /etc/kamailio

# Build-time config check: load every module deploy/kamailio/kamailio.cfg
# needs (all ship in the base kamailio package on trixie) so a missing
# module fails the image build, not a phone call.
RUN printf '%s\n' \
      'loadmodule "jsonrpcs.so"' \
      'loadmodule "kex.so"' \
      'loadmodule "corex.so"' \
      'loadmodule "tm.so"' \
      'loadmodule "tmx.so"' \
      'loadmodule "sl.so"' \
      'loadmodule "rr.so"' \
      'loadmodule "pv.so"' \
      'loadmodule "maxfwd.so"' \
      'loadmodule "textops.so"' \
      'loadmodule "siputils.so"' \
      'loadmodule "xlog.so"' \
      'loadmodule "sanity.so"' \
      'loadmodule "ctl.so"' \
      'loadmodule "cfg_rpc.so"' \
      'loadmodule "dispatcher.so"' \
      'loadmodule "path.so"' \
      'loadmodule "usrloc.so"' \
      'loadmodule "registrar.so"' \
      'loadmodule "nathelper.so"' \
      'loadmodule "pike.so"' \
      'loadmodule "xhttp.so"' \
      'loadmodule "xhttp_prom.so"' \
      'loadmodule "rtimer.so"' \
      'request_route { drop; }' \
      > /tmp/check.cfg \
    && kamailio -c -f /tmp/check.cfg > /dev/null \
    && rm /tmp/check.cfg

# The k8s pod mounts an emptyDir at /run/kamailio (fsGroup 102) and the
# ConfigMap at /etc/kamailio; the container itself needs nothing writable
# beyond /run/kamailio.
USER 102:102
EXPOSE 5060/udp 5070/udp 9090
ENTRYPOINT ["kamailio"]
# -DD: stay in the foreground without forking into daemon mode, but still
# fork the UDP/TCP listener children (plain -D would run no-fork and bind
# only the first listen address); -E logs to stderr. The config sets
# log_stderror=yes as well.
CMD ["-DD", "-E"]
