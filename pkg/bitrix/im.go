package bitrix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// DialogID is "123" for a private chat with user 123, or "chat456" for a group chat.
func ChatDialogID(chatID int64) string { return "chat" + strconv.FormatInt(chatID, 10) }

func IsChatDialog(dialogID string) bool { return strings.HasPrefix(dialogID, "chat") }

func (c *Client) RecentList(ctx context.Context, limit int, lastMessageDate string) (*RecentList, error) {
	var out RecentList
	params := map[string]any{
		"SKIP_OPENLINES":    "Y",
		"LIMIT":             limit,
		"GET_ORIGINAL_TEXT": "Y",
	}
	if lastMessageDate != "" {
		params["LAST_MESSAGE_DATE"] = lastMessageDate
	}
	return &out, c.REST(ctx, "im.recent.list", params, &out)
}

// LoadChat returns chat metadata plus the latest messages.
func (c *Client) LoadChat(ctx context.Context, dialogID string, messageLimit int) (*MessageBatch, error) {
	var out MessageBatch
	return &out, c.Action(ctx, "im.v2.Chat.load", map[string]any{
		"dialogId":     dialogID,
		"messageLimit": messageLimit,
	}, &out)
}

// MessageTail fetches messages relative to lastID. desc=true pages backwards (older messages).
func (c *Client) MessageTail(ctx context.Context, chatID, lastID int64, limit int, desc bool) (*MessageBatch, error) {
	order := "ASC"
	if desc {
		order = "DESC"
	}
	params := map[string]any{
		"chatId": chatID,
		"limit":  limit,
		"order":  map[string]any{"id": order},
	}
	if lastID > 0 {
		params["filter"] = map[string]any{"lastId": lastID}
	}
	var out MessageBatch
	return &out, c.Action(ctx, "im.v2.Chat.Message.tail", params, &out)
}

func (c *Client) MessageList(ctx context.Context, chatID int64, limit int) (*MessageBatch, error) {
	var out MessageBatch
	return &out, c.Action(ctx, "im.v2.Chat.Message.list", map[string]any{
		"chatId": chatID,
		"limit":  limit,
	}, &out)
}

type SendResult struct {
	ID FlexInt `json:"id"`
}

// SendMessage posts a text message. templateID is echoed back in the push event (message.templateId).
func (c *Client) SendMessage(ctx context.Context, dialogID, text string, replyID int64, templateID string) (int64, error) {
	if templateID == "" {
		templateID = uuid.NewString()
	}
	fields := map[string]any{
		"message":    text,
		"templateId": templateID,
	}
	if replyID != 0 {
		fields["replyId"] = replyID
	}
	var out SendResult
	err := c.Action(ctx, "im.v2.Chat.Message.send", map[string]any{
		"dialogId": dialogID,
		"fields":   fields,
	}, &out)
	return int64(out.ID), err
}

func (c *Client) EditMessage(ctx context.Context, messageID int64, text string) error {
	return c.Action(ctx, "im.v2.Chat.Message.update", map[string]any{
		"id":     messageID,
		"fields": map[string]any{"message": text},
	}, nil)
}

func (c *Client) DeleteMessage(ctx context.Context, messageID int64) error {
	return c.Action(ctx, "im.v2.Chat.Message.delete", map[string]any{"id": messageID}, nil)
}

func (c *Client) ReadMessages(ctx context.Context, chatID int64, ids []int64) error {
	return c.Action(ctx, "im.v2.Chat.Message.read", map[string]any{
		"chatId":     chatID,
		"ids":        ids,
		"actionUuid": uuid.NewString(),
	}, nil)
}

func (c *Client) ReadDialog(ctx context.Context, dialogID string, upToMessageID int64) error {
	params := map[string]any{"DIALOG_ID": dialogID}
	if upToMessageID != 0 {
		params["MESSAGE_ID"] = upToMessageID
	}
	return c.REST(ctx, "im.dialog.read", params, nil)
}

func (c *Client) Writing(ctx context.Context, dialogID string) error {
	return c.REST(ctx, "im.dialog.writing", map[string]any{"DIALOG_ID": dialogID}, nil)
}

