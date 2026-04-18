# ── Frontend build stage ─────────────────────────────────────────────────────
FROM node:20-alpine AS frontend

WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ .
RUN npm run build

# ── Backend build stage ──────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Copy the compiled frontend into the Go embed target
COPY --from=frontend /web/dist ./internal/webstatic/dist

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /spottr \
    ./cmd/spotnet

# ── Runtime stage ────────────────────────────────────────────────────────────
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=builder /spottr /app/spottr
RUN mkdir -p /data
VOLUME ["/data"]

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD wget -qO- http://localhost:8080/api?t=caps || exit 1

ENTRYPOINT ["/app/spottr"]
