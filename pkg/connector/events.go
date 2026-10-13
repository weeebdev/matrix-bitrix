package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	"github.com/weeebdev/matrix-bitrix/pkg/bitrix"
)

type pullMessageParams struct {
	ChatID   bitrix.FlexInt    `json:"chatId"`
	DialogID bitrix.FlexString `json:"dialogId"`
	Message  *bitrix.Message   `json:"message"`
	Files    json.RawMessage   `json:"files"`
}

// pullUpdateParams covers messageUpdate / messageDelete / messageDeleteComplete.
type pullUpdateParams struct {
	ID       bitrix.FlexInt    `json:"id"`
	ChatID   bitrix.FlexInt    `json:"chatId"`
	DialogID bitrix.FlexString `json:"dialogId"`
	SenderID bitrix.FlexInt    `json:"senderId"`
	Text     string            `json:"text"`
	Params   bitrix.Params     `json:"params"`
}

type pullReadParams struct {
	ChatID   bitrix.FlexInt    `json:"chatId"`
	DialogID bitrix.FlexString `json:"dialogId"`
	UserID   bitrix.FlexInt    `json:"userId"`
	LastID   bitrix.FlexInt    `json:"lastId"`
	Date     bitrix.FlexTime   `json:"date"`
}

type pullWritingParams struct {
	DialogID bitrix.FlexString `json:"dialogId"`
	UserID   bitrix.FlexInt    `json:"userId"`
}

type pullReactionParams struct {
	UserID          bitrix.FlexInt `json:"userId"`
	Reaction        string         `json:"reaction"`
	ActualReactions struct {
		Reaction bitrix.Reaction `json:"reaction"`
	} `json:"actualReactions"`
}

type pullChatParams struct {
	ChatID   bitrix.FlexInt    `json:"chatId"`
	DialogID bitrix.FlexString `json:"dialogId"`
}

func parseFiles(raw json.RawMessage) map[int64]*bitrix.File {
	out := make(map[int64]*bitrix.File)
	if len(raw) == 0 {
		return out
	}
	var asMap map[string]*bitrix.File
	if err := json.Unmarshal(raw, &asMap); err == nil {
		for _, f := range asMap {
			if f != nil {
				out[int64(f.ID)] = f
			}
		}
		return out
	}
	var asList []*bitrix.File
	if err := json.Unmarshal(raw, &asList); err == nil {
		for _, f := range asList {
			if f != nil {
				out[int64(f.ID)] = f
			}
		}
	}
	return out
}

func (bc *BitrixClient) handlePullEvent(ctx context.Context, evt *bitrix.PullEvent) {
	if evt.ModuleID != "im" {
		return
	}
	log := zerolog.Ctx(ctx).With().Str("command", evt.Command).Logger()
	ctx = log.WithContext(ctx)
	var err error
	switch evt.Command {
	case "message", "messageChat":
		err = bc.handleNewMessage(ctx, evt.Params)
	case "messageUpdate":
		err = bc.handleMessageUpdate(ctx, evt.Params)
	case "messageDelete", "messageDeleteComplete":
		err = bc.handleMessageDelete(ctx, evt.Params)
	case "addReaction", "deleteReaction":
		err = bc.handleReaction(ctx, evt.Params, evt.Command == "deleteReaction")
	case "readMessageOpponent", "readMessageChatOpponent":
		err = bc.handleReadReceipt(ctx, evt.Params, false)
	case "readMessage", "readMessageChat":
		err = bc.handleReadReceipt(ctx, evt.Params, true)
	case "messageParamsUpdate":
		err = bc.handleParamsUpdate(ctx, evt.Params)
	case "pinDelete":
		err = bc.handlePinDelete(ctx, evt.Params)
	case "startWriting":
		err = bc.handleTyping(ctx, evt.Params)
	case "chatRename", "chatAvatar", "chatUserAdd", "chatUserLeave", "chatUpdate", "chatOwner", "chatManagers":
		err = bc.handleChatChange(ctx, evt.Params)
	default:
		log.Trace().Msg("Unhandled im push event")
	}
	if err != nil {
		log.Err(err).RawJSON("params", evt.Params).Msg("Failed to handle push event")
	}
}

