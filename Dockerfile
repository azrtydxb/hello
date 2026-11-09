# Builds the Go services; pick one with --target hello-control | hello-sip | hello-fax.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/azrtydxb/hello/internal/version.Version=${VERSION} -X github.com/azrtydxb/hello/internal/version.Commit=${COMMIT}" \
      -o /out/ ./cmd/... ./test/fakecarrier

FROM alpine:3.24 AS runtime
RUN apk add --no-cache ca-certificates && adduser -D -H -u 65532 hello
USER 65532:65532

# Lab-only simulated SIP carrier (test/fakecarrier); never shipped.
FROM runtime AS fakecarrier
COPY --from=build /out/fakecarrier /usr/local/bin/fakecarrier
EXPOSE 5060/udp 8090
ENTRYPOINT ["fakecarrier"]

FROM runtime AS hello-control
COPY --from=build /out/hello-control /usr/local/bin/hello-control
EXPOSE 8081
ENTRYPOINT ["hello-control"]
CMD ["serve"]

FROM runtime AS hello-sip
COPY --from=build /out/hello-sip /usr/local/bin/hello-sip
EXPOSE 8082 5060/udp
ENTRYPOINT ["hello-sip"]
CMD ["serve"]

# The fax engine (pbx-gaps S-15, decision D13): spandsp through cgo, so it is
# built on glibc here and never in the static build above. The service lives
# in its own image with spandsp and poppler-utils (pdftoppm); hello-sip and
# hello-control never link either.
FROM golang:1.27-bookworm AS build-fax
RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev pkg-config libspandsp-dev libtiff-dev \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=1 go build -trimpath -tags spandsp \
      -ldflags "-s -w -X github.com/azrtydxb/hello/internal/version.Version=${VERSION} -X github.com/azrtydxb/hello/internal/version.Commit=${COMMIT}" \
      -o /out/hello-fax ./cmd/hello-fax

FROM debian:bookworm-slim AS hello-fax
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libspandsp2 libtiff6 poppler-utils \
    && rm -rf /var/lib/apt/lists/* && useradd --system --no-create-home --uid 65532 hello
USER 65532:65532
COPY --from=build-fax /out/hello-fax /usr/local/bin/hello-fax
EXPOSE 8083 40000-40999/udp
ENTRYPOINT ["hello-fax"]
CMD ["serve"]