func (c *Client) AddReaction(ctx context.Context, messageID int64, reaction string) error {
	return c.REST(ctx, "im.v2.Chat.Message.Reaction.add", map[string]any{
		"messageId": messageID,
		"reaction":  reaction,
	}, nil)
}

func (c *Client) DeleteReaction(ctx context.Context, messageID int64, reaction string) error {
	return c.REST(ctx, "im.v2.Chat.Message.Reaction.delete", map[string]any{
		"messageId": messageID,
		"reaction":  reaction,
	}, nil)
}

// SearchUsers runs the messenger's "new chat" people search (same entity selector as the web UI).
func (c *Client) SearchUsers(ctx context.Context, query string) ([]*User, error) {
	var out struct {
		Dialog struct {
			Items []struct {
				ID         FlexString `json:"id"`
				EntityType string     `json:"entityType"`
				CustomData *User      `json:"customData"`
			} `json:"items"`
		} `json:"dialog"`
	}
	err := c.ActionJSON(ctx, "ui.entityselector.doSearch", map[string]any{
		"dialog": map[string]any{
			"entities": []any{map[string]any{
				"id":            "im-recent-v2",
				"dynamicLoad":   true,
				"dynamicSearch": true,
				"options":       map[string]any{"withChatByUsers": false, "exclude": []any{}},
			}},
			"preselectedItems":      []any{},
			"clearUnavailableItems": false,
			"context":               "IM_CHAT_SEARCH",
			"id":                    "im-chat-search",
		},
		"searchQuery": map[string]any{
			"queryWords": strings.Fields(query),
			"query":      query,
		},
	}, &out)
	if err != nil {
		return nil, err
	}
	users := make([]*User, 0, len(out.Dialog.Items))
	for _, item := range out.Dialog.Items {
		if item.EntityType == "im-user" && item.CustomData != nil && item.CustomData.ID != 0 {
			users = append(users, item.CustomData)
		}
	}
	return users, nil
}

func (c *Client) GetUser(ctx context.Context, userID int64) (*User, error) {
	var out User
	return &out, c.REST(ctx, "im.user.get", map[string]any{"ID": userID}, &out)
}

func (c *Client) DialogUsers(ctx context.Context, dialogID string) ([]*User, error) {
	var all []*User
	for offset := 0; offset < 5000; offset += 50 {
		var page []*User
		err := c.REST(ctx, "im.dialog.users.list", map[string]any{
			"DIALOG_ID":                  dialogID,
			"SKIP_EXTERNAL":              "Y",
			"SKIP_EXTERNAL_EXCEPT_TYPES": "",
			"OFFSET":                     offset,
		}, &page)
		if err != nil {
			return all, err
		}
		all = append(all, page...)
		if len(page) < 50 {
			break
		}
	}
	return all, nil
}

const uploadChunkSize = 5 << 20

type uploadResponse struct {
	Token string `json:"token"`
	Done  bool   `json:"done"`
	File  struct {
		CustomData struct {
			FileID FlexInt `json:"fileId"`
		} `json:"customData"`
	} `json:"file"`
}

