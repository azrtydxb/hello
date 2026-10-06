# syntax=docker/dockerfile:1

# ---- build ----
FROM node:26-alpine AS build
WORKDIR /app
# Node >= 25 no longer bundles corepack; install it, then let it provision
# the pnpm version pinned in package.json "packageManager".
RUN npm install -g corepack@0.36.0 && corepack enable
COPY package.json pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY . .
RUN pnpm build

# ---- runtime ----
FROM nginxinc/nginx-unprivileged:alpine
# Upstream for /api/ — rendered into the config by the image's envsubst
# entrypoint (/docker-entrypoint.d/20-envsubst-on-templates.sh).
# HELLO_DNS_RESOLVER: Docker's embedded DNS by default; set it to the
# cluster DNS service (e.g. kube-dns) elsewhere. nginx's resolver applies no
# search domains, so on Kubernetes HELLO_CONTROL_UPSTREAM must be the Service
# FQDN (hello-control.<namespace>.svc.cluster.local).
ENV HELLO_CONTROL_UPSTREAM=http://hello-control-1:8081 \
    HELLO_DNS_RESOLVER=127.0.0.11 \
    NGINX_ENVSUBST_FILTER=^HELLO_
COPY nginx/default.conf.template /etc/nginx/templates/default.conf.template
# Refuse to start when the upstream does not resolve through
# HELLO_DNS_RESOLVER (no search domains: use an FQDN on Kubernetes).
COPY --chmod=755 nginx/30-hello-check-upstream.sh /docker-entrypoint.d/30-hello-check-upstream.sh
COPY --from=build /app/dist /usr/share/nginx/html
# nginx-unprivileged runs as uid 101; keep it explicit.
USER 101
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD ["wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8080/"]
