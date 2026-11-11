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
FROM golang:${GO_VERSION}-alpine AS go-builder
RUN apk add --no-cache git ca-certificates
WORKDIR /build
COPY backend/go.mod backend/go.sum ./backend/
RUN cd backend && go mod download
COPY backend/ ./backend/
COPY contracts/ ./contracts/
COPY deploy/config/ ./deploy/config/
COPY tests/fixtures/ ./tests/fixtures/
COPY --from=web-builder /build/web/dist ./web/dist/
RUN cd backend && CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /vera ./cmd/vera

# Stage 3: Final minimal image
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -g 10001 vera && \
    adduser -u 10001 -G vera -D -h /home/vera vera
WORKDIR /app
COPY --from=go-builder --chown=vera:vera /vera /app/vera
COPY --from=go-builder --chown=vera:vera /build/contracts /app/contracts
COPY --from=go-builder --chown=vera:vera /build/deploy/config /app/deploy/config
COPY --from=go-builder --chown=vera:vera /build/tests/fixtures /app/tests/fixtures
COPY --from=go-builder --chown=vera:vera /build/web/dist /app/web/dist
USER vera
EXPOSE 8080
ENTRYPOINT ["/app/vera"]
CMD ["api"]
