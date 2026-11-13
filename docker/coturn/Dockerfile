# coturn for kw (clients-webrtc-mobile-video S-21): STUN and TURN for the
# browser softphone. Built from Debian trixie's coturn package (4.6.1), the
# same reason Kamailio is: it is native on arm64 and amd64, and the kw nodes
# are arm64.
FROM debian:trixie-slim

RUN apt-get update && apt-get install -y --no-install-recommends coturn ca-certificates \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

# The manifests pin runAsUser/runAsGroup 103; normalize Debian's dynamically
# assigned turnserver user to it.
RUN groupmod -g 103 turnserver 2>/dev/null || groupadd -g 103 turnserver \
    ; usermod -u 103 -g 103 turnserver

# Build-time check: the binary starts and the entrypoint is valid shell, so a
# broken package or script fails the image build, not the first call.
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh \
    && sh -n /usr/local/bin/entrypoint.sh \
    && turnserver --version > /dev/null 2>&1

# The configuration is written from the environment at start (the shared
# secret comes from a Secret, so it is never in an image or a ConfigMap) into
# a scratch directory; nothing else is written.
USER 103:103
EXPOSE 3478/udp 3478/tcp 5349/tcp
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
