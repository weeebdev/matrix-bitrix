package connector

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"

	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
)

// ---- Bitrix BBCode -> Matrix ----

type bbRule struct {
	re   *regexp.Regexp
	html string
	text string
}

func ci(pattern string) *regexp.Regexp { return regexp.MustCompile(`(?is)` + pattern) }

var (
	reUserMention = ci(`\[USER=(\d+)(?:\s+[^\]]*)?\](.*?)\[/USER\]`)
	reOldQuote    = regexp.MustCompile(`(?s)-{20,}\n(.*?)\n-{20,}`)
	reURLNamed    = ci(`\[URL=([^\]]+)\](.*?)\[/URL\]`)
	reURLPlain    = ci(`\[URL\](.*?)\[/URL\]`)

	bbRules = []bbRule{
		{ci(`\[B\](.*?)\[/B\]`), "<strong>$1</strong>", "$1"},
		{ci(`\[I\](.*?)\[/I\]`), "<em>$1</em>", "$1"},
		{ci(`\[U\](.*?)\[/U\]`), "<u>$1</u>", "$1"},
		{ci(`\[S\](.*?)\[/S\]`), "<del>$1</del>", "$1"},
		{ci(`\[CODE\]\n?(.*?)\[/CODE\]`), "<pre><code>$1</code></pre>", "$1"},
		{ci(`\[QUOTE\]\n?(.*?)\[/QUOTE\]\n?`), "<blockquote>$1</blockquote>", "> $1\n"},
		{ci(`\[CHAT=\d+\](.*?)\[/CHAT\]`), "$1", "$1"},
		{ci(`\[(?:SEND|PUT|CALL|CONTEXT|COLOR|SIZE|FONT|DIALOG|TIMESTAMP)(?:=[^\]]*)?\](.*?)\[/(?:SEND|PUT|CALL|CONTEXT|COLOR|SIZE|FONT|DIALOG|TIMESTAMP)\]`), "$1", "$1"},
		{ci(`\[/?(?:LEFT|RIGHT|CENTER|JUSTIFY|P)\]`), "", ""},
		{ci(`\[(?:DISK|ICON|ATTACH)=[^\]]*\]`), "", ""},
		{ci(`\[BR\]`), "\n", "\n"},
	}
)

// bbcodeToMatrix converts Bitrix message text to a Matrix text content.
func bbcodeToMatrix(text string, mention func(userID string) (id.UserID, bool)) *event.MessageEventContent {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	plain := text
	htmlText := html.EscapeString(text)
	mentions := &event.Mentions{}
	hasHTML := false

	htmlText = reUserMention.ReplaceAllStringFunc(htmlText, func(s string) string {
		m := reUserMention.FindStringSubmatch(s)
		if mxid, ok := mention(m[1]); ok {
			mentions.Add(mxid)
			hasHTML = true
			return fmt.Sprintf(`<a href="%s">%s</a>`, mxid.URI().MatrixToURL(), m[2])
		}
		return m[2]
	})
	plain = reUserMention.ReplaceAllString(plain, "$2")

	htmlText = reURLNamed.ReplaceAllStringFunc(htmlText, func(s string) string {
		m := reURLNamed.FindStringSubmatch(s)
		hasHTML = true
		return fmt.Sprintf(`<a href="%s">%s</a>`, m[1], m[2])
	})
	plain = reURLNamed.ReplaceAllStringFunc(plain, func(s string) string {
		m := reURLNamed.FindStringSubmatch(s)
		if m[1] == m[2] {
			return m[1]
		}
		return fmt.Sprintf("%s (%s)", m[2], m[1])
	})
	htmlText = reURLPlain.ReplaceAllString(htmlText, "$1")
	plain = reURLPlain.ReplaceAllString(plain, "$1")

	for _, rule := range bbRules {
		before := htmlText
		htmlText = rule.re.ReplaceAllString(htmlText, rule.html)
		if before != htmlText && rule.html != "$1" && rule.html != "" && rule.html != "\n" {
			hasHTML = true
		}
		plain = rule.re.ReplaceAllString(plain, rule.text)
	}
	if reOldQuote.MatchString(htmlText) {
		hasHTML = true
		htmlText = reOldQuote.ReplaceAllString(htmlText, "<blockquote>$1</blockquote>")
		plain = reOldQuote.ReplaceAllStringFunc(plain, func(s string) string {
			m := reOldQuote.FindStringSubmatch(s)
			return "> " + strings.ReplaceAll(m[1], "\n", "\n> ")
		})
	}

	content := &event.MessageEventContent{
		MsgType:  event.MsgText,
		Body:     html.UnescapeString(strings.TrimSpace(plain)),
		Mentions: mentions,
	}
	if hasHTML {
		content.Format = event.FormatHTML
		content.FormattedBody = strings.ReplaceAll(strings.TrimSpace(htmlText), "\n", "<br/>")
	}
	return content
}

