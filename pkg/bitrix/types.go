package bitrix

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// FlexInt accepts JSON numbers, numeric strings, false and null.
type FlexInt int64

func (f *FlexInt) UnmarshalJSON(data []byte) error {
	data = bytes.Trim(data, `"`)
	switch string(data) {
	case "", "null", "false", "true":
		*f = 0
		return nil
	}
	v, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil {
		fv, ferr := strconv.ParseFloat(string(data), 64)
		if ferr != nil {
			*f = 0
			return nil
		}
		v = int64(fv)
	}
	*f = FlexInt(v)
	return nil
}

func (f FlexInt) String() string { return strconv.FormatInt(int64(f), 10) }

// FlexString accepts JSON strings, numbers, false and null.
type FlexString string

func (f *FlexString) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*f = FlexString(s)
		return nil
	}
	switch string(data) {
	case "null", "false":
		*f = ""
	default:
		*f = FlexString(data)
	}
	return nil
}

// PHPMap is a PHP associative array, which Bitrix serializes as [] when empty.
type PHPMap[V any] map[string]V

func (p *PHPMap[V]) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || data[0] != '{' {
		*p = PHPMap[V]{}
		return nil
	}
	return json.Unmarshal(data, (*map[string]V)(p))
}

// Params are message params (FILE_ID, REPLY_ID, IS_PINNED, ...).
type Params = PHPMap[json.RawMessage]

// FlexTime accepts RFC3339 strings, unix timestamps and false.
type FlexTime struct{ time.Time }

func (f *FlexTime) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	switch s {
	case "", "null", "false":
		return nil
	}
	if ts, err := strconv.ParseInt(s, 10, 64); err == nil {
		f.Time = time.Unix(ts, 0)
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "02.01.2006 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			f.Time = t
			return nil
		}
	}
	return nil
}

type User struct {
	ID           FlexInt    `json:"id"`
	Active       bool       `json:"active"`
	Name         string     `json:"name"`
	FirstName    string     `json:"firstName"`
	LastName     string     `json:"lastName"`
	WorkPosition string     `json:"workPosition"`
	Avatar       FlexString `json:"avatar"`
	AvatarHR     FlexString `json:"avatarHr"`
	Bot          bool       `json:"bot"`
	Connector    bool       `json:"connector"`
	Extranet     bool       `json:"extranet"`
}

