# syntax=docker/dockerfile:1
ARG GO_VERSION=1.27.3
ARG NODE_VERSION=22

# Stage 1: Build web frontend
FROM node:${NODE_VERSION}-alpine AS web-builder
WORKDIR /build/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
COPY contracts/ /build/contracts/
RUN npm run build

# Stage 2: Build Go backend
FROM golang:${GO_VERSION}-bookworm AS go-builder
RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev ca-certificates git && rm -rf /var/lib/apt/lists/*
WORKDIR /build
COPY backend/go.mod backend/go.sum ./backend/
# go.mod replaces tdewolff/font with a local copy, so it must exist before go mod download.
COPY backend/third_party/ ./backend/third_party/
RUN cd backend && go mod download
COPY backend/ ./backend/
COPY contracts/ ./contracts/
COPY deploy/config/ ./deploy/config/
COPY tests/fixtures/ ./tests/fixtures/
COPY --from=web-builder /build/web/dist ./web/dist/
RUN cd backend && CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o /vera ./cmd/vera

# Exercise dynamic SDK loading in the same libc/toolchain environment used to
# build the release executable. The fixture is synthetic and never deployed.
FROM go-builder AS native-check
RUN cd backend && CGO_ENABLED=1 go test ./internal/dataservice/emquant -run 'TestNativeLifecycleAndResponse|TestOpenRejectsInvalidAndMissingLibrary' -count=1

# Stage 3: Final minimal image
# debian-slim, not alpine: embedded PostgreSQL ships glibc binaries.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata curl libstdc++6 libgcc-s1 && rm -rf /var/lib/apt/lists/* && \
    groupadd -g 10001 vera && useradd -u 10001 -g vera -m -d /home/vera vera && \
    mkdir -p /data && chown vera:vera /data
# Relative defaults (../contracts, ../deploy/config, ../tests/fixtures) resolve from /app/backend.
WORKDIR /app/backend
COPY --from=go-builder --chown=vera:vera /vera /app/backend/vera
COPY --from=go-builder --chown=vera:vera /build/contracts /app/contracts
COPY --from=go-builder --chown=vera:vera /build/deploy/config /app/deploy/config
COPY --from=go-builder --chown=vera:vera /build/tests/fixtures /app/tests/fixtures
COPY --from=go-builder --chown=vera:vera /build/web/dist /app/web/dist
ENV VERA_ENV=test VERA_API_ADDR=0.0.0.0:8080 VERA_WEB_DIR=/app/web/dist VERA_DEMO_DIR=/data
USER vera
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/app/backend/vera"]
CMD ["demo"]
