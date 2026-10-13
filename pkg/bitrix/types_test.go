package bitrix

import (
	"encoding/json"
	"testing"
)

// Shapes captured from live portal push events (texts shortened).
func TestPushMessageShapes(t *testing.T) {
	var dm struct {
		DialogID FlexString `json:"dialogId"`
		Message  *Message   `json:"message"`
		Files    json.RawMessage
	}
	err := json.Unmarshal([]byte(`{"chatId":1001,"dialogId":77,"chat":[],"message":{"id":5001,
		"templateId":"00000000-0000-4000-8000-000000000001","chatId":1001,"senderId":77,"recipientId":42,
		"system":"N","date":"2026-10-12T14:49:04+05:00","text":"текст","params":{"REPLY_ID":5000}},"files":[]}`), &dm)
	if err != nil {
		t.Fatal(err)
	}
	m := dm.Message
	if dm.DialogID != "77" || m.Sender() != 77 || m.RecipientID != "42" || m.ReplyID() != 5000 || m.IsSystemMessage() || m.TemplateID == "" {
		t.Errorf("bad parse: dialog=%q %+v", dm.DialogID, m)
	}

	var group struct{ Message *Message }
	err = json.Unmarshal([]byte(`{"message":{"id":5101,"chatId":456,"senderId":88,"recipientId":"chat456",
		"system":"N","text":"x","params":[]}}`), &group)
	if err != nil {
		t.Fatal(err)
	}
	if group.Message.RecipientID != "chat456" || group.Message.ReplyID() != 0 {
		t.Errorf("bad group parse: %+v", group.Message)
	}

	var listed Message
	if err = json.Unmarshal([]byte(`{"id":1,"chat_id":7,"author_id":5,"params":{"FILE_ID":["9002"]}}`), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.ChatID != 7 || listed.Sender() != 5 || len(listed.FileIDs()) != 1 || listed.FileIDs()[0] != 9002 {
		t.Errorf("bad list parse: %+v", listed)
	}
}

func TestSystemAndEditShapes(t *testing.T) {
	var pin Message
	_ = json.Unmarshal([]byte(`{"id":5201,"senderId":42,"recipientId":99,"text":"---","params":{"CLASS":"bx-messenger-content-item-system","BETA":"Y"},"templateId":""}`), &pin)
	if !pin.IsSystemMessage() || pin.Sender() != 42 {
		t.Errorf("pin notice should be system with author: %+v", pin)
	}
	var edit Message
	_ = json.Unmarshal([]byte(`{"id":5302,"senderId":99,"text":"1234","params":{"REPLY_ID":"5301","IS_EDITED":"N","URL_ID":[]}}`), &edit)
	if edit.ReplyID() != 5301 || edit.IsSystemMessage() {
		t.Errorf("bad edit parse: %+v", edit)
	}
}

func TestNonImageFile(t *testing.T) {
	var b MessageBatch
	err := json.Unmarshal([]byte(`{"files":[{"id":9001,"type":"file","name":"a.txt","image":false},{"id":1,"type":"image","image":{"width":1280,"height":143}}]}`), &b)
	if err != nil {
		t.Fatal(err)
	}
	if b.Files[0].Image != nil && b.Files[0].Image.Width != 0 || b.Files[1].Image.Width != 1280 {
		t.Errorf("bad files: %+v %+v", b.Files[0].Image, b.Files[1].Image)
	}
}

func TestEmptyReactionMaps(t *testing.T) {
	var b MessageBatch
	err := json.Unmarshal([]byte(`{"reactions":[{"messageId":1,"reactionCounters":[],"reactionUsers":[],"ownReactions":[]}]}`), &b)
	if err != nil || len(b.Reactions) != 1 {
		t.Fatalf("err=%v %+v", err, b.Reactions)
	}
}
