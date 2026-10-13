package connector

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"

	"github.com/weeebdev/mautrix-bitrix/pkg/bitrix"
)

type BitrixClient struct {
	Main      *BitrixConnector
	UserLogin *bridgev2.UserLogin
	client    *bitrix.Client

	stopPull  context.CancelFunc
	loggedIn  bool
	stateLock sync.Mutex

	// chatID -> dialogID cache for push events that only carry chatId
	dialogLock sync.RWMutex
	dialogs    map[int64]string
}

var (
	_ bridgev2.NetworkAPI                    = (*BitrixClient)(nil)
	_ bridgev2.EditHandlingNetworkAPI        = (*BitrixClient)(nil)
	_ bridgev2.RedactionHandlingNetworkAPI   = (*BitrixClient)(nil)
	_ bridgev2.ReactionHandlingNetworkAPI    = (*BitrixClient)(nil)
	_ bridgev2.ReadReceiptHandlingNetworkAPI = (*BitrixClient)(nil)
	_ bridgev2.TypingHandlingNetworkAPI      = (*BitrixClient)(nil)
	_ bridgev2.BackfillingNetworkAPI         = (*BitrixClient)(nil)
)

func newBitrixClient(main *BitrixConnector, login *bridgev2.UserLogin, client *bitrix.Client) *BitrixClient {
	bc := &BitrixClient{
		Main:      main,
		UserLogin: login,
		client:    client,
		dialogs:   make(map[int64]string),
	}
	client.OnSessionChange = bc.saveSession
	return bc
}

func (bc *BitrixClient) saveSession() {
	meta := bc.UserLogin.Metadata.(*UserLoginMetadata)
	meta.Cookies = bc.client.Cookies()
	if err := bc.UserLogin.Save(context.Background()); err != nil {
		bc.UserLogin.Log.Err(err).Msg("Failed to save session cookies")
	}
}

func (bc *BitrixClient) Connect(ctx context.Context) {
	log := bc.UserLogin.Log
	bc.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnecting})
	if err := bc.client.EnsureSession(ctx); err != nil {
		log.Err(err).Msg("Failed to establish Bitrix session")
		if errors.Is(err, bitrix.ErrInvalidLogin) || errors.Is(err, bitrix.ErrNotLoggedIn) {
			bc.UserLogin.BridgeState.Send(status.BridgeState{
				StateEvent: status.StateBadCredentials,
				Error:      "bitrix-invalid-auth",
				Message:    "Bitrix session expired and re-login failed, please log in again",
			})
		} else {
			bc.UserLogin.BridgeState.Send(status.BridgeState{
				StateEvent: status.StateTransientDisconnect,
				Error:      "bitrix-connect-failed",
				Message:    err.Error(),
			})
		}
		return
	}
	bc.stateLock.Lock()
	bc.loggedIn = true
	if bc.stopPull != nil {
		bc.stopPull()
	}
	pullCtx, cancel := context.WithCancel(log.WithContext(context.Background()))
	bc.stopPull = cancel
	bc.stateLock.Unlock()

	pull := bc.client.NewPullClient(log.With().Str("component", "pull").Logger(), bc.handlePullEvent)
	pull.OnConnect = func() {
		bc.UserLogin.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnected})
		go bc.syncRecentChats(pullCtx)
	}
	pull.OnDisconnect = func(err error) {
		msg := "push connection lost"
		if err != nil {
			msg = err.Error()
		}
		bc.UserLogin.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateTransientDisconnect,
			Error:      "bitrix-pull-disconnected",
			Message:    msg,
		})
		// The push config fetch can fail because the session expired; refresh it before retrying.
		if refreshErr := bc.client.EnsureSession(pullCtx); errors.Is(refreshErr, bitrix.ErrInvalidLogin) || errors.Is(refreshErr, bitrix.ErrNotLoggedIn) {
			bc.UserLogin.BridgeState.Send(status.BridgeState{
				StateEvent: status.StateBadCredentials,
				Error:      "bitrix-invalid-auth",
				Message:    "Bitrix session expired and re-login failed, please log in again",
			})
			cancel()
		}
	}
	go pull.Run(pullCtx)
}

