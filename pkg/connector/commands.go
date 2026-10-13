package connector

import (
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
)

var cmdDeleteEmptyPortals = &commands.FullHandler{
	Func: func(ce *commands.Event) {
		portals, err := ce.Bridge.GetAllPortalsWithMXID(ce.Ctx)
		if err != nil {
			ce.Reply("Failed to get portals: %v", err)
			return
		}
		var empty []*bridgev2.Portal
		for _, portal := range portals {
			last, err := ce.Bridge.DB.Message.GetLastPartAtOrBeforeTime(ce.Ctx, portal.PortalKey, time.Now().Add(24*time.Hour))
			if err == nil && last == nil {
				empty = append(empty, portal)
			}
		}
		if len(empty) == 0 {
			ce.Reply("No empty rooms found")
			return
		}
		failed := 0
		bridgev2.DeleteManyPortals(ce.Ctx, empty, func(portal *bridgev2.Portal, delete bool, err error) {
			failed++
			ce.Log.Err(err).Str("portal_id", string(portal.ID)).Bool("db_delete", delete).Msg("Failed to delete empty portal")
		})
		ce.Reply("Deleted %d empty rooms (%d errors)", len(empty), failed)
	},
	Name: "delete-empty-portals",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionAdmin,
		Description: "Delete bridged rooms that have no messages",
	},
	RequiresAdmin: true,
}
