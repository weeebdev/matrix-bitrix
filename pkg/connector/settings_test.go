package connector

import "testing"

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