// resolveDialog figures out the portal dialog ID from the event's chat/dialog IDs.
// For private chats, Bitrix's dialogId is relative to the receiver, so peerID (the other
// user involved in the event) is used when the dialog isn't already known.
func (bc *BitrixClient) resolveDialog(chatID int64, dialogID string, peerID int64) string {
	if known := bc.dialogForChat(chatID); known != "" {
		return known
	}
	if bitrix.IsChatDialog(dialogID) {
		bc.rememberDialog(chatID, dialogID)
		return dialogID
	}
	if peerID != 0 && peerID != bc.client.UserID {
		return strconv.FormatInt(peerID, 10)
	}
	if dialogID != "" && dialogID != strconv.FormatInt(bc.client.UserID, 10) {
		return dialogID
	}
	return ""
}

// portalKeyForMessage finds the portal of an already-bridged message.
func (bc *BitrixClient) portalKeyForMessage(ctx context.Context, messageID int64) (networkid.PortalKey, bool) {
	msg, err := bc.UserLogin.Bridge.DB.Message.GetFirstPartByID(ctx, bc.UserLogin.ID, makeMessageID(messageID))
	if err != nil || msg == nil {
		return networkid.PortalKey{}, false
	}
	return msg.Room, true
}

func (bc *BitrixClient) handleNewMessage(ctx context.Context, raw json.RawMessage) error {
	var p pullMessageParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	msg := p.Message
	if msg == nil || msg.ID == 0 {
		return nil
	}
	if msg.ChatID == 0 {
		msg.ChatID = p.ChatID
	}
	sender := msg.Sender()
	var dialogID string
	recipient := string(msg.RecipientID)
	switch {
	case bitrix.IsChatDialog(recipient):
		dialogID = recipient
	case sender == bc.client.UserID:
		dialogID = bc.resolveDialog(int64(msg.ChatID), recipient, parseDialogInt(recipient))
	default:
		dialogID = bc.resolveDialog(int64(msg.ChatID), string(p.DialogID), sender)
	}
	if dialogID == "" {
		dialogID = string(p.DialogID)
	}
	if dialogID == "" {
		zerolog.Ctx(ctx).Warn().Int64("message_id", int64(msg.ID)).Msg("Couldn't determine dialog of message")
		return nil
	}
	bc.rememberDialog(int64(msg.ChatID), dialogID)
	data := &incomingMessage{Msg: msg, Files: parseFiles(p.Files)}
	eventSender := bc.sender(sender)
	if msg.Sender() == 0 {
		eventSender = bridgev2.EventSender{}
	}
	bc.UserLogin.Bridge.QueueRemoteEvent(bc.UserLogin, &simplevent.Message[*incomingMessage]{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventMessage,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Int64("message_id", int64(msg.ID)).Str("dialog_id", dialogID)
			},
			PortalKey:    bc.portalKey(dialogID),
			CreatePortal: true,
			Sender:       eventSender,
			Timestamp:    msgTime(msg),
			StreamOrder:  int64(msg.ID),
		},
		Data:               data,
		ID:                 makeMessageID(int64(msg.ID)),
		TransactionID:      networkid.TransactionID(msg.TemplateID),
		ConvertMessageFunc: bc.convertMessage,
	})
	return nil
}

func msgTime(msg *bitrix.Message) time.Time {
	if msg.Date.IsZero() {
		return time.Now()
	}
	return msg.Date.Time
}

func (bc *BitrixClient) updateTarget(ctx context.Context, p *pullUpdateParams) (networkid.PortalKey, bool) {
	if key, ok := bc.portalKeyForMessage(ctx, int64(p.ID)); ok {
		return key, true
	}
	return networkid.PortalKey{}, false
}

func (bc *BitrixClient) handleMessageUpdate(ctx context.Context, raw json.RawMessage) error {
	var p pullUpdateParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	key, ok := bc.updateTarget(ctx, &p)
	if !ok {
		zerolog.Ctx(ctx).Debug().Int64("message_id", int64(p.ID)).Msg("Ignoring edit of unknown message")
		return nil
	}
	msg := &bitrix.Message{ID: p.ID, ChatID: p.ChatID, AuthorID: p.SenderID, SenderID: p.SenderID, Text: p.Text, Params: p.Params}
	bc.UserLogin.Bridge.QueueRemoteEvent(bc.UserLogin, &simplevent.Message[*incomingMessage]{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventEdit,
			PortalKey: key,
			Sender:    bc.sender(int64(p.SenderID)),
			Timestamp: time.Now(),
		},
		Data:            &incomingMessage{Msg: msg},
		ID:              makeMessageID(int64(p.ID)),
		TargetMessage:   makeMessageID(int64(p.ID)),
		ConvertEditFunc: bc.convertEdit,
	})
	return nil
}

