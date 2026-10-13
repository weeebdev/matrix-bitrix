package connector

import (
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestSettings(t *testing.T) {
	var meta UserLoginMetadata
	if !meta.KeepDeletedEnabled() || meta.Invisible {
		t.Fatal("defaults should be keep-deleted on, invisible off")
	}
	off := false
	meta.KeepDeleted = &off
	if meta.KeepDeletedEnabled() {
		t.Error("keep-deleted should be off")
	}
	for in, want := range map[string]bool{"on": true, "OFF": false, "yes": true, "0": false} {
		if v, ok := boolArg([]string{in}); !ok || v != want {
			t.Errorf("boolArg(%q) = %v, %v", in, v, ok)
		}
	}
	if _, ok := boolArg([]string{"maybe"}); ok {
		t.Error("garbage should not parse")
	}
}

func TestDeletedContent(t *testing.T) {
	text := deletedContent(&event.MessageEventContent{MsgType: event.MsgText, Body: "a < b\nc"})
	if text.FormattedBody != "<del>a &lt; b<br/>c</del><br/><em>"+deletedLabel+"</em>" || text.Body != "~~a < b\nc~~\n"+deletedLabel {
		t.Errorf("text: %q / %q", text.FormattedBody, text.Body)
	}
	orig := &event.MessageEventContent{MsgType: event.MsgImage, Body: "pic.png", URL: "mxc://x/y"}
	img := deletedContent(orig)
	if img.URL != "mxc://x/y" || img.FileName != "pic.png" || img.Body != "pic.png ("+deletedLabel+")" {
		t.Errorf("image: %+v", img)
	}
	if orig.Body != "pic.png" {
		t.Error("original content must not be modified")
	}
}

func TestWithEditHistory(t *testing.T) {
	c := withEditHistory(&event.MessageEventContent{MsgType: event.MsgText, Body: "v3"}, []string{"v2", "a<b"})
	if c.Body != "v3\n✏️ was: v2\n✏️ was: a<b" {
		t.Errorf("body %q", c.Body)
	}
	want := `v3<br/><font color="#888888"><sub>✏️ was: v2</sub></font><br/><font color="#888888"><sub>✏️ was: a&lt;b</sub></font>`
	if c.FormattedBody != want {
		t.Errorf("html %q", c.FormattedBody)
	}
}
