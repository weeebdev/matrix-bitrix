package connector

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/weeebdev/mautrix-bitrix/pkg/bitrix"
)

const textPartID = networkid.PartID("")

// Beeper's media repository rejects uploads above 100 MiB; skip downloading those at all.
const maxBridgedFileSize = 100 << 20

func filePartID(fileID int64) networkid.PartID {
	return networkid.PartID(fmt.Sprintf("file-%d", fileID))
}

// incomingMessage bundles a message with the side data needed to convert it.
type incomingMessage struct {
	Msg   *bitrix.Message
	Files map[int64]*bitrix.File
}

func (bc *BitrixClient) convertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data *incomingMessage) (*bridgev2.ConvertedMessage, error) {
	msg := data.Msg
	cm := &bridgev2.ConvertedMessage{}
	if replyID := msg.ReplyID(); replyID != 0 {
		cm.ReplyTo = &networkid.MessageOptionalPartID{MessageID: makeMessageID(replyID)}
	}
	text := strings.TrimSpace(msg.Text)
	if text != "" {
		content := bbcodeToMatrix(text, bc.mentionResolver)
		if msg.IsSystemMessage() {
			content.MsgType = event.MsgNotice
		}
		cm.Parts = append(cm.Parts, &bridgev2.ConvertedMessagePart{
			ID:      textPartID,
			Type:    event.EventMessage,
			Content: content,
		})
	}
	for _, fileID := range msg.FileIDs() {
		part := bc.convertFile(ctx, portal, intent, fileID, data.Files[fileID])
		cm.Parts = append(cm.Parts, part)
	}
	for _, part := range cm.Parts {
		part.DBMetadata = &MessageMetadata{Content: part.Content}
	}
	if len(cm.Parts) == 0 {
		cm.Parts = append(cm.Parts, &bridgev2.ConvertedMessagePart{
			ID:   textPartID,
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgNotice,
				Body:    "Unsupported Bitrix message",
			},
		})
	}
	return cm, nil
}

func (bc *BitrixClient) convertFile(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, fileID int64, file *bitrix.File) *bridgev2.ConvertedMessagePart {
	part := &bridgev2.ConvertedMessagePart{
		ID:   filePartID(fileID),
		Type: event.EventMessage,
	}
	if file == nil || file.URLDownload == "" {
		part.Content = &event.MessageEventContent{
			MsgType: event.MsgNotice,
			Body:    fmt.Sprintf("Bitrix file #%d (not available)", fileID),
		}
		return part
	}
	content := &event.MessageEventContent{
		Body:     file.Name,
		FileName: file.Name,
		Info:     &event.FileInfo{Size: int(file.Size)},
	}
	switch file.Type {
	case "image":
		content.MsgType = event.MsgImage
	case "video":
		content.MsgType = event.MsgVideo
	case "audio":
		content.MsgType = event.MsgAudio
	default:
		content.MsgType = event.MsgFile
	}
	if file.Image != nil {
		content.Info.Width, content.Info.Height = file.Image.Width, file.Image.Height
	}
	if file.Size > maxBridgedFileSize {
		part.Content = &event.MessageEventContent{
			MsgType: event.MsgNotice,
			Body:    fmt.Sprintf("File %s is too large to bridge (%.1f MB), open it in Bitrix", file.Name, float64(file.Size)/(1<<20)),
		}
		return part
	}
	data, mimeType, err := bc.client.Download(ctx, file.URLDownload)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Int64("file_id", fileID).Msg("Failed to download Bitrix file")
		part.Content = &event.MessageEventContent{
			MsgType: event.MsgNotice,
			Body:    fmt.Sprintf("Failed to bridge file %s", file.Name),
		}
		return part
	}
	if mimeType == "" || strings.HasPrefix(mimeType, "application/octet-stream") {
		if guessed := mime.TypeByExtension(path.Ext(file.Name)); guessed != "" {
			mimeType = guessed
		} else {
			mimeType = http.DetectContentType(data)
		}
	}
	mimeType = strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0])
	content.Info.MimeType = mimeType
	content.Info.Size = len(data)
	content.URL, content.File, err = intent.UploadMedia(ctx, portal.MXID, data, file.Name, mimeType)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Int64("file_id", fileID).Msg("Failed to upload file to Matrix")
		part.Content = &event.MessageEventContent{
			MsgType: event.MsgNotice,
			Body:    fmt.Sprintf("Failed to bridge file %s", file.Name),
		}
		return part
	}
	part.Content = content
	return part
}

func (bc *BitrixClient) convertEdit(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message, data *incomingMessage) (*bridgev2.ConvertedEdit, error) {
	var target *database.Message
	for _, part := range existing {
		if part.PartID == textPartID {
			target = part
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("%w: no text part to edit", bridgev2.ErrIgnoringRemoteEvent)
	}
	content := bbcodeToMatrix(strings.TrimSpace(data.Msg.Text), bc.mentionResolver)
	target.Metadata = &MessageMetadata{Content: content}
	return &bridgev2.ConvertedEdit{
		ModifiedParts: []*bridgev2.ConvertedEditPart{{
			Part:    target,
			Type:    event.EventMessage,
			Content: content,
		}},
	}, nil
}

func (bc *BitrixClient) backfillReactions(reactions []bitrix.Reaction) map[int64][]*bridgev2.BackfillReaction {
	out := make(map[int64][]*bridgev2.BackfillReaction)
	for _, r := range reactions {
		for code, users := range r.ReactionUsers {
			for _, uid := range users {
				out[int64(r.MessageID)] = append(out[int64(r.MessageID)], &bridgev2.BackfillReaction{
					Sender:  bc.sender(int64(uid)),
					EmojiID: networkid.EmojiID(code),
					Emoji:   bitrixReactionEmoji(code),
				})
			}
		}
	}
	return out
}