func (bc *BitrixClient) handleMessageDelete(ctx context.Context, raw json.RawMessage) error {
	var p pullUpdateParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	key, ok := bc.updateTarget(ctx, &p)
	if !ok {
		return nil
	}
	messageID := int64(p.ID)
	// Own messages aren't stored and are always removed, wherever they were deleted.
	if bc.meta().KeepDeletedEnabled() && !bc.wasDeletedFromMatrix(messageID) && int64(p.SenderID) != bc.client.UserID {
		return bc.markDeleted(ctx, key, messageID, int64(p.SenderID))
	}
	bc.UserLogin.Bridge.QueueRemoteEvent(bc.UserLogin, &simplevent.MessageRemove{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessageRemove,
			PortalKey: key,
			Sender:    bc.sender(int64(p.SenderID)),
			Timestamp: time.Now(),
		},
		TargetMessage: makeMessageID(messageID),
	})
	return nil
}

// markDeleted keeps a message deleted in Bitrix and marks it as deleted in place: text parts are
// struck through, file parts keep their file with a marked caption. Messages bridged before content
// was stored in metadata get a reply notice instead.
func (bc *BitrixClient) markDeleted(ctx context.Context, key networkid.PortalKey, messageID, senderID int64) error {
	parts, err := bc.UserLogin.Bridge.DB.Message.GetAllPartsByID(ctx, bc.UserLogin.ID, makeMessageID(messageID))
	if err != nil {
		return err
	}
	canEdit := false
	for _, part := range parts {
		meta, ok := part.Metadata.(*MessageMetadata)
		if ok && meta.Deleted {
			return nil // messageDelete and messageDeleteComplete both arrive for one delete
		} else if bc.openContent(part) != nil {
			canEdit = true
		}
	}
	if !canEdit {
		return bc.replyDeleted(ctx, key, messageID)
	}
	bc.UserLogin.Bridge.QueueRemoteEvent(bc.UserLogin, &simplevent.Message[int64]{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventEdit,
			PortalKey: key,
			Sender:    bc.sender(senderID),
			Timestamp: time.Now(),
		},
		Data:          messageID,
		ID:            makeMessageID(messageID),
		TargetMessage: makeMessageID(messageID),
		ConvertEditFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message, _ int64) (*bridgev2.ConvertedEdit, error) {
			edit := &bridgev2.ConvertedEdit{}
			for _, part := range existing {
				meta, ok := part.Metadata.(*MessageMetadata)
				stored := bc.openContent(part)
				if !ok || stored == nil || stored.Content == nil || meta.Deleted {
					continue
				}
				edit.ModifiedParts = append(edit.ModifiedParts, &bridgev2.ConvertedEditPart{
					Part:    part,
					Type:    event.EventMessage,
					Content: deletedContent(stored.Content),
				})
				meta.Deleted = true
			}
			if len(edit.ModifiedParts) == 0 {
				return nil, fmt.Errorf("%w: nothing to mark as deleted", bridgev2.ErrIgnoringRemoteEvent)
			}
			return edit, nil
		},
	})
	return nil
}

const deletedLabel = "🗑 deleted in Bitrix"

// deletedContent returns a copy of content marked as deleted.
func deletedContent(orig *event.MessageEventContent) *event.MessageEventContent {
	c := *orig
	c.Mentions = &event.Mentions{}
	switch c.MsgType {
	case event.MsgText, event.MsgNotice, event.MsgEmote:
		formatted := c.FormattedBody
		if c.Format != event.FormatHTML || formatted == "" {
			formatted = strings.ReplaceAll(html.EscapeString(c.Body), "\n", "<br/>")
		}
		c.Format = event.FormatHTML
		c.FormattedBody = "<del>" + formatted + "</del><br/><em>" + deletedLabel + "</em>"
		c.Body = "~~" + c.Body + "~~\n" + deletedLabel
	default:
		// Media: keep the file, mark the caption
		name := c.FileName
		if name == "" {
			name = c.Body
		}
		c.FileName = name
		c.Body = name + " (" + deletedLabel + ")"
		c.Format, c.FormattedBody = "", ""
	}
	return &c
}