func (bc *BitrixClient) Disconnect() {
	bc.stateLock.Lock()
	defer bc.stateLock.Unlock()
	if bc.stopPull != nil {
		bc.stopPull()
		bc.stopPull = nil
	}
}

func (bc *BitrixClient) IsLoggedIn() bool {
	return bc.loggedIn
}

func (bc *BitrixClient) LogoutRemote(ctx context.Context) {
	bc.Disconnect()
	meta := bc.UserLogin.Metadata.(*UserLoginMetadata)
	meta.Password = ""
	meta.Cookies = nil
}

func (bc *BitrixClient) IsThisUser(ctx context.Context, userID networkid.UserID) bool {
	return userID == makeUserID(bc.client.UserID)
}

func (bc *BitrixClient) myUserID() networkid.UserID {
	return makeUserID(bc.client.UserID)
}

func (bc *BitrixClient) portalKey(dialogID string) networkid.PortalKey {
	return networkid.PortalKey{ID: networkid.PortalID(dialogID), Receiver: bc.UserLogin.ID}
}

func (bc *BitrixClient) rememberDialog(chatID int64, dialogID string) {
	if chatID == 0 || dialogID == "" {
		return
	}
	bc.dialogLock.Lock()
	bc.dialogs[chatID] = dialogID
	bc.dialogLock.Unlock()
}

func (bc *BitrixClient) dialogForChat(chatID int64) string {
	bc.dialogLock.RLock()
	defer bc.dialogLock.RUnlock()
	return bc.dialogs[chatID]
}

func (bc *BitrixClient) sender(userID int64) bridgev2.EventSender {
	return bridgev2.EventSender{
		IsFromMe:    userID == bc.client.UserID,
		SenderLogin: ptrIf(userID == bc.client.UserID, bc.UserLogin.ID),
		Sender:      makeUserID(userID),
	}
}

func ptrIf(cond bool, id networkid.UserLoginID) networkid.UserLoginID {
	if cond {
		return id
	}
	return ""
}

// chatIDFor returns the numeric chat ID behind a portal, loading it if unknown.
func (bc *BitrixClient) chatIDFor(ctx context.Context, portal *bridgev2.Portal) (int64, error) {
	meta := portal.Metadata.(*PortalMetadata)
	if meta.ChatID != 0 {
		return meta.ChatID, nil
	}
	batch, err := bc.client.LoadChat(ctx, string(portal.ID), 0)
	if err != nil {
		return 0, err
	} else if batch.Chat == nil || batch.Chat.ID == 0 {
		return 0, fmt.Errorf("chat %s not found", portal.ID)
	}
	meta.ChatID = int64(batch.Chat.ID)
	meta.DiskFolderID = int64(batch.Chat.DiskFolder)
	bc.rememberDialog(meta.ChatID, string(portal.ID))
	if portal.MXID != "" {
		_ = portal.Save(ctx)
	}
	return meta.ChatID, nil
}

