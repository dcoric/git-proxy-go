# syntax=docker/dockerfile:1
#
# Multi-stage build for git-proxy-go (X-2). The runtime image bundles the
# external tools the chain shells out to: git (pullRemote/writePack/diff),
# gitleaks (the secret-scanning processor), and tini as PID 1 so the proxy's
# child git/gitleaks processes are reaped.

ARG GO_VERSION=1.25
ARG GITLEAKS_VERSION=8.30.1
ARG ALPINE_VERSION=3.21

# --- build the static proxy binary -----------------------------------------
FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=0.0.0-docker
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/git-proxy ./cmd/git-proxy

# --- fetch the gitleaks binary for the target arch --------------------------
FROM alpine:${ALPINE_VERSION} AS gitleaks
ARG GITLEAKS_VERSION
ARG TARGETARCH
RUN apk add --no-cache curl tar && \
    case "${TARGETARCH}" in \
      amd64) gl_arch=x64 ;; \
      arm64) gl_arch=arm64 ;; \
      *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac && \
    curl -fsSL "https://github.com/gitleaks/gitleaks/releases/download/v${GITLEAKS_VERSION}/gitleaks_${GITLEAKS_VERSION}_linux_${gl_arch}.tar.gz" \
      | tar -xz -C /usr/local/bin gitleaks && \
    /usr/local/bin/gitleaks version

# --- runtime ----------------------------------------------------------------
FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache git tini ca-certificates && \
    adduser -D -u 10001 -h /home/gitproxy gitproxy
COPY --from=build /out/git-proxy /usr/local/bin/git-proxy
COPY --from=gitleaks /usr/local/bin/gitleaks /usr/local/bin/gitleaks
USER gitproxy
WORKDIR /home/gitproxy
# Management UI / git-transport / SSH (defaults; override via env).
EXPOSE 8080 8000 2222
ENTRYPOINT ["/sbin/tini", "--", "git-proxy"]
