# Slim image for Coolify / small hosts.
# ---------------------------------------------------------------------------
# This root Dockerfile is a lean variant of docker/Dockerfile: it drops the
# WhatsApp Calling / IVR voice stack (Piper TTS + voice model + espeak-ng +
# opus-tools) and ffmpeg. Result: a ~120MB image instead of ~800MB, so the
# build and (crucially) the layer export don't exhaust RAM on small servers.
#
# Disabled by this variant: WhatsApp Calling/IVR voice prompts and server-side
# audio/video transcoding. Core messaging, campaigns, chatbot, contacts, etc.
# are unaffected. Need those features? Use docker/Dockerfile (full image) or
# ask to add ffmpeg back.
# ---------------------------------------------------------------------------

# Frontend build stage — pinned to BUILDPLATFORM (output is arch-agnostic JS).
FROM --platform=$BUILDPLATFORM node:22-alpine AS frontend-builder

WORKDIR /app/frontend

# Copy frontend dependency files first for caching
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci

# Copy frontend source and build
COPY frontend/ .
RUN npm run build

# Go build stage — runs on BUILDPLATFORM, cross-compiles to TARGETOS/TARGETARCH.
FROM --platform=$BUILDPLATFORM golang:1.25.3-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

RUN apk add --no-cache git ca-certificates

# Cache module downloads
COPY go.mod go.sum ./
RUN go mod download

# Copy source and embed the frontend build into the Go binary
COPY . .
COPY --from=frontend-builder /app/frontend/dist/ ./internal/frontend/dist/

# Static binary (CGO disabled) so it runs on a minimal Alpine base.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -installsuffix cgo -o whatomate ./cmd/whatomate

# Minimal runtime — the static binary only needs CA certs + timezone data.
FROM alpine:3.22

WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata

# Binary
COPY --from=builder /app/whatomate .

# Config example (overridden by WHATOMATE_* env vars in production)
COPY --from=builder /app/config.example.toml ./config.toml

# Runtime data directories
RUN mkdir -p /app/uploads /app/audio

EXPOSE 8080

# Binary is the ENTRYPOINT; CMD holds only the default subcommand/flags.
ENTRYPOINT ["./whatomate"]
CMD ["server", "-config", "config.toml", "-migrate"]