func (bc *BitrixClient) syncRecentChats(ctx context.Context) {
	log := zerolog.Ctx(ctx)
	recent, err := bc.client.RecentList(ctx, bc.Main.Config.InitialChatLimit, "")
	if err != nil {
		log.Err(err).Msg("Failed to fetch recent chats")
		return
	}
	for _, item := range recent.Items {
		dialogID := string(item.ID)
		// Recent also lists colleagues you've never talked to (e.g. new employees);
		// rooms for those are created when the first real message arrives.
		if dialogID == "" || item.Type == "notification" || item.Message.ID == 0 {
			continue
		}
		bc.rememberDialog(int64(item.ChatID), dialogID)
		ts := item.Message.Date.Time
		if ts.IsZero() {
			ts = item.DateUpdate.Time
		}
		bc.UserLogin.Bridge.QueueRemoteEvent(bc.UserLogin, &simplevent.ChatResync{
			EventMeta: simplevent.EventMeta{
				Type: bridgev2.RemoteEventChatResync,
				LogContext: func(c zerolog.Context) zerolog.Context {
					return c.Str("dialog_id", dialogID)
				},
				PortalKey:    bc.portalKey(dialogID),
				CreatePortal: true,
			},
			GetChatInfoFunc:     bc.GetChatInfo,
			LatestMessageTS:     ts,
			BundledBackfillData: &backfillHint{Unread: item.Counter > 0 || item.Unread},
		})
		// Mirror Bitrix's read state: chats read elsewhere (or before the bridge existed) are
		// marked read on Matrix up to their last message.
		if item.Counter == 0 && !item.Unread {
			bc.UserLogin.Bridge.QueueRemoteEvent(bc.UserLogin, &simplevent.Receipt{
				EventMeta: simplevent.EventMeta{
					Type:      bridgev2.RemoteEventReadReceipt,
					PortalKey: bc.portalKey(dialogID),
					Sender:    bc.sender(bc.client.UserID),
					Timestamp: ts,
				},
				LastTarget: makeMessageID(int64(item.Message.ID)),
				ReadUpTo:   time.Now(),
			})
		}
	}
	log.Info().Int("count", len(recent.Items)).Msg("Queued recent chat sync")
}

func (bc *BitrixClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	dialogID := string(portal.ID)
	if !bitrix.IsChatDialog(dialogID) {
		// DMs don't need a server round-trip, and may not exist on Bitrix yet when started from
		// Matrix: the chat is created when the first message is sent.
		return bc.dmChatInfo(dialogID), nil
	}
	batch, err := bc.client.LoadChat(ctx, dialogID, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to load chat: %w", err)
	} else if batch.Chat == nil {
		return nil, fmt.Errorf("chat %s not found", dialogID)
	}
	chat := batch.Chat
	chatID := int64(chat.ID)
	folderID := int64(chat.DiskFolder)
	bc.rememberDialog(chatID, dialogID)
	info := &bridgev2.ChatInfo{
		CanBackfill: true,
		ExtraUpdates: func(ctx context.Context, p *bridgev2.Portal) bool {
			meta := p.Metadata.(*PortalMetadata)
			changed := meta.ChatID != chatID || (folderID != 0 && meta.DiskFolderID != folderID)
			meta.ChatID = chatID
			if folderID != 0 {
				meta.DiskFolderID = folderID
			}
			return changed
		},
	}
	info.Type = ptr.Ptr(database.RoomTypeDefault)
	info.Name = ptr.Ptr(chat.Name)
	info.Topic = ptr.Ptr(chat.Description)
	if avatarURL := string(chat.Avatar); avatarURL != "" {
		info.Avatar = bc.makeAvatar(avatarURL)
	} else {
		info.Avatar = &bridgev2.Avatar{Remove: true}
	}
	users, err := bc.client.DialogUsers(ctx, dialogID)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to fetch chat members")
	}
	memberList := &bridgev2.ChatMemberList{
		IsFull:           err == nil && len(users) <= bc.Main.Config.MaxMemberSync,
		TotalMemberCount: max(chat.UserCounter, len(users)),
	}
	hasSelf := false
	for i, user := range users {
		if i >= bc.Main.Config.MaxMemberSync {
			break
		}
		uid := int64(user.ID)
		member := bridgev2.ChatMember{
			EventSender: bc.sender(uid),
			Membership:  event.MembershipJoin,
			UserInfo:    bc.userInfo(user),
		}
		if uid == int64(chat.Owner) {
			member.PowerLevel = ptr.Ptr(95)
		}
		if uid == bc.client.UserID {
			hasSelf = true
		}
		memberList.Members = append(memberList.Members, member)
	}
	if !hasSelf {
		memberList.Members = append(memberList.Members, bridgev2.ChatMember{
			EventSender: bc.sender(bc.client.UserID),
			Membership:  event.MembershipJoin,
		})
	}
	info.Members = memberList
	return info, nil
}

