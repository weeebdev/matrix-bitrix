package connector

import (
	"context"
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func testMention(userID string) (id.UserID, bool) {
	return id.UserID("@bitrix_" + userID + ":beeper.local"), true
}

func TestBBCodeToMatrix(t *testing.T) {
	in := "[B]Заголовок[/B]\n[USER=42]Иванов Иван[/USER] see [URL=https://x.kz]link[/URL] <tag>"
	c := bbcodeToMatrix(in, testMention)
	if c.Format != event.FormatHTML {
		t.Fatalf("expected HTML format")
	}
	wantHTML := `<strong>Заголовок</strong><br/><a href="https://matrix.to/#/@bitrix_42:beeper.local">Иванов Иван</a> see <a href="https://x.kz">link</a> &lt;tag&gt;`
	if c.FormattedBody != wantHTML {
		t.Errorf("html:\n got %q\nwant %q", c.FormattedBody, wantHTML)
	}
	wantBody := "Заголовок\nИванов Иван see link (https://x.kz) <tag>"
	if c.Body != wantBody {
		t.Errorf("body:\n got %q\nwant %q", c.Body, wantBody)
	}
	if len(c.Mentions.UserIDs) != 1 {
		t.Errorf("expected 1 mention, got %v", c.Mentions.UserIDs)
	}
}

func TestBBCodePlainText(t *testing.T) {
	c := bbcodeToMatrix("ура", testMention)
	if c.Format != "" || c.Body != "ура" {
		t.Errorf("unexpected %+v", c)
	}
}

func TestBBCodeNestedAndQuote(t *testing.T) {
	c := bbcodeToMatrix("[B][i]x[/i][/B]\n------------------------------------------------------\nIvan [12.10.2026]\nold\n------------------------------------------------------\nreply", testMention)
	if !strings.Contains(c.FormattedBody, "<strong><em>x</em></strong>") || !strings.Contains(c.FormattedBody, "<blockquote>") {
		t.Errorf("got %q", c.FormattedBody)
	}
}

func TestReactionMapping(t *testing.T) {
	for _, emoji := range []string{"👍", "❤️", "😂"} {
		if _, ok := matrixReactionToBitrix(emoji); !ok {
			t.Errorf("%s should map", emoji)
		}
	}
	if _, ok := matrixReactionToBitrix("🦄"); ok {
		t.Errorf("unicorn should not map")
	}
}

func TestMatrixToBBCode(t *testing.T) {
	bc := &BitrixClient{}
	out := bc.matrixToBBCode(context.Background(), &event.MessageEventContent{
		Body:          "hi",
		Format:        event.FormatHTML,
		FormattedBody: `<b>bold</b> <i>it</i> <a href="https://e.kz">site</a><br><code>x</code>`,
	})
	want := "[B]bold[/B] [I]it[/I] [URL=https://e.kz]site[/URL]\n[CODE]x[/CODE]"
	if out != want {
		t.Errorf("got %q want %q", out, want)
	}
}
