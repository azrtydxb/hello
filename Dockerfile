# Builds both Go services; pick one with --target hello-control | hello-sip.
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

FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates && adduser -D -H -u 65532 hello
USER 65532:65532

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

# Lab-only simulated SIP carrier (test/fakecarrier); never shipped.
FROM runtime AS fakecarrier
COPY --from=build /out/fakecarrier /usr/local/bin/fakecarrier
EXPOSE 5060/udp 8090
ENTRYPOINT ["fakecarrier"]
