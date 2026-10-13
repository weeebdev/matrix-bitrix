package connector

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"
	"strings"

	"go.mau.fi/util/configupgrade"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/adil/mautrix-bitrix/pkg/bitrix"
)

type BitrixConnector struct {
	br     *bridgev2.Bridge
	Config Config
}

var _ bridgev2.NetworkConnector = (*BitrixConnector)(nil)

//go:embed example-config.yaml
var ExampleConfig string

type Config struct {
	// Portal URL pre-filled in the login form (e.g. https://portal.example.com).
	DefaultPortalURL string `yaml:"default_portal_url"`
	// How many recent chats to sync when connecting.
	InitialChatLimit int `yaml:"initial_chat_limit"`
	// Max members to sync for group chats (large "general" chats can have thousands).
	MaxMemberSync int `yaml:"max_member_sync"`
}

func upgradeConfig(helper configupgrade.Helper) {
	helper.Copy(configupgrade.Str, "default_portal_url")
	helper.Copy(configupgrade.Int, "initial_chat_limit")
	helper.Copy(configupgrade.Int, "max_member_sync")
}

func (bc *BitrixConnector) GetConfig() (example string, data any, upgrader configupgrade.Upgrader) {
	return ExampleConfig, &bc.Config, configupgrade.SimpleUpgrader(upgradeConfig)
}

func (bc *BitrixConnector) Init(bridge *bridgev2.Bridge) {
	bc.br = bridge
	if bc.Config.InitialChatLimit == 0 {
		bc.Config.InitialChatLimit = 30
	}
	if bc.Config.MaxMemberSync == 0 {
		bc.Config.MaxMemberSync = 200
	}
}

func (bc *BitrixConnector) Start(ctx context.Context) error {
	return nil
}

func (bc *BitrixConnector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName: "Bitrix24",
		NetworkURL:  "https://www.bitrix24.com",
		// assets/bitrix24.png, uploaded to the Beeper media repo
		NetworkIcon:          "mxc://local.beeper.com/weeebdev_492sCiEpxNtUNhsVI08GUdH1gdk3vUFUkTJBO7mF10Fq1Em6lmY2AFcK5fGaxhmm",
		NetworkID:            "bitrix",
		BeeperBridgeType:     "github.com/adil/mautrix-bitrix",
		DefaultPort:          29340,
		DefaultCommandPrefix: "!bitrix",
	}
}

func (bc *BitrixConnector) GetBridgeInfoVersion() (info, capabilities int) {
	return 2, 2
}

func (bc *BitrixConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{}
}

type UserLoginMetadata struct {
	PortalURL string            `json:"portal_url"`
	Login     string            `json:"login"`
	Password  string            `json:"password,omitempty"`
	UserID    int64             `json:"user_id"`
	Cookies   map[string]string `json:"cookies"`
}

type PortalMetadata struct {
	ChatID       int64 `json:"chat_id,omitempty"`
	DiskFolderID int64 `json:"disk_folder_id,omitempty"`
}

func (bc *BitrixConnector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		Portal:    func() any { return &PortalMetadata{} },
		Ghost:     nil,
		Message:   nil,
		Reaction:  nil,
		UserLogin: func() any { return &UserLoginMetadata{} },
	}
}

func (bc *BitrixConnector) LoadUserLogin(ctx context.Context, login *bridgev2.UserLogin) error {
	meta := login.Metadata.(*UserLoginMetadata)
	client, err := bitrix.NewClient(meta.PortalURL)
	if err != nil {
		return err
	}
	client.Login, client.Password = meta.Login, meta.Password
	client.UserID = meta.UserID
	client.SetCookies(meta.Cookies)
	login.Client = newBitrixClient(bc, login, client)
	return nil
}

func makeUserLoginID(host string, userID int64) networkid.UserLoginID {
	return networkid.UserLoginID(fmt.Sprintf("%s:%d", host, userID))
}

func makeUserID(userID int64) networkid.UserID {
	return networkid.UserID(strconv.FormatInt(userID, 10))
}

func parseUserID(userID networkid.UserID) int64 {
	v, _ := strconv.ParseInt(string(userID), 10, 64)
	return v
}

func makeMessageID(messageID int64) networkid.MessageID {
	return networkid.MessageID(strconv.FormatInt(messageID, 10))
}

func parseMessageID(messageID networkid.MessageID) int64 {
	v, _ := strconv.ParseInt(string(messageID), 10, 64)
	return v
}

func hostOf(portalURL string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(portalURL, "https://"), "http://")
	return strings.TrimRight(strings.SplitN(s, "/", 2)[0], "/")
}
