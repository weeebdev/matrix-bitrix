package connector

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
)

func testSealer(t *testing.T) *contentSealer {
	s, err := newContentSealer(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSealRoundTrip(t *testing.T) {
	s := testSealer(t)
	in := &storedContent{Content: &event.MessageEventContent{MsgType: event.MsgText, Body: "секрет"}, EditHistory: []string{"v1"}}
	sealed, err := s.Seal("42/", in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "секрет") || strings.Contains(sealed, "v1") {
		t.Fatal("sealed output contains plaintext")
	}
	out, err := s.Open("42/", sealed)
	if err != nil || out.Content.Body != "секрет" || out.EditHistory[0] != "v1" {
		t.Fatalf("round trip failed: %v %+v", err, out)
	}
	if _, err = s.Open("43/", sealed); err == nil {
		t.Fatal("content sealed for one message must not open for another")
	}
	again, _ := s.Seal("42/", in)
	if again == sealed {
		t.Fatal("nonce must differ between seals")
	}
}

func TestSealerKeyValidation(t *testing.T) {
	if _, err := newContentSealer("not base64!"); err == nil {
		t.Error("invalid base64 should fail")
	}
	if _, err := newContentSealer(base64.StdEncoding.EncodeToString(make([]byte, 16))); err == nil {
		t.Error("16-byte key should fail")
	}
}

func TestWipePlaintextContent(t *testing.T) {
	db, err := dbutil.NewWithDialect("file::memory:?cache=shared", "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = db.Exec(ctx, `CREATE TABLE message (bridge_id TEXT, id TEXT, metadata jsonb NOT NULL);
		INSERT INTO message VALUES ('b', '1', '{"content":{"body":"secret"},"edit_history":["old"],"deleted":true}');
		INSERT INTO message VALUES ('b', '2', '{"sealed":"xyz"}');
		INSERT INTO message VALUES ('other', '3', '{"content":{"body":"not ours"}}');`)
	if err != nil {
		t.Fatal(err)
	}
	bc := &BitrixConnector{br: &bridgev2.Bridge{ID: "b", DB: nil}}
	bc.br.DB = &database.Database{Database: db}
	if err = bc.wipePlaintextContent(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"1": `{"deleted":true}`, "2": `{"sealed":"xyz"}`, "3": `{"content":{"body":"not ours"}}`} {
		var got string
		if err = db.QueryRow(ctx, "SELECT metadata FROM message WHERE id=$1", id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("row %s: got %s, want %s", id, got, want)
		}
	}
}

func TestWipeLeavesNoRemnantsOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.db"
	db, err := dbutil.NewWithDialect("file:"+path+"?_journal_mode=WAL&_txlock=immediate", "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const marker = "PLAINTEXT-MARKER-7f3a"
	_, err = db.Exec(ctx, `CREATE TABLE message (bridge_id TEXT, id TEXT, metadata jsonb NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		_, err = db.Exec(ctx, `INSERT INTO message VALUES ('b', $1, $2)`, i, `{"content":{"body":"`+marker+strings.Repeat("x", 200)+`"}}`)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	bc := &BitrixConnector{br: &bridgev2.Bridge{ID: "b"}}
	bc.br.DB = &database.Database{Database: db}
	bc.br.Log = zerolog.Nop()
	if err = bc.wipePlaintextContent(ctx); err != nil {
		t.Fatal(err)
	}
	scan := func() int {
		n := 0
		for _, suffix := range []string{"", "-wal", "-shm"} {
			data, _ := os.ReadFile(path + suffix)
			n += strings.Count(string(data), marker)
		}
		return n
	}
	if scan() == 0 {
		t.Fatal("expected remnants in free pages before vacuum (test is not exercising the problem)")
	}
	bc.vacuumSQLite(ctx)
	if n := scan(); n != 0 {
		t.Fatalf("found %d plaintext remnants on disk after vacuum", n)
	}
}
