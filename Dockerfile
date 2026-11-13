# syntax=docker/dockerfile:1
#
# The router is a pure-Go static binary (CGO_ENABLED=0), so the runtime
# image is distroless/static-debian12 with no C runtime or shared libraries.
#
# The mini UI is a Next.js static export built by a Node.js stage and
# copied into assets/ui/ before the Go binary is assembled.

# --- Stage 1: build the Next.js mini UI ---
FROM node:22-alpine AS ui-builder
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm ci --prefer-offline
COPY frontend/ ./
# next.config.ts sets `output: "export"` so the static export lands at
# /app/frontend/out/; the build-stage below copies it into /app/assets/ui
# where the Go server reads it.
RUN npm run build

FROM golang:1.26.10-bookworm AS build-stage

# Build identity stamped into the binary via -ldflags (see the go build below)
# and surfaced at GET /v1/version. Default to "unknown" so a plain
# `docker build` with no --build-arg still produces a runnable image.
ARG ROUTER_SHA=unknown
ARG ROUTER_BUILD_TIME=unknown
ARG ROUTER_PR=unknown
WORKDIR /app

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd ./cmd
COPY internal ./internal
# Mini UI static export built by the ui-builder stage.
COPY --from=ui-builder /app/frontend/out ./assets/ui

WORKDIR /app/cmd/router
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
      -ldflags "-X weave-os/router/internal/version.Commit=${ROUTER_SHA} -X weave-os/router/internal/version.BuildTime=${ROUTER_BUILD_TIME} -X weave-os/router/internal/version.PR=${ROUTER_PR}" \
      -o /server

# The smoke suite's one-shot seed command is a small target in this Dockerfile
# so CI does not start a fresh Go SDK container and run `go run` on every smoke
# invocation.
FROM golang:1.26.10-bookworm AS seed-build
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/seed ./cmd/seed
COPY internal ./internal
RUN CGO_ENABLED=0 go build -o /seed ./cmd/seed

FROM gcr.io/distroless/static-debian12 AS seed-runtime
COPY --from=seed-build /seed /seed
ENTRYPOINT ["/seed"]


FROM gcr.io/distroless/static-debian12 AS build-release-stage

WORKDIR /
COPY --from=build-stage /server /server
# Mini UI static files served by gin at /ui; path must match the
# gin.Static("/ui", "./assets/ui") call in internal/server/server.go.
COPY --from=build-stage /app/assets/ui ./assets/ui

ARG VERSION
ENV VERSION=$VERSION

ENTRYPOINT ["/server"]
