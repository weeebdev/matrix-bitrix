# syntax=docker/dockerfile:1
#
# Built natively on an arm64 runner (see .github/workflows/release.yml):
# mattn/go-sqlite3 needs cgo, and cross-compiling cgo is not worth it when
# the cluster is all Oracle ARM anyway.

FROM golang:1.26-alpine AS build
RUN apk add --no-cache build-base git
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    ./build.sh

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /src/matrix-bitrix /usr/bin/matrix-bitrix
# State (sqlite db) lives here; mount a volume.
WORKDIR /data
ENTRYPOINT ["/usr/bin/matrix-bitrix"]
# Config is mounted read-only, so never let the bridge rewrite it.
CMD ["-c", "/config/config.yaml", "--no-update"]