// ---- Matrix -> Bitrix BBCode ----

func wrap(tag string) format.TextConverter {
	return func(s string, _ format.Context) string {
		return "[" + tag + "]" + s + "[/" + tag + "]"
	}
}

func (bc *BitrixClient) matrixToBBCode(ctx context.Context, content *event.MessageEventContent) string {
	if content.Format != event.FormatHTML || content.FormattedBody == "" {
		return content.Body
	}
	parser := &format.HTMLParser{
		TabsToSpaces:           4,
		Newline:                "\n",
		HorizontalLine:         "\n---\n",
		BoldConverter:          wrap("B"),
		ItalicConverter:        wrap("I"),
		UnderlineConverter:     wrap("U"),
		StrikethroughConverter: wrap("S"),
		MonospaceConverter:     func(s string, _ format.Context) string { return "[CODE]" + s + "[/CODE]" },
		MonospaceBlockConverter: func(code, _ string, _ format.Context) string {
			return "[CODE]" + strings.TrimRight(code, "\n") + "[/CODE]"
		},
		LinkConverter: func(text, href string, _ format.Context) string {
			if text == href || text == "" {
				return href
			}
			return "[URL=" + href + "]" + text + "[/URL]"
		},
		PillConverter: func(displayname, mxid, eventID string, _ format.Context) string {
			if mxid == "" || mxid[0] != '@' {
				return displayname
			}
			userID := id.UserID(mxid)
			if userID == bc.UserLogin.UserMXID {
				return fmt.Sprintf("[USER=%d]%s[/USER]", bc.client.UserID, displayname)
			}
			if ghostID, ok := bc.UserLogin.Bridge.Matrix.ParseGhostMXID(userID); ok {
				name := displayname
				if ghost, err := bc.UserLogin.Bridge.GetExistingGhostByID(ctx, ghostID); err == nil && ghost != nil && ghost.Name != "" {
					name = ghost.Name
				}
				return fmt.Sprintf("[USER=%s]%s[/USER]", ghostID, name)
			}
			return displayname
		},
	}
	return parser.Parse(content.FormattedBody, format.NewContext(ctx))
}

func (bc *BitrixClient) mentionResolver(userID string) (id.UserID, bool) {
	if _, err := strconv.ParseInt(userID, 10, 64); err != nil {
		return "", false
	}
	if userID == strconv.FormatInt(bc.client.UserID, 10) {
		return bc.UserLogin.UserMXID, true
	}
	return bc.UserLogin.Bridge.Matrix.FormatGhostMXID(networkid.UserID(userID)), true
}

// ---- Reactions ----

// Bitrix only supports a fixed reaction set.
var reactionToEmoji = map[string]string{
	"like":     "👍",
	"kiss":     "😘",
	"laugh":    "😂",
	"wonder":   "😮",
	"cry":      "😢",
	"angry":    "😡",
	"facepalm": "🤦",
}

var emojiToReaction = func() map[string]string {
	out := make(map[string]string, len(reactionToEmoji)+8)
	for k, v := range reactionToEmoji {
		out[v] = k
	}
	// common variants
	out["❤️"] = "kiss"
	out["❤"] = "kiss"
	out["😍"] = "kiss"
	out["🤣"] = "laugh"
	out["😆"] = "laugh"
	out["😲"] = "wonder"
	out["😯"] = "wonder"
	out["😭"] = "cry"
	out["😠"] = "angry"
	out["🤦‍♂️"] = "facepalm"
	out["🤦‍♀️"] = "facepalm"
	out["👍🏻"] = "like"
	return out
}()

func bitrixReactionEmoji(code string) string {
	if emoji, ok := reactionToEmoji[code]; ok {
		return emoji
	}
	return code
}

func allowedReactions() []string {
	out := make([]string, 0, len(reactionToEmoji))
	for _, code := range []string{"like", "kiss", "laugh", "wonder", "cry", "angry", "facepalm"} {
		out = append(out, reactionToEmoji[code])
	}
	return out
}

func normalizeEmoji(emoji string) string {
	return strings.TrimSuffix(emoji, "️")
}

func matrixReactionToBitrix(emoji string) (string, bool) {
	if code, ok := emojiToReaction[emoji]; ok {
		return code, true
	}
	code, ok := emojiToReaction[normalizeEmoji(emoji)]
	return code, ok
}
