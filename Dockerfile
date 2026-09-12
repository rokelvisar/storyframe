# syntax=docker/dockerfile:1

# ---------- Stage 1: build the Angular SPA ----------
FROM node:24-slim AS frontend
WORKDIR /app
# workspace root manifests first for layer caching
COPY package.json package-lock.json .npmrc ./
COPY frontend/package.json ./frontend/package.json
RUN npm ci
COPY frontend ./frontend
RUN npm --workspace frontend run build
# -> /app/frontend/dist/{index.html,*.js,*.css}

# ---------- Stage 2: build the Go binary ----------
FROM golang:1.23-bookworm AS backend
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# drop the placeholder and embed the real SPA build
RUN rm -rf ./internal/web/dist && mkdir -p ./internal/web/dist
COPY --from=frontend /app/frontend/dist/ ./internal/web/dist/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/video-extractor ./cmd/server

# ---------- Stage 3: minimal runtime ----------
FROM debian:12-slim AS runtime
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ffmpeg fonts-dejavu-core ca-certificates tini \
 && rm -rf /var/lib/apt/lists/*

COPY --from=backend /out/video-extractor /usr/local/bin/video-extractor

ENV PORT=8080 \
    DATA_DIR=/data \
    FFMPEG_BIN=ffmpeg \
    FFMPEG_WORKERS=2 \
    ANALYSIS_PROVIDER=litellm

RUN mkdir -p /data && useradd -r -u 10001 -d /data app && chown -R app /data
VOLUME ["/data"]
USER app
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD ["/usr/local/bin/video-extractor", "-healthcheck"]

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/video-extractor"]