func (bc *BitrixClient) replyDeleted(ctx context.Context, key networkid.PortalKey, messageID int64) error {
	noticeID := networkid.MessageID(fmt.Sprintf("deleted-%d", messageID))
	if existing, err := bc.UserLogin.Bridge.DB.Message.GetFirstPartByID(ctx, bc.UserLogin.ID, noticeID); err == nil && existing != nil {
		return nil
	}
	bc.UserLogin.Bridge.QueueRemoteEvent(bc.UserLogin, &simplevent.Message[int64]{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessage,
			PortalKey: key,
			Timestamp: time.Now(),
		},
		Data: messageID,
		ID:   noticeID,
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, _ int64) (*bridgev2.ConvertedMessage, error) {
			return &bridgev2.ConvertedMessage{
				ReplyTo: &networkid.MessageOptionalPartID{MessageID: makeMessageID(messageID)},
				Parts: []*bridgev2.ConvertedMessagePart{{
					Type:    event.EventMessage,
					Content: &event.MessageEventContent{MsgType: event.MsgNotice, Body: "🗑 This message was deleted in Bitrix"},
				}},
			}, nil
		},
	})
	return nil
}

func (bc *BitrixClient) handleReaction(ctx context.Context, raw json.RawMessage, remove bool) error {
	var p pullReactionParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	messageID := int64(p.ActualReactions.Reaction.MessageID)
	key, ok := bc.portalKeyForMessage(ctx, messageID)
	if !ok || p.Reaction == "" {
		return nil
	}
	evtType := bridgev2.RemoteEventReaction
	if remove {
		evtType = bridgev2.RemoteEventReactionRemove
	}
	bc.UserLogin.Bridge.QueueRemoteEvent(bc.UserLogin, &simplevent.Reaction{
		EventMeta: simplevent.EventMeta{
			Type:      evtType,
			PortalKey: key,
			Sender:    bc.sender(int64(p.UserID)),
			Timestamp: time.Now(),
		},
		TargetMessage: makeMessageID(messageID),
		EmojiID:       networkid.EmojiID(p.Reaction),
		Emoji:         bitrixReactionEmoji(p.Reaction),
	})
	return nil
}

func (bc *BitrixClient) handleReadReceipt(ctx context.Context, raw json.RawMessage, own bool) error {
	var p pullReadParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if p.LastID == 0 {
		return nil
	}
	reader := int64(p.UserID)
	if own {
		reader = bc.client.UserID
	} else if reader == bc.client.UserID {
		return nil
	}
	peer := int64(0)
	if !own {
		peer = reader
	}
	dialogID := bc.resolveDialog(int64(p.ChatID), string(p.DialogID), peer)
	if dialogID == "" {
		if key, ok := bc.portalKeyForMessage(ctx, int64(p.LastID)); ok {
			dialogID = string(key.ID)
		} else {
			return nil
		}
	}
	ts := p.Date.Time
	if ts.IsZero() {
		ts = time.Now()
	}
	bc.UserLogin.Bridge.QueueRemoteEvent(bc.UserLogin, &simplevent.Receipt{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventReadReceipt,
			PortalKey: bc.portalKey(dialogID),
			Sender:    bc.sender(reader),
			Timestamp: ts,
		},
		LastTarget: makeMessageID(int64(p.LastID)),
	})
	return nil
}

type typingEvent struct {
	simplevent.EventMeta
	Timeout time.Duration
}

var _ bridgev2.RemoteTyping = (*typingEvent)(nil)

func (t *typingEvent) GetTimeout() time.Duration { return t.Timeout }

func (bc *BitrixClient) handleTyping(ctx context.Context, raw json.RawMessage) error {
	var p pullWritingParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	userID := int64(p.UserID)
	if userID == 0 || userID == bc.client.UserID {
		return nil
	}
	dialogID := string(p.DialogID)
	if !bitrix.IsChatDialog(dialogID) {
		dialogID = strconv.FormatInt(userID, 10)
	}
	bc.UserLogin.Bridge.QueueRemoteEvent(bc.UserLogin, &typingEvent{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventTyping,
			PortalKey: bc.portalKey(dialogID),
			Sender:    bc.sender(userID),
			Timestamp: time.Now(),
		},
		Timeout: 15 * time.Second,
	})
	return nil
}

func (bc *BitrixClient) handleChatChange(ctx context.Context, raw json.RawMessage) error {
	var p pullChatParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	dialogID := string(p.DialogID)
	if !bitrix.IsChatDialog(dialogID) {
		if p.ChatID == 0 {
			return nil
		}
		dialogID = bitrix.ChatDialogID(int64(p.ChatID))
	}
	if strings.TrimSpace(dialogID) == "" {
		return nil
	}
	bc.UserLogin.Bridge.QueueRemoteEvent(bc.UserLogin, &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatResync,
			PortalKey: bc.portalKey(dialogID),
		},
		GetChatInfoFunc: bc.GetChatInfo,
	})
	return nil
}
