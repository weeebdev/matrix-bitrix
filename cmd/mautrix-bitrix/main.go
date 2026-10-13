package main

import (
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"

	"github.com/weeebdev/mautrix-bitrix/pkg/connector"
)

// Filled at build time with -X linker flags.
var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

func main() {
	m := mxmain.BridgeMain{
		Name:        "mautrix-bitrix",
		Description: "A Matrix-Bitrix24 puppeting bridge",
		URL:         "https://github.com/weeebdev/mautrix-bitrix",
		Version:     "0.1.0",
		Connector:   &connector.BitrixConnector{},
	}
	m.InitVersion(Tag, Commit, BuildTime)
	m.Run()
}
