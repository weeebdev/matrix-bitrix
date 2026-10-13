package main

import (
	"context"

	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"

	"github.com/weeebdev/matrix-bitrix/pkg/connector"
)

// Filled at build time with -X linker flags.
var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

func main() {
	m := &mxmain.BridgeMain{
		Name:        "matrix-bitrix",
		Description: "A Matrix-Bitrix24 puppeting bridge",
		URL:         "https://github.com/weeebdev/matrix-bitrix",
		Version:     "0.1.0",
		Connector:   &connector.BitrixConnector{},
	}
	m.PostInit = func() { migrateDBOwner(m) }
	m.InitVersion(Tag, Commit, BuildTime)
	m.Run()
}

// migrateDBOwner moves databases created before the rename from mautrix-bitrix (the old name) to
// matrix-bitrix. dbutil refuses to open a database owned by another program name; the owner check
// runs on the first upgrade in Start, so this has to happen in PostInit.
func migrateDBOwner(m *mxmain.BridgeMain) {
	res, err := m.DB.Exec(context.Background(),
		"UPDATE database_owner SET owner=$1 WHERE key=0 AND owner=$2",
		m.GetDBOwner(), "megabridge/mautrix-bitrix")
	if err != nil {
		// A fresh database doesn't have the table yet.
		m.Log.Debug().Err(err).Msg("Database owner not migrated")
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		m.Log.Info().Str("new_owner", m.GetDBOwner()).Msg("Migrated database owner from mautrix-bitrix")
	}
}