// UnmarshalJSON accepts both the camelCase (im.v2) and snake_case (legacy REST) user objects.
func (u *User) UnmarshalJSON(data []byte) error {
	type plain User
	var aux struct {
		plain
		FirstNameS    string     `json:"first_name"`
		LastNameS     string     `json:"last_name"`
		WorkPositionS string     `json:"work_position"`
		AvatarHRS     FlexString `json:"avatar_hr"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*u = User(aux.plain)
	if u.FirstName == "" {
		u.FirstName = aux.FirstNameS
	}
	if u.LastName == "" {
		u.LastName = aux.LastNameS
	}
	if u.WorkPosition == "" {
		u.WorkPosition = aux.WorkPositionS
	}
	if u.AvatarHR == "" {
		u.AvatarHR = aux.AvatarHRS
	}
	return nil
}

func (u *User) AvatarURL() string {
	for _, a := range []FlexString{u.AvatarHR, u.Avatar} {
		s := string(a)
		if s != "" && !strings.Contains(s, "blank.gif") {
			return s
		}
	}
	return ""
}

type File struct {
	ID          FlexInt    `json:"id"`
	ChatID      FlexInt    `json:"chatId"`
	Date        FlexTime   `json:"date"`
	Type        string     `json:"type"` // image, video, audio, file
	Name        string     `json:"name"`
	Extension   string     `json:"extension"`
	Size        int64      `json:"size"`
	AuthorID    FlexInt    `json:"authorId"`
	URLDownload string     `json:"urlDownload"`
	URLShow     string     `json:"urlShow"`
	URLPreview  string     `json:"urlPreview"`
	Image       *ImageSize `json:"image"`
}

// ImageSize is the image dimensions of a file, or false for non-image files.
type ImageSize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

func (i *ImageSize) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || data[0] != '{' {
		return nil
	}
	type plain ImageSize
	return json.Unmarshal(data, (*plain)(i))
}

// Message is the im.v2 message object. Pull events use camelCase keys (chatId, senderId),
// while im.v2.Chat.Message.list uses snake_case (chat_id, author_id); both are accepted.
type Message struct {
	ID          FlexInt         `json:"id"`
	ChatID      FlexInt         `json:"chatId"`
	AuthorID    FlexInt         `json:"authorId"`
	SenderID    FlexInt         `json:"senderId"`
	RecipientID FlexString      `json:"recipientId"`
	Date        FlexTime        `json:"date"`
	Text        string          `json:"text"`
	IsSystem    bool            `json:"isSystem"`
	System      FlexString      `json:"system"`
	UUID        FlexString      `json:"uuid"`
	TemplateID  FlexString      `json:"templateId"`
	Params      Params          `json:"params"`
	Forward     json.RawMessage `json:"forward"`
}

func (m *Message) UnmarshalJSON(data []byte) error {
	type plain Message
	var aux struct {
		plain
		ChatIDS   FlexInt `json:"chat_id"`
		AuthorIDS FlexInt `json:"author_id"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*m = Message(aux.plain)
	if m.ChatID == 0 {
		m.ChatID = aux.ChatIDS
	}
	if m.AuthorID == 0 {
		m.AuthorID = aux.AuthorIDS
	}
	if m.SenderID == 0 {
		m.SenderID = m.AuthorID
	}
	if m.AuthorID == 0 {
		m.AuthorID = m.SenderID
	}
	return nil
}

func (m *Message) Sender() int64 {
	return int64(m.AuthorID)
}

// IsSystemMessage reports service messages (pins, joins, calls...). They may still have a
// real author, e.g. "X pinned a message" is authored by X.
func (m *Message) IsSystemMessage() bool {
	if m.IsSystem || m.System == "Y" || m.System == "true" || m.AuthorID == 0 {
		return true
	}
	raw, ok := m.Params["CLASS"]
	return ok && strings.Contains(string(raw), "-system")
}

// ParamInts reads a param that may be a scalar or array of numbers/strings.
func (m *Message) ParamInts(key string) []int64 {
	raw, ok := m.Params[key]
	if !ok {
		return nil
	}
	var arr []FlexInt
	if err := json.Unmarshal(raw, &arr); err != nil {
		var single FlexInt
		if json.Unmarshal(raw, &single) != nil || single == 0 {
			return nil
		}
		arr = []FlexInt{single}
	}
	out := make([]int64, 0, len(arr))
	for _, v := range arr {
		if v != 0 {
			out = append(out, int64(v))
		}
	}
	return out
}

func (m *Message) FileIDs() []int64 { return m.ParamInts("FILE_ID") }

func (m *Message) ReplyID() int64 {
	ids := m.ParamInts("REPLY_ID")
	if len(ids) > 0 {
		return ids[0]
	}
	return 0
}

func (m *Message) IsDeleted() bool {
	raw, ok := m.Params["IS_DELETED"]
	return ok && strings.Contains(string(raw), "Y")
}

func (m *Message) IsEdited() bool {
	raw, ok := m.Params["IS_EDITED"]
	return ok && strings.Contains(string(raw), "Y")
}

type Reaction struct {
	MessageID        FlexInt           `json:"messageId"`
	ReactionCounters PHPMap[int]       `json:"reactionCounters"`
	ReactionUsers    PHPMap[[]FlexInt] `json:"reactionUsers"`
	OwnReactions     []string          `json:"ownReactions"`
}

type Chat struct {
	ID          FlexInt    `json:"id"`
	DialogID    FlexString `json:"dialogId"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Type        string     `json:"type"`        // private, chat, open, general, channel, ...
	MessageType string     `json:"messageType"` // P, C, O, ...
	Avatar      FlexString `json:"avatar"`
	Owner       FlexInt    `json:"owner"`
	UserCounter int        `json:"userCounter"`
	LastID      FlexInt    `json:"lastId"`
	Counter     int        `json:"counter"`
	DiskFolder  FlexInt    `json:"diskFolderId"`
}

func (c *Chat) IsPrivate() bool {
	return c.Type == "private" || c.MessageType == "P"
}

// MessageBatch is the common shape of im.v2.Chat.load/Message.list/Message.tail responses.
type MessageBatch struct {
	Chat        *Chat      `json:"chat"`
	Messages    []*Message `json:"messages"`
	Users       []*User    `json:"users"`
	Files       []*File    `json:"files"`
	Reactions   []Reaction `json:"reactions"`
	HasPrevPage bool       `json:"hasPrevPage"`
	HasNextPage bool       `json:"hasNextPage"`
}

func (b *MessageBatch) FileMap() map[int64]*File {
	out := make(map[int64]*File, len(b.Files))
	for _, f := range b.Files {
		out[int64(f.ID)] = f
	}
	return out
}

func (b *MessageBatch) UserMap() map[int64]*User {
	out := make(map[int64]*User, len(b.Users))
	for _, u := range b.Users {
		out[int64(u.ID)] = u
	}
	return out
}

type RecentItem struct {
	ID      FlexString `json:"id"` // dialog ID: "123" or "chat123"
	ChatID  FlexInt    `json:"chat_id"`
	Type    string     `json:"type"` // user, chat
	Title   string     `json:"title"`
	Counter int        `json:"counter"`
	Pinned  bool       `json:"pinned"`
	Unread  bool       `json:"unread"`
	Avatar  struct {
		URL string `json:"url"`
	} `json:"avatar"`
	Message struct {
		ID       FlexInt  `json:"id"`
		Text     string   `json:"text"`
		AuthorID FlexInt  `json:"author_id"`
		Date     FlexTime `json:"date"`
	} `json:"message"`
	DateUpdate FlexTime `json:"date_update"`
	User       *User    `json:"user"`
	Chat       *struct {
		ID          FlexInt         `json:"id"`
		Name        string          `json:"name"`
		Type        string          `json:"type"`
		Avatar      FlexString      `json:"avatar"`
		Owner       FlexInt         `json:"owner"`
		UserCounter int             `json:"user_counter"`
		MuteList    json.RawMessage `json:"mute_list"`
	} `json:"chat"`
}

type RecentList struct {
	Items   []*RecentItem `json:"items"`
	HasMore bool          `json:"hasMore"`
}
