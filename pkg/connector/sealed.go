package connector

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

// Message content kept for keep-deleted and show-edits is stored in message metadata, which ends
// up in database snapshots and backups. It's encrypted with AES-256-GCM using a key from the
// CONTENT_ENCRYPTION_KEY environment variable (base64 of 32 bytes), which never touches the
// database. Without the key nothing is stored, and both features fall back to degraded behavior.

const contentKeyEnv = "CONTENT_ENCRYPTION_KEY"

type storedContent struct {
	Content     *event.MessageEventContent `json:"c"`
	EditHistory []string                   `json:"h,omitempty"`
}

type contentSealer struct {
	aead cipher.AEAD
}

func newContentSealerFromEnv() (*contentSealer, error) {
	raw := strings.TrimSpace(os.Getenv(contentKeyEnv))
	if raw == "" {
		return nil, nil
	}
	return newContentSealer(raw)
}

func newContentSealer(b64Key string) (*contentSealer, error) {
	key, err := base64.StdEncoding.DecodeString(b64Key)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid base64: %w", contentKeyEnv, err)
	} else if len(key) != 32 {
		return nil, fmt.Errorf("%s must be 32 bytes, got %d", contentKeyEnv, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &contentSealer{aead: aead}, nil
}

// Seal encrypts sc, binding it to the message ID so it can't be moved to another message.
func (s *contentSealer) Seal(messageID string, sc *storedContent) (string, error) {
	plain, err := json.Marshal(sc)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	out := s.aead.Seal(nonce, nonce, plain, []byte(messageID))
	return base64.StdEncoding.EncodeToString(out), nil
}

func (s *contentSealer) Open(messageID, sealed string) (*storedContent, error) {
	data, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, err
	}
	ns := s.aead.NonceSize()
	if len(data) < ns {
		return nil, errors.New("sealed content too short")
	}
	plain, err := s.aead.Open(nil, data[:ns], data[ns:], []byte(messageID))
	if err != nil {
		return nil, err
	}
	var sc storedContent
	return &sc, json.Unmarshal(plain, &sc)
}

// sealContent encrypts content for a message part. Returns "" (nothing stored) without a key.
func (bc *BitrixClient) sealContent(messageID networkid.MessageID, partID networkid.PartID, sc *storedContent) string {
	if bc.Main.sealer == nil {
		return ""
	}
	sealed, err := bc.Main.sealer.Seal(string(messageID)+"/"+string(partID), sc)
	if err != nil {
		bc.UserLogin.Log.Err(err).Msg("Failed to encrypt message content")
		return ""
	}
	return sealed
}

// openContent decrypts the stored content of a message part, or returns nil.
func (bc *BitrixClient) openContent(part *database.Message) *storedContent {
	meta, ok := part.Metadata.(*MessageMetadata)
	if !ok || meta.Sealed == "" || bc.Main.sealer == nil {
		return nil
	}
	sc, err := bc.Main.sealer.Open(string(part.ID)+"/"+string(part.PartID), meta.Sealed)
	if err != nil {
		bc.UserLogin.Log.Warn().Err(err).Str("message_id", string(part.ID)).Msg("Failed to decrypt stored message content")
		return nil
	}
	return sc
}

// wipePlaintextContent removes plaintext content stored by versions before 0.1.7 (the "content" and
// "edit_history" metadata keys). It's a no-op once nothing matches.
func (bc *BitrixConnector) wipePlaintextContent(ctx context.Context) error {
	query := `UPDATE message SET metadata=json_remove(metadata, '$.content', '$.edit_history')
		WHERE bridge_id=$1 AND (json_extract(metadata, '$.content') IS NOT NULL OR json_extract(metadata, '$.edit_history') IS NOT NULL)`
	if bc.br.DB.Dialect == dbutil.Postgres {
		query = `UPDATE message SET metadata=metadata - 'content' - 'edit_history'
			WHERE bridge_id=$1 AND (metadata ? 'content' OR metadata ? 'edit_history')`
	}
	res, err := bc.br.DB.Exec(ctx, query, bc.br.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		bc.br.Log.Info().Int64("rows", n).Msg("Wiped plaintext message content from the database")
	}
	return nil
}

// vacuumSQLite rebuilds a SQLite database so content removed by the wipe (or by later edits and
// deletes) doesn't linger in free pages, then truncates the WAL. It runs on every start; the
// bridge database is small. Failures are logged and don't stop the bridge. For deletes between
// restarts, set _secure_delete=on in the database URI.
func (bc *BitrixConnector) vacuumSQLite(ctx context.Context) {
	if bc.br.DB.Dialect != dbutil.SQLite {
		return
	}
	log := bc.br.Log.With().Str("action", "vacuum database").Logger()
	before, err := bc.sqliteSize(ctx)
	if err == nil {
		_, err = bc.br.DB.Exec(ctx, "PRAGMA secure_delete=ON")
	}
	if err == nil {
		_, err = bc.br.DB.Exec(ctx, "VACUUM")
	}
	if err == nil {
		_, err = bc.br.DB.Exec(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	}
	if err != nil {
		log.Err(err).Msg("Failed to vacuum database")
		return
	}
	after, _ := bc.sqliteSize(ctx)
	log.Info().Int64("size_before", before).Int64("size_after", after).Msg("Vacuumed database")
}

func (bc *BitrixConnector) sqliteSize(ctx context.Context) (int64, error) {
	var pages, pageSize int64
	if err := bc.br.DB.QueryRow(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return 0, err
	}
	if err := bc.br.DB.QueryRow(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return 0, err
	}
	return pages * pageSize, nil
}
