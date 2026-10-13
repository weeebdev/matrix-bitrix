package connector

import (
	"context"
	"html"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/event"
)

// Per-login settings live in UserLoginMetadata and are toggled with bot commands.

func (meta *UserLoginMetadata) ShowEditsEnabled() bool {
	return meta.ShowEdits == nil || *meta.ShowEdits
}

func (meta *UserLoginMetadata) KeepDeletedEnabled() bool {
	return meta.KeepDeleted == nil || *meta.KeepDeleted
}

func boolArg(args []string) (value, ok bool) {
	if len(args) == 0 {
		return false, false
	}
	switch strings.ToLower(args[0]) {
	case "on", "yes", "true", "1", "enable":
		return true, true
	case "off", "no", "false", "0", "disable":
		return false, true
	}
	return false, false
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func settingCommand(name, description string, get func(*UserLoginMetadata) bool, set func(*UserLoginMetadata, bool), explain string) *commands.FullHandler {
	return &commands.FullHandler{
		Func: func(ce *commands.Event) {
			login := ce.User.GetDefaultLogin()
			if login == nil {
				ce.Reply("You're not logged in")
				return
			}
			meta := login.Metadata.(*UserLoginMetadata)
			value, ok := boolArg(ce.Args)
			if !ok {
				ce.Reply("%s is **%s**. Usage: `%s on|off`\n\n%s", name, onOff(get(meta)), name, explain)
				return
			}
			set(meta, value)
			if err := login.Save(ce.Ctx); err != nil {
				ce.Reply("Failed to save setting: %v", err)
				return
			}
			ce.Reply("%s is now **%s**", name, onOff(value))
		},
		Name: name,
		Help: commands.HelpMeta{
			Section:     commands.HelpSectionGeneral,
			Description: description,
			Args:        "[on|off]",
		},
		RequiresLogin: true,
	}
}

var cmdInvisible = settingCommand("invisible",
	"Don't send read receipts or typing notifications to Bitrix",
	func(m *UserLoginMetadata) bool { return m.Invisible },
	func(m *UserLoginMetadata, v bool) { m.Invisible = v },
	"When on, reading or typing in Beeper isn't reported to colleagues, and chats stay unread in "+
		"Bitrix itself. Bitrix may still show you as online while the bridge is connected.")

var cmdKeepDeleted = settingCommand("keep-deleted",
	"Keep messages in Beeper when they're deleted in Bitrix",
	func(m *UserLoginMetadata) bool { return m.KeepDeletedEnabled() },
	func(m *UserLoginMetadata, v bool) { m.KeepDeleted = &v },
	"When on, messages deleted in Bitrix stay in Beeper, struck through and marked 🗑, with attachments kept. "+
		"Messages you delete from Beeper are always removed.")

func pauseCommand(name, description string, pause bool) *commands.FullHandler {
	return &commands.FullHandler{
		Func: func(ce *commands.Event) {
			login := ce.User.GetDefaultLogin()
			if login == nil {
				ce.Reply("You're not logged in")
				return
			}
			meta := login.Metadata.(*UserLoginMetadata)
			if meta.Paused == pause {
				ce.Reply("Bridge is already %s", map[bool]string{true: "paused", false: "running"}[pause])
				return
			}
			meta.Paused = pause
			if err := login.Save(ce.Ctx); err != nil {
				ce.Reply("Failed to save: %v", err)
				return
			}
			client := login.Client.(*BitrixClient)
			if pause {
				client.Disconnect()
				client.sendPausedState()
				ce.Reply("Paused: disconnected from Bitrix. Nothing is sent or received until you send `resume`, " +
					"including after restarts.")
			} else {
				go client.Connect(login.Log.WithContext(context.Background()))
				ce.Reply("Resuming: reconnecting to Bitrix. Messages sent while paused are fetched by the next sync.")
			}
		},
		Name: name,
		Help: commands.HelpMeta{
			Section:     commands.HelpSectionGeneral,
			Description: description,
		},
		RequiresLogin: true,
	}
}

var cmdPause = pauseCommand("pause", "Disconnect from Bitrix and stop bridging until `resume` (survives restarts)", true)
var cmdResume = pauseCommand("resume", "Reconnect to Bitrix after `pause`", false)

// matrixDeletes remembers messages deleted from Matrix, so their Bitrix delete echo isn't
// mistaken for someone else's delete when keep-deleted is on.
const matrixDeleteMemory = 10 * time.Minute

func (bc *BitrixClient) rememberMatrixDelete(messageID int64) {
	bc.deletesLock.Lock()
	defer bc.deletesLock.Unlock()
	now := time.Now()
	for id, at := range bc.matrixDeletes {
		if now.Sub(at) > matrixDeleteMemory {
			delete(bc.matrixDeletes, id)
		}
	}
	bc.matrixDeletes[messageID] = now
}

func (bc *BitrixClient) wasDeletedFromMatrix(messageID int64) bool {
	bc.deletesLock.Lock()
	defer bc.deletesLock.Unlock()
	at, ok := bc.matrixDeletes[messageID]
	return ok && time.Since(at) <= matrixDeleteMemory
}

var cmdShowEdits = settingCommand("show-edits",
	"Show previous versions under edited messages",
	func(m *UserLoginMetadata) bool { return m.ShowEditsEnabled() },
	func(m *UserLoginMetadata, v bool) { m.ShowEdits = &v },
	"When on, edited messages list their earlier versions underneath (\"✏️ was: …\"), newest first. "+
		"Beeper otherwise only shows the latest version.")

const maxEditHistory = 10

// withEditHistory returns a copy of content with previous versions appended, newest first.
func withEditHistory(orig *event.MessageEventContent, history []string) *event.MessageEventContent {
	c := *orig
	formatted := c.FormattedBody
	if c.Format != event.FormatHTML || formatted == "" {
		formatted = strings.ReplaceAll(html.EscapeString(c.Body), "\n", "<br/>")
	}
	var plain, rich strings.Builder
	plain.WriteString(c.Body)
	rich.WriteString(formatted)
	for _, old := range history {
		plain.WriteString("\n✏️ was: " + old)
		rich.WriteString(`<br/><font color="#888888"><sub>✏️ was: ` +
			strings.ReplaceAll(html.EscapeString(old), "\n", "<br/>") + "</sub></font>")
	}
	c.Body = plain.String()
	c.Format = event.FormatHTML
	c.FormattedBody = rich.String()
	return &c
}
