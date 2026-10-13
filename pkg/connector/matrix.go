package connector

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

func (bc *BitrixClient) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	dialogID := string(msg.Portal.ID)
	var replyID int64
	if msg.ReplyTo != nil {
		replyID = parseMessageID(msg.ReplyTo.ID)
	}
	switch msg.Content.MsgType {
	case event.MsgImage, event.MsgFile, event.MsgVideo, event.MsgAudio:
		return bc.sendMedia(ctx, msg)
	}
	text := bc.matrixToBBCode(ctx, msg.Content)
	if msg.Content.MsgType == event.MsgEmote {
		text = "/me " + text
	}
	templateID := uuid.NewString()
	txnID := networkid.TransactionID(templateID)
	msg.AddPendingToIgnore(txnID)
	messageID, err := bc.client.SendMessage(ctx, dialogID, text, replyID, templateID)
	if err != nil {
		msg.RemovePending(txnID)
		return nil, err
	}
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:       makeMessageID(messageID),
			SenderID: bc.myUserID(),
		},
		StreamOrder:   messageID,
		RemovePending: txnID,
	}, nil
}

func (bc *BitrixClient) sendMedia(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	chatID, err := bc.chatIDFor(ctx, msg.Portal)
	if err != nil {
		return nil, err
	}
	content := msg.Content
	data, err := bc.UserLogin.Bridge.Bot.DownloadMedia(ctx, content.URL, content.File)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", bridgev2.ErrMediaDownloadFailed, err)
	}
	fileName := content.GetFileName()
	if fileName == "" {
		fileName = "file"
	}
	mimeType := ""
	if content.Info != nil {
		mimeType = content.Info.MimeType
	}
	caption := ""
	if content.FileName != "" && content.Body != content.FileName {
		caption = bc.matrixToBBCode(ctx, content)
	}
	// Bitrix doesn't return the message ID; it's learned from the push echo carrying templateId.
	templateID := uuid.NewString()
	txnID := networkid.TransactionID(templateID)
	pendingAdded := false
	folderID := msg.Portal.Metadata.(*PortalMetadata).DiskFolderID
	asFile := content.MsgType == event.MsgFile
	_, err = bc.client.UploadFile(ctx, chatID, folderID, fileName, mimeType, data, caption, templateID, asFile, func(fileID int64) {
		partID := filePartID(fileID)
		if caption != "" {
			partID = textPartID
		}
		msg.AddPendingToSave(&database.Message{PartID: partID, SenderID: bc.myUserID()}, txnID, nil)
		pendingAdded = true
	})
	if err != nil {
		if pendingAdded {
			msg.RemovePending(txnID)
		}
		return nil, err
	}
	return &bridgev2.MatrixMessageResponse{Pending: true}, nil
}

func (bc *BitrixClient) HandleMatrixEdit(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	return bc.client.EditMessage(ctx, parseMessageID(msg.EditTarget.ID), bc.matrixToBBCode(ctx, msg.Content))
}

func (bc *BitrixClient) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	messageID := parseMessageID(msg.TargetMessage.ID)
	bc.rememberMatrixDelete(messageID)
	return bc.client.DeleteMessage(ctx, messageID)
}

func (bc *BitrixClient) PreHandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	code, ok := matrixReactionToBitrix(msg.Content.RelatesTo.Key)
	if !ok {
		return bridgev2.MatrixReactionPreResponse{}, fmt.Errorf("%w: Bitrix only supports %s", bridgev2.ErrReactionsNotSupported, strings.Join(allowedReactions(), " "))
	}
	return bridgev2.MatrixReactionPreResponse{
		SenderID:     bc.myUserID(),
		EmojiID:      networkid.EmojiID(code),
		Emoji:        bitrixReactionEmoji(code),
		MaxReactions: 1,
	}, nil
}

func (bc *BitrixClient) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	return nil, bc.client.AddReaction(ctx, parseMessageID(msg.TargetMessage.ID), string(msg.PreHandleResp.EmojiID))
}

func (bc *BitrixClient) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	return bc.client.DeleteReaction(ctx, parseMessageID(msg.TargetReaction.MessageID), string(msg.TargetReaction.EmojiID))
}

func (bc *BitrixClient) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	if bc.meta().Invisible {
		return nil
	}
	target := msg.ExactMessage
	if target == nil {
		var err error
		target, err = bc.UserLogin.Bridge.DB.Message.GetLastPartAtOrBeforeTime(ctx, msg.Portal.PortalKey, msg.ReadUpTo)
		if err != nil || target == nil {
			return err
		}
	}
	return bc.client.ReadDialog(ctx, string(msg.Portal.ID), parseMessageID(target.ID))
}

func (bc *BitrixClient) HandleMatrixTyping(ctx context.Context, msg *bridgev2.MatrixTyping) error {
	if !msg.IsTyping || msg.Type != bridgev2.TypingTypeText || bc.meta().Invisible {
		return nil
	}
	return bc.client.Writing(ctx, string(msg.Portal.ID))
}