func (bc *BitrixClient) dmChatInfo(dialogID string) *bridgev2.ChatInfo {
	otherID := makeUserID(parseUserID(networkid.UserID(dialogID)))
	members := []bridgev2.ChatMember{
		{EventSender: bc.sender(bc.client.UserID), Membership: event.MembershipJoin},
	}
	if otherID != bc.myUserID() {
		members = append(members, bridgev2.ChatMember{
			EventSender: bridgev2.EventSender{Sender: otherID},
			Membership:  event.MembershipJoin,
		})
	}
	return &bridgev2.ChatInfo{
		CanBackfill: true,
		Type:        ptr.Ptr(database.RoomTypeDM),
		Members: &bridgev2.ChatMemberList{
			IsFull:      true,
			OtherUserID: otherID,
			Members:     members,
		},
	}
}

func (bc *BitrixClient) makeAvatar(avatarURL string) *bridgev2.Avatar {
	return &bridgev2.Avatar{
		ID: networkid.AvatarID(avatarURL),
		Get: func(ctx context.Context) ([]byte, error) {
			data, _, err := bc.client.Download(ctx, avatarURL)
			return data, err
		},
	}
}

func (bc *BitrixClient) userInfo(user *bitrix.User) *bridgev2.UserInfo {
	if user == nil || user.Name == "" {
		return nil
	}
	info := &bridgev2.UserInfo{
		Name:  ptr.Ptr(user.Name),
		IsBot: ptr.Ptr(user.Bot),
	}
	if avatarURL := user.AvatarURL(); avatarURL != "" {
		info.Avatar = bc.makeAvatar(avatarURL)
	} else {
		info.Avatar = &bridgev2.Avatar{Remove: true}
	}
	return info
}

func (bc *BitrixClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	user, err := bc.client.GetUser(ctx, parseUserID(ghost.ID))
	if err != nil {
		return nil, err
	}
	info := bc.userInfo(user)
	if info == nil {
		info = &bridgev2.UserInfo{Name: ptr.Ptr("Bitrix user " + string(ghost.ID))}
	}
	return info, nil
}

func (bc *BitrixClient) GetCapabilities(ctx context.Context, portal *bridgev2.Portal) *event.RoomFeatures {
	fileFeature := &event.FileFeatures{
		MimeTypes: map[string]event.CapabilitySupportLevel{"*/*": event.CapLevelFullySupported},
		Caption:   event.CapLevelFullySupported,
		MaxSize:   100 << 20,
	}
	return &event.RoomFeatures{
		ID: "kz.adil.bitrix.capabilities.2026_10_12",
		Formatting: event.FormattingFeatureMap{
			event.FmtBold:          event.CapLevelFullySupported,
			event.FmtItalic:        event.CapLevelFullySupported,
			event.FmtUnderline:     event.CapLevelFullySupported,
			event.FmtStrikethrough: event.CapLevelFullySupported,
			event.FmtInlineCode:    event.CapLevelPartialSupport,
			event.FmtCodeBlock:     event.CapLevelFullySupported,
			event.FmtBlockquote:    event.CapLevelPartialSupport,
			event.FmtInlineLink:    event.CapLevelFullySupported,
			event.FmtUserLink:      event.CapLevelFullySupported,
		},
		File: event.FileFeatureMap{
			event.MsgImage: fileFeature,
			event.MsgFile:  fileFeature,
			event.MsgVideo: fileFeature,
			event.MsgAudio: fileFeature,
		},
		MaxTextLength:       20000,
		Reply:               event.CapLevelFullySupported,
		Edit:                event.CapLevelFullySupported,
		Delete:              event.CapLevelFullySupported,
		Reaction:            event.CapLevelPartialSupport,
		ReactionCount:       1,
		AllowedReactions:    allowedReactions(),
		ReadReceipts:        true,
		TypingNotifications: true,
	}
}

func parseDialogInt(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}
