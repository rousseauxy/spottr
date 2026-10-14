# ── Frontend build stage ─────────────────────────────────────────────────────
FROM node:24-alpine AS frontend

WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ .
RUN npm run build

# ── Backend build stage ──────────────────────────────────────────────────────
FROM golang:1.27-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Copy the compiled frontend into the Go embed target
COPY --from=frontend /web/dist ./internal/webstatic/dist

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /spottr \
    ./cmd/spottr

# ── Runtime stage ────────────────────────────────────────────────────────────
FROM alpine:3.24

# apk upgrade: the base image can lag behind a fixed package (zlib, OpenSSL).
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates tzdata wget

# Run as an unprivileged user. The process only needs to read its own binary
# and read/write /data; nothing it does requires root, and running as uid 0
# meant any RCE started with full container privileges.
RUN addgroup -g 65532 -S spottr \
 && adduser  -u 65532 -S spottr -G spottr

WORKDIR /app
COPY --from=builder /spottr /app/spottr

# /data is a volume mount at runtime; create it owned by the runtime user so a
# fresh (empty) volume is writable without needing an entrypoint chown.
RUN mkdir -p /data && chown -R spottr:spottr /data /app
VOLUME ["/data"]

USER spottr:spottr

EXPOSE 8080

# Hits the dedicated liveness endpoint rather than the newznab API, which now
# requires a key — the old `/api?t=caps` probe would fail closed.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/app/spottr"]
