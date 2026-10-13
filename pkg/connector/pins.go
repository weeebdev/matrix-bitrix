package connector

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/weeebdev/matrix-bitrix/pkg/bitrix"
)

// Pins are bridged one way (Bitrix -> Matrix): bridgev2 doesn't route m.room.pinned_events
// changes to network connectors yet.

type pullParamsUpdate struct {
	ID     bitrix.FlexInt `json:"id"`
	Params bitrix.Params  `json:"params"`
}

type pullPinDelete struct {
	MessageID bitrix.FlexInt `json:"messageId"`
}

func (bc *BitrixClient) handleParamsUpdate(ctx context.Context, raw json.RawMessage) error {
	var p pullParamsUpdate
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	pinned, ok := p.Params["IS_PINNED"]
	if !ok {
		return nil
	}
	return bc.setPinned(ctx, int64(p.ID), strings.Contains(string(pinned), "Y"))
}

func (bc *BitrixClient) handlePinDelete(ctx context.Context, raw json.RawMessage) error {
	var p pullPinDelete
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	return bc.setPinned(ctx, int64(p.MessageID), false)
}

func (bc *BitrixClient) setPinned(ctx context.Context, messageID int64, pinned bool) error {
	br := bc.UserLogin.Bridge
	dbMsg, err := br.DB.Message.GetFirstPartByID(ctx, bc.UserLogin.ID, makeMessageID(messageID))
	if err != nil || dbMsg == nil {
		return err
	}
	portal, err := br.GetExistingPortalByKey(ctx, dbMsg.Room)
	if err != nil || portal == nil || portal.MXID == "" {
		return err
	}
	var content event.PinnedEventsEventContent
	if stateMatrix, ok := br.Matrix.(bridgev2.MatrixConnectorWithArbitraryRoomState); ok {
		if evt, err := stateMatrix.GetStateEvent(ctx, portal.MXID, event.StatePinnedEvents, ""); err == nil && evt != nil {
			_ = evt.Content.ParseRaw(event.StatePinnedEvents)
			if parsed, ok := evt.Content.Parsed.(*event.PinnedEventsEventContent); ok {
				content = *parsed
			}
		}
	}
	has := slices.Contains(content.Pinned, dbMsg.MXID)
	if pinned == has {
		return nil
	}
	if pinned {
		content.Pinned = append(content.Pinned, dbMsg.MXID)
	} else {
		content.Pinned = slices.DeleteFunc(content.Pinned, func(e id.EventID) bool { return e == dbMsg.MXID })
	}
	_, err = br.Bot.SendState(ctx, portal.MXID, event.StatePinnedEvents, "", &event.Content{Parsed: &content}, time.Now())
	if err == nil {
		zerolog.Ctx(ctx).Debug().Int64("message_id", messageID).Bool("pinned", pinned).Msg("Updated pinned events")
	}
	return err
}
