#!/bin/sh
# Builds the bridge with the pure-Go olm implementation (no libolm needed).
MAUTRIX_VERSION=$(grep 'maunium.net/go/mautrix ' go.mod | awk '{ print $2 }' | head -n1)
GO_LDFLAGS="-s -w -X main.Tag=$(git describe --exact-match --tags 2>/dev/null) -X main.Commit=$(git rev-parse HEAD 2>/dev/null) -X 'main.BuildTime=`date -Iseconds`' -X 'maunium.net/go/mautrix.GoModVersion=$MAUTRIX_VERSION'"
go build -tags goolm -ldflags="$GO_LDFLAGS" -o matrix-bitrix ./cmd/matrix-bitrix "$@"
