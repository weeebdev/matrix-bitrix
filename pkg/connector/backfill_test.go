package connector

import (
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"

	"github.com/weeebdev/matrix-bitrix/pkg/bitrix"
)

func TestTrimBackwardBatch(t *testing.T) {
	bc := &BitrixClient{Main: &BitrixConnector{Config: Config{BackfillMaxDays: 365}}}
	msg := func(id int64, age time.Duration) *bitrix.Message {
		return &bitrix.Message{ID: bitrix.FlexInt(id), Date: bitrix.FlexTime{Time: time.Now().Add(-age)}}
	}
	day := 24 * time.Hour

	resp := &bridgev2.FetchMessagesResponse{HasMore: true}
	kept := bc.trimBackwardBatch([]*bitrix.Message{msg(8, 10*day), msg(9, 9*day)}, 10, resp)
	if len(kept) != 2 || !resp.HasMore {
		t.Fatalf("recent batch: kept %d, has_more %v", len(kept), resp.HasMore)
	}

	resp = &bridgev2.FetchMessagesResponse{HasMore: true}
	kept = bc.trimBackwardBatch([]*bitrix.Message{msg(8, 400*day), msg(9, 300*day)}, 10, resp)
	if len(kept) != 1 || kept[0].ID != 9 || resp.HasMore {
		t.Fatalf("batch crossing the cutoff: kept %d, has_more %v", len(kept), resp.HasMore)
	}

	resp = &bridgev2.FetchMessagesResponse{HasMore: true}
	kept = bc.trimBackwardBatch([]*bitrix.Message{msg(10, day), msg(11, day)}, 10, resp)
	if len(kept) != 0 || resp.HasMore {
		t.Fatalf("batch not older than anchor: kept %d, has_more %v", len(kept), resp.HasMore)
	}

	bc.Main.Config.BackfillMaxDays = -1
	resp = &bridgev2.FetchMessagesResponse{HasMore: true}
	kept = bc.trimBackwardBatch([]*bitrix.Message{msg(8, 4000*day)}, 10, resp)
	if len(kept) != 1 || !resp.HasMore {
		t.Fatalf("unlimited: kept %d, has_more %v", len(kept), resp.HasMore)
	}
}
