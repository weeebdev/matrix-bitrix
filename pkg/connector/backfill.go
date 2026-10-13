package connector

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/weeebdev/matrix-bitrix/pkg/bitrix"
)

// backfillHint is passed from the recent-chat sync to the initial backfill.
type backfillHint struct {
	Unread bool
}

func (bc *BitrixClient) FetchMessages(ctx context.Context, params bridgev2.FetchMessagesParams) (*bridgev2.FetchMessagesResponse, error) {
	if bc.meta().Paused {
		return nil, fmt.Errorf("bridge is paused")
	}
	chatID, err := bc.chatIDFor(ctx, params.Portal)
	if err != nil {
		return nil, err
	}
	count := min(max(params.Count, 1), 50)
	var anchor int64
	if params.Cursor != "" {
		anchor, _ = strconv.ParseInt(string(params.Cursor), 10, 64)
	} else if params.AnchorMessage != nil {
		anchor = parseMessageID(params.AnchorMessage.ID)
	}
	var batch *bitrix.MessageBatch
	if params.Forward {
		if anchor == 0 {
			batch, err = bc.client.MessageList(ctx, chatID, count)
		} else {
			batch, err = bc.client.MessageTail(ctx, chatID, anchor, count, false)
		}
	} else {
		batch, err = bc.client.MessageTail(ctx, chatID, anchor, count, true)
	}
	if err != nil {
		return nil, err
	}
	files := batch.FileMap()
	reactions := bc.backfillReactions(batch.Reactions)
	msgs := batch.Messages
	slices.SortFunc(msgs, func(a, b *bitrix.Message) int { return int(a.ID - b.ID) })

	resp := &bridgev2.FetchMessagesResponse{
		Forward: params.Forward,
		HasMore: len(msgs) >= count,
	}
	if hint, ok := params.BundledData.(*backfillHint); ok && params.Forward {
		resp.MarkRead = !hint.Unread
	}
	for _, m := range msgs {
		if params.Forward && anchor != 0 && int64(m.ID) <= anchor {
			continue
		}
		converted, err := bc.convertMessage(ctx, params.Portal, bc.UserLogin.Bridge.Bot, &incomingMessage{Msg: m, Files: files})
		if err != nil {
			return nil, err
		}
		sender := bc.sender(m.Sender())
		if m.Sender() == 0 {
			sender = bridgev2.EventSender{}
		}
		resp.Messages = append(resp.Messages, &bridgev2.BackfillMessage{
			ConvertedMessage: converted,
			Sender:           sender,
			ID:               makeMessageID(int64(m.ID)),
			Timestamp:        msgTime(m),
			StreamOrder:      int64(m.ID),
			Reactions:        reactions[int64(m.ID)],
		})
	}
	if len(msgs) > 0 {
		edge := msgs[0].ID
		if params.Forward {
			edge = msgs[len(msgs)-1].ID
		}
		resp.Cursor = networkid.PaginationCursor(edge.String())
	}
	return resp, nil
}
