# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH VERSION=1.0.0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/buggy ./cmd/buggy

FROM alpine:3.22
ARG VERSION=1.0.0
LABEL org.opencontainers.image.title="Buggy" \
    org.opencontainers.image.version="${VERSION}" \
    org.opencontainers.image.source="https://github.com/kohanmathers/buggy"
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H -u 10001 buggy \
    && mkdir -p /data /config && chown buggy /data /config
COPY --from=build /out/buggy /usr/local/bin/buggy
USER buggy
ENV BUGGY_CONFIG=/config/config.yml \
    BUGGY_DATA=/data \
    BUGGY_LISTEN=:8080
VOLUME ["/data", "/config"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["buggy"]
CMD ["serve"]