// uploadToFolder streams a file into a disk folder through the same ui.fileuploader controller
// the web messenger uses. Large files are sent in Content-Range chunks tied together by a token.
func (c *Client) uploadToFolder(ctx context.Context, folderID int64, name, mimeType string, data []byte) (int64, error) {
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	options := fmt.Sprintf(`{"folderId":%d}`, folderID)
	token := ""
	total := len(data)
	for offset := 0; ; offset += uploadChunkSize {
		end := min(offset+uploadChunkSize, total)
		path := "/bitrix/services/main/ajax.php?controller=disk.uf.integration.diskUploaderController" +
			"&controllerOptions=" + url.QueryEscape(options) + "&token=" + url.QueryEscape(token) + "&action=ui.fileuploader.upload"
		req, err := c.newRequest(ctx, http.MethodPost, path, bytes.NewReader(data[offset:end]))
		if err != nil {
			return 0, err
		}
		c.lock.RLock()
		req.Header.Set("X-Bitrix-Csrf-Token", c.SessID)
		req.Header.Set("X-Bitrix-Site-Id", c.SiteID)
		c.lock.RUnlock()
		req.Header.Set("Bx-Ajax", "true")
		req.Header.Set("Content-Type", mimeType)
		req.Header.Set("X-Upload-Content-Name", url.PathEscape(name))
		if total > uploadChunkSize {
			req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, end-1, total))
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return 0, err
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return 0, err
		}
		var ar ajaxResponse
		if err = json.Unmarshal(body, &ar); err != nil {
			return 0, fmt.Errorf("upload: HTTP %d, invalid JSON: %.200s", resp.StatusCode, body)
		} else if ar.Status != "success" {
			e := &Error{Method: "ui.fileuploader.upload", Code: "unknown", Msg: string(body)}
			if len(ar.Errors) > 0 {
				e.Code, e.Msg = fmt.Sprint(ar.Errors[0].Code), ar.Errors[0].Message
			}
			return 0, e
		}
		var ur uploadResponse
		if err = json.Unmarshal(ar.Data, &ur); err != nil {
			return 0, fmt.Errorf("upload: failed to parse data: %w", err)
		}
		token = ur.Token
		if ur.Done || end >= total {
			if ur.File.CustomData.FileID == 0 {
				return 0, fmt.Errorf("upload finished without a file ID: %.300s", ar.Data)
			}
			return int64(ur.File.CustomData.FileID), nil
		}
	}
}

// UploadFile uploads a file into the chat's disk folder and posts it as a message.
// The resulting message isn't returned by the API; it arrives via push with the given templateID.
// Returns the disk file ID, which is what message params FILE_ID reference.
// beforeCommit is called with the disk file ID right before the message is created.
func (c *Client) UploadFile(ctx context.Context, chatID, folderID int64, name, mimeType string, data []byte, caption, templateID string, asFile bool, beforeCommit func(fileID int64)) (int64, error) {
	if folderID == 0 {
		var folder struct {
			ID FlexInt `json:"ID"`
		}
		if err := c.REST(ctx, "im.disk.folder.get", map[string]any{"CHAT_ID": chatID}, &folder); err != nil {
			return 0, fmt.Errorf("failed to get chat folder: %w", err)
		}
		folderID = int64(folder.ID)
	}
	var fileID int64
	err := c.withRetry(ctx, func() (err error) {
		fileID, err = c.uploadToFolder(ctx, folderID, name, mimeType, data)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("failed to upload file: %w", err)
	}
	params := map[string]any{
		"chat_id":     chatID,
		"message":     caption,
		"template_id": templateID,
		"upload_id":   []int64{fileID},
	}
	if asFile {
		params["as_file"] = "Y"
	}
	if beforeCommit != nil {
		beforeCommit(fileID)
	}
	if err = c.REST(ctx, "im.disk.file.commit", params, nil); err != nil {
		return fileID, fmt.Errorf("failed to commit file: %w", err)
	}
	return fileID, nil
}

type PullConfig struct {
	Channels map[string]struct {
		ID    string   `json:"id"`
		Start FlexTime `json:"start"`
		End   FlexTime `json:"end"`
		Type  string   `json:"type"`
	} `json:"channels"`
	Server struct {
		Version         int        `json:"version"`
		ServerEnabled   bool       `json:"server_enabled"`
		Mode            string     `json:"mode"`
		Hostname        string     `json:"hostname"`
		LongPolling     string     `json:"long_polling"`
		LongPollingSec  string     `json:"long_pooling_secure"`
		WebSocketEnable bool       `json:"websocket_enabled"`
		WebSocket       string     `json:"websocket"`
		WebSocketSecure string     `json:"websocket_secure"`
		ConfigTimestamp FlexString `json:"config_timestamp"`
	} `json:"server"`
	API struct {
		RevisionWeb FlexInt `json:"revision_web"`
	} `json:"api"`
	ClientID string `json:"clientId"`
	JWT      string `json:"jwt"`
}

func (c *Client) GetPullConfig(ctx context.Context) (*PullConfig, error) {
	var out PullConfig
	return &out, c.REST(ctx, "pull.config.get", map[string]any{"CACHE": "N"}, &out)
}
