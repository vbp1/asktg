package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"asktg/internal/domain"
	"asktg/internal/search"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type URLTask struct {
	TaskID   int64
	ChatID   int64
	MsgID    int64
	URL      string
	Attempts int
}

type URLCandidateMessage struct {
	ChatID   int64
	MsgID    int64
	Text     string
	URLsMode string
}

type EmbeddingTask struct {
	TaskID   int64
	ChunkID  int64
	Attempts int
}

type EmbeddingCandidate struct {
	ChunkID int64
	ChatID  int64
	MsgID   int64
	Text    string
}

type EmbeddingRecord struct {
	ChunkID int64
	Vector  []float32
}

func Open(dbPath string) (*Store, error) {
	if dbPath == "" {
		return nil, errors.New("db path is required")
	}
	db, err := sql.Open("sqlite", filepath.Clean(dbPath))
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON;`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000;`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode = WAL;`); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	schema := `
CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS chats (
	chat_id INTEGER PRIMARY KEY,
	title TEXT NOT NULL,
	type TEXT NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 0,
	history_mode TEXT NOT NULL DEFAULT 'full',
	allow_embeddings INTEGER NOT NULL DEFAULT 0,
	embeddings_since_unix INTEGER NOT NULL DEFAULT 0,
	urls_mode TEXT NOT NULL DEFAULT 'off',
	sync_cursor TEXT NOT NULL DEFAULT '',
	last_message_unix INTEGER NOT NULL DEFAULT 0,
	last_synced_unix INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS messages (
	chat_id INTEGER NOT NULL,
	msg_id INTEGER NOT NULL,
	ts INTEGER NOT NULL,
	sender_id INTEGER NOT NULL DEFAULT 0,
	sender_display TEXT NOT NULL DEFAULT '',
	text TEXT NOT NULL DEFAULT '',
	edit_ts INTEGER NOT NULL DEFAULT 0,
	deleted INTEGER NOT NULL DEFAULT 0,
	has_url INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (chat_id, msg_id),
	FOREIGN KEY (chat_id) REFERENCES chats(chat_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS chunks (
	chunk_id INTEGER PRIMARY KEY AUTOINCREMENT,
	chat_id INTEGER NOT NULL,
	msg_id INTEGER NOT NULL,
	ts INTEGER NOT NULL,
	text TEXT NOT NULL,
	deleted INTEGER NOT NULL DEFAULT 0,
	FOREIGN KEY (chat_id, msg_id) REFERENCES messages(chat_id, msg_id) ON DELETE CASCADE
);

CREATE VIRTUAL TABLE IF NOT EXISTS fts_chunks USING fts5(
	text,
	chat_id UNINDEXED,
	msg_id UNINDEXED,
	ts UNINDEXED,
	tokenize = 'unicode61'
);

CREATE TABLE IF NOT EXISTS embeddings (
	chunk_id INTEGER PRIMARY KEY,
	model TEXT NOT NULL,
	dims INTEGER NOT NULL,
	vec BLOB NOT NULL,
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS url_docs (
	url_id INTEGER PRIMARY KEY AUTOINCREMENT,
	chat_id INTEGER NOT NULL,
	msg_id INTEGER NOT NULL,
	url TEXT NOT NULL,
	final_url TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	extracted_text TEXT NOT NULL DEFAULT '',
	hash TEXT NOT NULL DEFAULT '',
	fetched_at INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_url_docs_unique ON url_docs(chat_id, msg_id, url);

CREATE VIRTUAL TABLE IF NOT EXISTS fts_url_docs USING fts5(
	text,
	chat_id UNINDEXED,
	msg_id UNINDEXED,
	url UNINDEXED,
	tokenize = 'unicode61'
);

CREATE TABLE IF NOT EXISTS tasks (
	task_id INTEGER PRIMARY KEY AUTOINCREMENT,
	type TEXT NOT NULL,
	payload TEXT NOT NULL,
	state TEXT NOT NULL,
	attempts INTEGER NOT NULL DEFAULT 0,
	next_run_at INTEGER NOT NULL DEFAULT 0,
	priority INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_tasks_state_next ON tasks(type, state, next_run_at, priority, created_at);
`
	_, err := s.db.ExecContext(ctx, schema)
	if err != nil {
		return err
	}
	if err := s.ensureChatsEmbeddingsSinceColumn(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureChatsEmbeddingsSinceColumn(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE chats ADD COLUMN embeddings_since_unix INTEGER NOT NULL DEFAULT 0`); err != nil {
		lower := strings.ToLower(err.Error())
		if !strings.Contains(lower, "duplicate column name") {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE chats
SET embeddings_since_unix = CAST(strftime('%s','now') AS INTEGER)
WHERE allow_embeddings = 1
  AND embeddings_since_unix = 0
`)
	return err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO settings(key, value) VALUES(?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value
`, key, value)
	return err
}

func (s *Store) ListSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	settings := map[string]string{}
	for rows.Next() {
		var key string
		var value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		settings[key] = value
	}
	return settings, rows.Err()
}

func (s *Store) Checkpoint(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE);`)
	return err
}

func (s *Store) ExportDatabaseSnapshot(ctx context.Context, destinationPath string) error {
	cleanPath := strings.TrimSpace(filepath.Clean(destinationPath))
	if cleanPath == "" {
		return errors.New("destination path is required")
	}
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o755); err != nil {
		return err
	}
	if err := os.Remove(cleanPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	escaped := strings.ReplaceAll(cleanPath, "'", "''")
	_, err := s.db.ExecContext(ctx, "VACUUM main INTO '"+escaped+"'")
	return err
}

func (s *Store) ScrubSecretSettings(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM settings`)
	if err != nil {
		return err
	}
	defer rows.Close()

	keys := make([]string, 0, 8)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return err
		}
		lower := strings.ToLower(key)
		if strings.Contains(lower, "api_hash") ||
			strings.Contains(lower, "api_key") ||
			strings.Contains(lower, "token") ||
			strings.Contains(lower, "secret") ||
			strings.Contains(lower, "password") {
			keys = append(keys, key)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, key := range keys {
		if _, err := s.db.ExecContext(ctx, `UPDATE settings SET value = '' WHERE key = ?`, key); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetSetting(ctx context.Context, key, defaultValue string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultValue, nil
	}
	return value, err
}

func (s *Store) GetSettingInt(ctx context.Context, key string, defaultValue int) (int, error) {
	raw, err := s.GetSetting(ctx, key, strconv.Itoa(defaultValue))
	if err != nil {
		return defaultValue, err
	}
	parsed, parseErr := strconv.Atoi(raw)
	if parseErr != nil {
		return defaultValue, nil
	}
	return parsed, nil
}

func (s *Store) GetSettingBool(ctx context.Context, key string, defaultValue bool) (bool, error) {
	fallback := "0"
	if defaultValue {
		fallback = "1"
	}
	raw, err := s.GetSetting(ctx, key, fallback)
	if err != nil {
		return defaultValue, err
	}
	return raw == "1" || strings.EqualFold(raw, "true"), nil
}

func (s *Store) UpsertChat(ctx context.Context, chat domain.ChatPolicy) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO chats(chat_id, title, type, enabled, history_mode, allow_embeddings, embeddings_since_unix, urls_mode, sync_cursor, last_message_unix, last_synced_unix)
VALUES(?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)
ON CONFLICT(chat_id) DO UPDATE SET
	title = excluded.title,
	type = excluded.type,
	enabled = excluded.enabled,
	history_mode = excluded.history_mode,
	allow_embeddings = excluded.allow_embeddings,
	urls_mode = excluded.urls_mode,
	sync_cursor = excluded.sync_cursor,
	last_message_unix = excluded.last_message_unix,
	last_synced_unix = excluded.last_synced_unix
`, chat.ChatID, chat.Title, chat.Type, boolToInt(chat.Enabled), chat.HistoryMode, boolToInt(chat.AllowEmbeddings), chat.URLsMode, chat.SyncCursor, chat.LastMessageUnix, chat.LastSyncedUnix)
	return err
}

func (s *Store) UpsertDiscoveredChat(ctx context.Context, chatID int64, title, chatType string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO chats(chat_id, title, type, enabled, history_mode, allow_embeddings, embeddings_since_unix, urls_mode, sync_cursor, last_message_unix, last_synced_unix)
VALUES(?, ?, ?, 0, 'full', 0, 0, 'off', '', 0, 0)
ON CONFLICT(chat_id) DO UPDATE SET
	title = excluded.title,
	type = excluded.type
`, chatID, title, chatType)
	return err
}

func (s *Store) UpdateChatSyncState(ctx context.Context, chatID int64, syncCursor string, lastMessageUnix int64, lastSyncedUnix int64) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE chats
SET sync_cursor = ?, last_message_unix = ?, last_synced_unix = ?
WHERE chat_id = ?
`, syncCursor, lastMessageUnix, lastSyncedUnix, chatID)
	return err
}

func (s *Store) SetChatPolicy(ctx context.Context, chatID int64, enabled bool, historyMode string, allowEmbeddings bool, urlsMode string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var (
		currentAllow int
		sinceUnix    int64
	)
	err = tx.QueryRowContext(ctx, `
SELECT allow_embeddings, embeddings_since_unix
FROM chats
WHERE chat_id = ?
`, chatID).Scan(&currentAllow, &sinceUnix)
	if err != nil {
		return err
	}

	if !allowEmbeddings {
		sinceUnix = 0
	} else if strings.EqualFold(strings.TrimSpace(historyMode), "full") {
		// Backfill mode: allow embedding candidates from the full retained history.
		sinceUnix = 0
	} else if currentAllow == 0 || sinceUnix <= 0 {
		// New-only mode: only embed content from the time embeddings were enabled.
		sinceUnix = time.Now().Unix()
	}

	_, err = tx.ExecContext(ctx, `
UPDATE chats
SET enabled = ?, history_mode = ?, allow_embeddings = ?, embeddings_since_unix = ?, urls_mode = ?
WHERE chat_id = ?
`, boolToInt(enabled), historyMode, boolToInt(allowEmbeddings), sinceUnix, urlsMode, chatID)
	if err != nil {
		return err
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return commitErr
	}
	return nil
}

func (s *Store) GetChatPolicy(ctx context.Context, chatID int64) (domain.ChatPolicy, error) {
	var (
		chat            domain.ChatPolicy
		enabled         int
		allowEmbeddings int
	)
	err := s.db.QueryRowContext(ctx, `
SELECT chat_id, title, type, enabled, history_mode, allow_embeddings, urls_mode, sync_cursor, last_message_unix, last_synced_unix
FROM chats
WHERE chat_id = ?
`, chatID).Scan(&chat.ChatID, &chat.Title, &chat.Type, &enabled, &chat.HistoryMode, &allowEmbeddings, &chat.URLsMode, &chat.SyncCursor, &chat.LastMessageUnix, &chat.LastSyncedUnix)
	if err != nil {
		return domain.ChatPolicy{}, err
	}
	chat.Enabled = enabled == 1
	chat.AllowEmbeddings = allowEmbeddings == 1
	return chat, nil
}

func (s *Store) ListChats(ctx context.Context) ([]domain.ChatPolicy, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT chat_id, title, type, enabled, history_mode, allow_embeddings, urls_mode, sync_cursor, last_message_unix, last_synced_unix
FROM chats
ORDER BY title ASC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	chats := make([]domain.ChatPolicy, 0, 16)
	for rows.Next() {
		var (
			chat            domain.ChatPolicy
			enabled         int
			allowEmbeddings int
		)
		if err := rows.Scan(&chat.ChatID, &chat.Title, &chat.Type, &enabled, &chat.HistoryMode, &allowEmbeddings, &chat.URLsMode, &chat.SyncCursor, &chat.LastMessageUnix, &chat.LastSyncedUnix); err != nil {
			return nil, err
		}
		chat.Enabled = enabled == 1
		chat.AllowEmbeddings = allowEmbeddings == 1
		chats = append(chats, chat)
	}
	return chats, rows.Err()
}

func (s *Store) BackfillEmbeddingsForEnabledChats(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE chats
SET embeddings_since_unix = 0
WHERE enabled = 1
  AND allow_embeddings = 1
  AND history_mode = 'full'
  AND embeddings_since_unix != 0
`)
	if err != nil {
		return 0, err
	}
	affected, _ := res.RowsAffected()
	return int(affected), nil
}

func (s *Store) UpsertMessage(ctx context.Context, msg domain.Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	_, err = tx.ExecContext(ctx, `
INSERT INTO messages(chat_id, msg_id, ts, sender_id, sender_display, text, edit_ts, deleted, has_url)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(chat_id, msg_id) DO UPDATE SET
	ts = excluded.ts,
	sender_id = excluded.sender_id,
	sender_display = excluded.sender_display,
	text = excluded.text,
	edit_ts = excluded.edit_ts,
	deleted = excluded.deleted,
	has_url = excluded.has_url
`, msg.ChatID, msg.MsgID, msg.Timestamp, msg.SenderID, msg.SenderDisplay, msg.Text, msg.EditTS, boolToInt(msg.Deleted), boolToInt(msg.HasURL))
	if err != nil {
		return err
	}

	if msg.Deleted {
		if _, err = tx.ExecContext(ctx, `
DELETE FROM embeddings
WHERE chunk_id IN (
	SELECT chunk_id FROM chunks WHERE chat_id = ? AND msg_id = ?
)
`, msg.ChatID, msg.MsgID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `
DELETE FROM fts_chunks WHERE rowid IN (
	SELECT chunk_id FROM chunks WHERE chat_id = ? AND msg_id = ?
)`, msg.ChatID, msg.MsgID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE chunks SET deleted = 1, text = '' WHERE chat_id = ? AND msg_id = ?`, msg.ChatID, msg.MsgID); err != nil {
			return err
		}
	} else {
		if _, err = tx.ExecContext(ctx, `
DELETE FROM embeddings
WHERE chunk_id IN (
	SELECT chunk_id FROM chunks WHERE chat_id = ? AND msg_id = ?
)
`, msg.ChatID, msg.MsgID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM fts_chunks WHERE rowid IN (SELECT chunk_id FROM chunks WHERE chat_id = ? AND msg_id = ?)`, msg.ChatID, msg.MsgID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM chunks WHERE chat_id = ? AND msg_id = ?`, msg.ChatID, msg.MsgID); err != nil {
			return err
		}
		res, execErr := tx.ExecContext(ctx, `
INSERT INTO chunks(chat_id, msg_id, ts, text, deleted)
VALUES(?, ?, ?, ?, 0)
`, msg.ChatID, msg.MsgID, msg.Timestamp, msg.Text)
		if execErr != nil {
			return execErr
		}
		chunkID, idErr := res.LastInsertId()
		if idErr != nil {
			return idErr
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO fts_chunks(rowid, text, chat_id, msg_id, ts)
VALUES(?, ?, ?, ?, ?)
`, chunkID, msg.Text, msg.ChatID, msg.MsgID, msg.Timestamp)
		if err != nil {
			return err
		}
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return commitErr
	}
	return nil
}

func (s *Store) GetMessage(ctx context.Context, chatID, msgID int64) (domain.Message, error) {
	var (
		message domain.Message
		deleted int
		hasURL  int
	)
	err := s.db.QueryRowContext(ctx, `
SELECT chat_id, msg_id, ts, edit_ts, sender_id, sender_display, text, deleted, has_url
FROM messages
WHERE chat_id = ? AND msg_id = ?
`, chatID, msgID).Scan(&message.ChatID, &message.MsgID, &message.Timestamp, &message.EditTS, &message.SenderID, &message.SenderDisplay, &message.Text, &deleted, &hasURL)
	if err != nil {
		return domain.Message{}, err
	}
	message.Deleted = deleted == 1
	message.HasURL = hasURL == 1
	return message, nil
}

func (s *Store) ListChunkIDsByMessage(ctx context.Context, chatID, msgID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT chunk_id
FROM chunks
WHERE chat_id = ? AND msg_id = ?
`, chatID, msgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	chunkIDs := make([]int64, 0, 2)
	for rows.Next() {
		var chunkID int64
		if err := rows.Scan(&chunkID); err != nil {
			return nil, err
		}
		chunkIDs = append(chunkIDs, chunkID)
	}
	return chunkIDs, rows.Err()
}

func (s *Store) MarkMessageDeleted(ctx context.Context, chatID, msgID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx, `
UPDATE messages
SET deleted = 1, text = ''
WHERE chat_id = ? AND msg_id = ?
`, chatID, msgID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
UPDATE chunks
SET deleted = 1, text = ''
WHERE chat_id = ? AND msg_id = ?
`, chatID, msgID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
DELETE FROM fts_chunks WHERE rowid IN (
	SELECT chunk_id FROM chunks WHERE chat_id = ? AND msg_id = ?
)
`, chatID, msgID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
DELETE FROM embeddings
WHERE chunk_id IN (
	SELECT chunk_id FROM chunks WHERE chat_id = ? AND msg_id = ?
)
`, chatID, msgID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
DELETE FROM fts_url_docs WHERE rowid IN (
	SELECT url_id FROM url_docs WHERE chat_id = ? AND msg_id = ?
)
`, chatID, msgID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM url_docs WHERE chat_id = ? AND msg_id = ?`, chatID, msgID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PurgeChatData(ctx context.Context, chatID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx, `
DELETE FROM embeddings
WHERE chunk_id IN (
	SELECT chunk_id FROM chunks WHERE chat_id = ?
)
`, chatID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
DELETE FROM fts_url_docs
WHERE rowid IN (
	SELECT url_id FROM url_docs WHERE chat_id = ?
)
`, chatID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM url_docs WHERE chat_id = ?`, chatID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
DELETE FROM fts_chunks
WHERE rowid IN (
	SELECT chunk_id FROM chunks WHERE chat_id = ?
)
`, chatID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM chunks WHERE chat_id = ?`, chatID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM messages WHERE chat_id = ?`, chatID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
DELETE FROM tasks
WHERE type = 'url_fetch'
  AND payload LIKE ?
`, "%\"chat_id\":"+strconv.FormatInt(chatID, 10)+",%"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
UPDATE chats
SET sync_cursor = '', last_message_unix = 0, last_synced_unix = 0
WHERE chat_id = ?
`, chatID); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) PurgeAllData(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx, `DELETE FROM embeddings`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM fts_url_docs`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM url_docs`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM fts_chunks`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM chunks`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM messages`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM tasks`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE chats SET sync_cursor = '', last_message_unix = 0, last_synced_unix = 0`); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) ResetEmbeddings(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, `DELETE FROM embeddings`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM tasks WHERE type = 'embed_chunk'`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) EnableEmbeddingsForEnabledChats(ctx context.Context) (int, error) {
	nowUnix := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `
UPDATE chats
SET allow_embeddings = 1,
    embeddings_since_unix = CASE WHEN embeddings_since_unix <= 0 THEN ? ELSE embeddings_since_unix END
WHERE enabled = 1
  AND allow_embeddings = 0
`, nowUnix)
	if err != nil {
		return 0, err
	}
	affected, _ := res.RowsAffected()
	return int(affected), nil
}

type embeddingTaskPayload struct {
	ChunkID int64 `json:"chunk_id"`
}

func (s *Store) EnqueueEmbeddingTask(ctx context.Context, chunkID int64, priority int) error {
	if chunkID == 0 {
		return errors.New("invalid embedding task payload")
	}
	payload := embeddingTaskPayload{ChunkID: chunkID}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	var exists int
	if err := s.db.QueryRowContext(ctx, `
SELECT 1
FROM tasks
WHERE type = 'embed_chunk'
  AND payload = ?
LIMIT 1
`, string(encoded)).Scan(&exists); err == nil && exists == 1 {
		return nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	nowUnix := time.Now().Unix()
	_, err = s.db.ExecContext(ctx, `
INSERT INTO tasks(type, payload, state, attempts, next_run_at, priority, created_at)
VALUES('embed_chunk', ?, 'pending', 0, ?, ?, ?)
`, string(encoded), nowUnix, priority, nowUnix)
	return err
}

func (s *Store) ClaimEmbeddingTask(ctx context.Context, nowUnix int64) (EmbeddingTask, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return EmbeddingTask{}, false, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var (
		taskID   int64
		payload  string
		attempts int
	)
	err = tx.QueryRowContext(ctx, `
SELECT task_id, payload, attempts
FROM tasks
WHERE type = 'embed_chunk'
  AND state = 'pending'
  AND next_run_at <= ?
ORDER BY priority ASC, created_at ASC
LIMIT 1
`, nowUnix).Scan(&taskID, &payload, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return EmbeddingTask{}, false, nil
	}
	if err != nil {
		return EmbeddingTask{}, false, err
	}

	if _, err = tx.ExecContext(ctx, `
UPDATE tasks
SET state = 'running', attempts = attempts + 1
WHERE task_id = ?
`, taskID); err != nil {
		return EmbeddingTask{}, false, err
	}

	var decoded embeddingTaskPayload
	if decodeErr := json.Unmarshal([]byte(payload), &decoded); decodeErr != nil {
		return EmbeddingTask{}, false, decodeErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return EmbeddingTask{}, false, commitErr
	}

	return EmbeddingTask{
		TaskID:   taskID,
		ChunkID:  decoded.ChunkID,
		Attempts: attempts + 1,
	}, true, nil
}

func (s *Store) ListEmbeddingCandidates(ctx context.Context, limit int) ([]EmbeddingCandidate, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT c.chunk_id, c.chat_id, c.msg_id, c.text
FROM chunks c
JOIN chats ch ON ch.chat_id = c.chat_id
JOIN messages m ON m.chat_id = c.chat_id AND m.msg_id = c.msg_id
LEFT JOIN embeddings e ON e.chunk_id = c.chunk_id
WHERE ch.enabled = 1
  AND ch.allow_embeddings = 1
  AND c.deleted = 0
  AND m.deleted = 0
  AND e.chunk_id IS NULL
  AND (ch.embeddings_since_unix = 0 OR m.ts >= ch.embeddings_since_unix)
ORDER BY m.ts DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]EmbeddingCandidate, 0, limit)
	for rows.Next() {
		var item EmbeddingCandidate
		if err := rows.Scan(&item.ChunkID, &item.ChatID, &item.MsgID, &item.Text); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) EmbeddingsProgress(ctx context.Context) (domain.EmbeddingsProgress, error) {
	var totalEligible int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(1)
FROM chunks c
JOIN chats ch ON ch.chat_id = c.chat_id
JOIN messages m ON m.chat_id = c.chat_id AND m.msg_id = c.msg_id
WHERE ch.enabled = 1
  AND ch.allow_embeddings = 1
  AND c.deleted = 0
  AND m.deleted = 0
  AND (ch.embeddings_since_unix = 0 OR m.ts >= ch.embeddings_since_unix)
`).Scan(&totalEligible); err != nil {
		return domain.EmbeddingsProgress{}, err
	}

	var embedded int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(1)
FROM chunks c
JOIN chats ch ON ch.chat_id = c.chat_id
JOIN messages m ON m.chat_id = c.chat_id AND m.msg_id = c.msg_id
JOIN embeddings e ON e.chunk_id = c.chunk_id
WHERE ch.enabled = 1
  AND ch.allow_embeddings = 1
  AND c.deleted = 0
  AND m.deleted = 0
  AND (ch.embeddings_since_unix = 0 OR m.ts >= ch.embeddings_since_unix)
`).Scan(&embedded); err != nil {
		return domain.EmbeddingsProgress{}, err
	}

	var pending, running, failed int
	if err := s.db.QueryRowContext(ctx, `
SELECT
  SUM(CASE WHEN state = 'pending' THEN 1 ELSE 0 END) AS pending,
  SUM(CASE WHEN state = 'running' THEN 1 ELSE 0 END) AS running,
  SUM(CASE WHEN state = 'failed' THEN 1 ELSE 0 END) AS failed
FROM tasks
WHERE type = 'embed_chunk'
`).Scan(&pending, &running, &failed); err != nil {
		return domain.EmbeddingsProgress{}, err
	}

	return domain.EmbeddingsProgress{
		TotalEligible: totalEligible,
		Embedded:      embedded,
		QueuePending:  pending,
		QueueRunning:  running,
		QueueFailed:   failed,
	}, nil
}

func (s *Store) LoadChunkForEmbedding(ctx context.Context, chunkID int64) (EmbeddingCandidate, error) {
	var item EmbeddingCandidate
	err := s.db.QueryRowContext(ctx, `
SELECT c.chunk_id, c.chat_id, c.msg_id, c.text
FROM chunks c
JOIN chats ch ON ch.chat_id = c.chat_id
JOIN messages m ON m.chat_id = c.chat_id AND m.msg_id = c.msg_id
WHERE c.chunk_id = ?
  AND ch.enabled = 1
  AND ch.allow_embeddings = 1
  AND c.deleted = 0
  AND m.deleted = 0
  AND (ch.embeddings_since_unix = 0 OR m.ts >= ch.embeddings_since_unix)
`, chunkID).Scan(&item.ChunkID, &item.ChatID, &item.MsgID, &item.Text)
	if err != nil {
		return EmbeddingCandidate{}, err
	}
	return item, nil
}

func (s *Store) UpsertEmbedding(ctx context.Context, chunkID int64, model string, vector []float32, createdAt int64) error {
	if chunkID == 0 {
		return errors.New("chunk id is required")
	}
	if len(vector) == 0 {
		return errors.New("embedding vector is empty")
	}
	blob := encodeFloat32Slice(vector)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO embeddings(chunk_id, model, dims, vec, created_at)
VALUES(?, ?, ?, ?, ?)
ON CONFLICT(chunk_id) DO UPDATE SET
	model = excluded.model,
	dims = excluded.dims,
	vec = excluded.vec,
	created_at = excluded.created_at
`, chunkID, strings.TrimSpace(model), len(vector), blob, createdAt)
	return err
}

func (s *Store) ListEmbeddings(ctx context.Context, limit int) ([]EmbeddingRecord, error) {
	query := `
SELECT e.chunk_id, e.vec
FROM embeddings e
JOIN chunks c ON c.chunk_id = e.chunk_id
JOIN messages m ON m.chat_id = c.chat_id AND m.msg_id = c.msg_id
JOIN chats ch ON ch.chat_id = c.chat_id
WHERE m.deleted = 0
  AND c.deleted = 0
  AND ch.enabled = 1
  AND ch.allow_embeddings = 1
ORDER BY e.chunk_id ASC
`
	args := make([]any, 0, 1)
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]EmbeddingRecord, 0, 256)
	for rows.Next() {
		var (
			chunkID int64
			blob    []byte
		)
		if err := rows.Scan(&chunkID, &blob); err != nil {
			return nil, err
		}
		vector := decodeFloat32Slice(blob)
		if len(vector) == 0 {
			continue
		}
		records = append(records, EmbeddingRecord{
			ChunkID: chunkID,
			Vector:  vector,
		})
	}
	return records, rows.Err()
}

func (s *Store) LookupChunkResults(ctx context.Context, req domain.SearchRequest, chunkIDs []int64) (map[int64]domain.SearchResult, error) {
	if len(chunkIDs) == 0 {
		return map[int64]domain.SearchResult{}, nil
	}

	query := `
SELECT
	c.chunk_id,
	c.chat_id,
	c.msg_id,
	m.ts,
	ch.title,
	m.sender_display,
	m.text
FROM chunks c
JOIN messages m ON m.chat_id = c.chat_id AND m.msg_id = c.msg_id
JOIN chats ch ON ch.chat_id = c.chat_id
WHERE c.chunk_id IN (
`
	args := make([]any, 0, len(chunkIDs)+8)
	for idx, id := range chunkIDs {
		if idx > 0 {
			query += ","
		}
		query += "?"
		args = append(args, id)
	}
	query += `
)
  AND m.deleted = 0
  AND c.deleted = 0
  AND ch.enabled = 1
  AND ch.allow_embeddings = 1
`

	if req.Filters.FromUnix > 0 {
		query += " AND m.ts >= ?"
		args = append(args, req.Filters.FromUnix)
	}
	if req.Filters.ToUnix > 0 {
		query += " AND m.ts <= ?"
		args = append(args, req.Filters.ToUnix)
	}
	if len(req.Filters.ChatIDs) > 0 {
		query += " AND c.chat_id IN ("
		for idx, id := range req.Filters.ChatIDs {
			if idx > 0 {
				query += ","
			}
			query += "?"
			args = append(args, id)
		}
		query += ")"
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make(map[int64]domain.SearchResult, len(chunkIDs))
	for rows.Next() {
		var (
			chunkID int64
			item    domain.SearchResult
		)
		if err := rows.Scan(&chunkID, &item.ChatID, &item.MsgID, &item.Timestamp, &item.ChatTitle, &item.Sender, &item.MessageText); err != nil {
			return nil, err
		}
		item.Snippet = item.MessageText
		results[chunkID] = item
	}
	return results, rows.Err()
}

func (s *Store) SearchByEmbedding(ctx context.Context, req domain.SearchRequest, vector []float32, maxCandidates int) ([]domain.SearchResult, error) {
	if len(vector) == 0 {
		return nil, nil
	}
	if maxCandidates <= 0 {
		maxCandidates = 2000
	}
	limit := req.Filters.Limit
	if limit <= 0 || limit > 100 {
		limit = 25
	}

	query := `
SELECT
	e.vec,
	c.chat_id,
	c.msg_id,
	m.ts,
	ch.title,
	m.sender_display,
	m.text
FROM embeddings e
JOIN chunks c ON c.chunk_id = e.chunk_id
JOIN messages m ON m.chat_id = c.chat_id AND m.msg_id = c.msg_id
JOIN chats ch ON ch.chat_id = c.chat_id
WHERE m.deleted = 0
  AND c.deleted = 0
  AND ch.enabled = 1
  AND ch.allow_embeddings = 1
`
	args := make([]any, 0, 8)
	if req.Filters.FromUnix > 0 {
		query += " AND m.ts >= ?"
		args = append(args, req.Filters.FromUnix)
	}
	if req.Filters.ToUnix > 0 {
		query += " AND m.ts <= ?"
		args = append(args, req.Filters.ToUnix)
	}
	if len(req.Filters.ChatIDs) > 0 {
		placeholders := make([]string, 0, len(req.Filters.ChatIDs))
		for _, id := range req.Filters.ChatIDs {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		query += " AND c.chat_id IN (" + strings.Join(placeholders, ",") + ")"
	}
	query += " ORDER BY m.ts DESC LIMIT ?"
	args = append(args, maxCandidates)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make([]domain.SearchResult, 0, limit)
	for rows.Next() {
		var (
			blob []byte
			item domain.SearchResult
		)
		if err := rows.Scan(&blob, &item.ChatID, &item.MsgID, &item.Timestamp, &item.ChatTitle, &item.Sender, &item.MessageText); err != nil {
			return nil, err
		}
		docVector := decodeFloat32Slice(blob)
		if len(docVector) == 0 || len(docVector) != len(vector) {
			continue
		}
		similarity := cosineSimilarity(vector, docVector)
		item.Score = 1 - similarity
		item.Snippet = item.MessageText
		results = append(results, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].Timestamp > results[j].Timestamp
		}
		return results[i].Score < results[j].Score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

type urlTaskPayload struct {
	ChatID int64  `json:"chat_id"`
	MsgID  int64  `json:"msg_id"`
	URL    string `json:"url"`
}

func (s *Store) EnqueueURLTask(ctx context.Context, chatID int64, msgID int64, rawURL string, priority int) error {
	if chatID == 0 || msgID == 0 || strings.TrimSpace(rawURL) == "" {
		return errors.New("invalid url task payload")
	}
	payload := urlTaskPayload{
		ChatID: chatID,
		MsgID:  msgID,
		URL:    strings.TrimSpace(rawURL),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	var exists int
	if err := s.db.QueryRowContext(ctx, `
SELECT 1
FROM tasks
WHERE type = 'url_fetch'
  AND payload = ?
LIMIT 1
`, string(encoded)).Scan(&exists); err == nil && exists == 1 {
		return nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	nowUnix := time.Now().Unix()
	_, err = s.db.ExecContext(ctx, `
INSERT INTO tasks(type, payload, state, attempts, next_run_at, priority, created_at)
VALUES('url_fetch', ?, 'pending', 0, ?, ?, ?)
`, string(encoded), nowUnix, priority, nowUnix)
	return err
}

func (s *Store) ClaimURLTask(ctx context.Context, nowUnix int64) (URLTask, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return URLTask{}, false, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var (
		taskID   int64
		payload  string
		attempts int
	)
	err = tx.QueryRowContext(ctx, `
SELECT task_id, payload, attempts
FROM tasks
WHERE type = 'url_fetch'
  AND state = 'pending'
  AND next_run_at <= ?
ORDER BY priority ASC, created_at ASC
LIMIT 1
`, nowUnix).Scan(&taskID, &payload, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return URLTask{}, false, nil
	}
	if err != nil {
		return URLTask{}, false, err
	}

	if _, err = tx.ExecContext(ctx, `
UPDATE tasks
SET state = 'running', attempts = attempts + 1
WHERE task_id = ?
`, taskID); err != nil {
		return URLTask{}, false, err
	}

	var decoded urlTaskPayload
	if decodeErr := json.Unmarshal([]byte(payload), &decoded); decodeErr != nil {
		return URLTask{}, false, decodeErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return URLTask{}, false, commitErr
	}

	return URLTask{
		TaskID:   taskID,
		ChatID:   decoded.ChatID,
		MsgID:    decoded.MsgID,
		URL:      decoded.URL,
		Attempts: attempts + 1,
	}, true, nil
}

func (s *Store) CompleteTask(ctx context.Context, taskID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET state = 'done' WHERE task_id = ?`, taskID)
	return err
}

func (s *Store) RetryTask(ctx context.Context, taskID int64, nextRunAt int64) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE tasks
SET state = 'pending', next_run_at = ?
WHERE task_id = ?
`, nextRunAt, taskID)
	return err
}

func (s *Store) FailTask(ctx context.Context, taskID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET state = 'failed' WHERE task_id = ?`, taskID)
	return err
}

func (s *Store) UpsertURLDoc(ctx context.Context, chatID int64, msgID int64, rawURL, finalURL, title, extractedText, hash string, fetchedAt int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	_, err = tx.ExecContext(ctx, `
INSERT INTO url_docs(chat_id, msg_id, url, final_url, title, extracted_text, hash, fetched_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(chat_id, msg_id, url) DO UPDATE SET
	final_url = excluded.final_url,
	title = excluded.title,
	extracted_text = excluded.extracted_text,
	hash = excluded.hash,
	fetched_at = excluded.fetched_at
`, chatID, msgID, rawURL, finalURL, title, extractedText, hash, fetchedAt)
	if err != nil {
		return err
	}

	var urlID int64
	if err = tx.QueryRowContext(ctx, `
SELECT url_id
FROM url_docs
WHERE chat_id = ? AND msg_id = ? AND url = ?
`, chatID, msgID, rawURL).Scan(&urlID); err != nil {
		return err
	}

	if _, err = tx.ExecContext(ctx, `DELETE FROM fts_url_docs WHERE rowid = ?`, urlID); err != nil {
		return err
	}
	if strings.TrimSpace(extractedText) != "" {
		if _, err = tx.ExecContext(ctx, `
INSERT INTO fts_url_docs(rowid, text, chat_id, msg_id, url)
VALUES(?, ?, ?, ?, ?)
`, urlID, extractedText, chatID, msgID, rawURL); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *Store) ListURLQueueCandidates(ctx context.Context, limit int) ([]URLCandidateMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT m.chat_id, m.msg_id, m.text, c.urls_mode
FROM messages m
JOIN chats c ON c.chat_id = m.chat_id
WHERE c.enabled = 1
  AND c.urls_mode IN ('lazy','full')
  AND m.deleted = 0
  AND m.has_url = 1
  AND NOT EXISTS (
    SELECT 1
    FROM url_docs d
    WHERE d.chat_id = m.chat_id AND d.msg_id = m.msg_id
  )
ORDER BY m.ts DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	candidates := make([]URLCandidateMessage, 0, limit)
	for rows.Next() {
		var item URLCandidateMessage
		if err := rows.Scan(&item.ChatID, &item.MsgID, &item.Text, &item.URLsMode); err != nil {
			return nil, err
		}
		candidates = append(candidates, item)
	}
	return candidates, rows.Err()
}

func (s *Store) Search(ctx context.Context, req domain.SearchRequest) ([]domain.SearchResult, error) {
	match, err := search.BuildFTSMatch(req.Query, req.Filters.Advanced)
	if err != nil {
		return nil, err
	}

	limit := req.Filters.Limit
	if limit <= 0 || limit > 100 {
		limit = 25
	}

	query := `
SELECT
	fts_chunks.chat_id,
	fts_chunks.msg_id,
	fts_chunks.ts,
	c.title,
	m.sender_display,
	m.text,
	COALESCE(snippet(fts_chunks, 0, '<mark>', '</mark>', '...', 18), m.text) AS snippet,
	bm25(fts_chunks) AS rank
FROM fts_chunks
JOIN messages m ON m.chat_id = fts_chunks.chat_id AND m.msg_id = fts_chunks.msg_id
JOIN chats c ON c.chat_id = fts_chunks.chat_id
WHERE fts_chunks MATCH ?
  AND m.deleted = 0
  AND c.enabled = 1
`
	args := []any{match}

	if req.Filters.FromUnix > 0 {
		query += " AND fts_chunks.ts >= ?"
		args = append(args, req.Filters.FromUnix)
	}
	if req.Filters.ToUnix > 0 {
		query += " AND fts_chunks.ts <= ?"
		args = append(args, req.Filters.ToUnix)
	}
	if req.Filters.HasURL != nil {
		query += " AND m.has_url = ?"
		args = append(args, boolToInt(*req.Filters.HasURL))
	}
	if len(req.Filters.ChatIDs) > 0 {
		placeholders := make([]string, 0, len(req.Filters.ChatIDs))
		for _, id := range req.Filters.ChatIDs {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		query += " AND fts_chunks.chat_id IN (" + strings.Join(placeholders, ",") + ")"
	}

	query += " ORDER BY rank ASC, fts_chunks.ts DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	merged := make(map[string]domain.SearchResult, limit)
	for rows.Next() {
		var item domain.SearchResult
		if err := rows.Scan(&item.ChatID, &item.MsgID, &item.Timestamp, &item.ChatTitle, &item.Sender, &item.MessageText, &item.Snippet, &item.Score); err != nil {
			return nil, err
		}
		if req.Mode == domain.SearchModeHybrid {
			item.Score = item.Score * 0.9
		}
		mergeSearchResult(merged, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if req.Filters.HasURL == nil || *req.Filters.HasURL {
		urlQuery := `
SELECT
	fts_url_docs.chat_id,
	fts_url_docs.msg_id,
	m.ts,
	c.title,
	m.sender_display,
	m.text,
	d.url,
	d.final_url,
	d.title,
	COALESCE(snippet(fts_url_docs, 0, '<mark>', '</mark>', '...', 18), m.text) AS snippet,
	bm25(fts_url_docs) AS rank
FROM fts_url_docs
JOIN messages m ON m.chat_id = fts_url_docs.chat_id AND m.msg_id = fts_url_docs.msg_id
JOIN chats c ON c.chat_id = fts_url_docs.chat_id
JOIN url_docs d ON d.url_id = fts_url_docs.rowid
WHERE fts_url_docs MATCH ?
  AND m.deleted = 0
  AND c.enabled = 1
`
		urlArgs := []any{match}
		if req.Filters.FromUnix > 0 {
			urlQuery += " AND m.ts >= ?"
			urlArgs = append(urlArgs, req.Filters.FromUnix)
		}
		if req.Filters.ToUnix > 0 {
			urlQuery += " AND m.ts <= ?"
			urlArgs = append(urlArgs, req.Filters.ToUnix)
		}
		if len(req.Filters.ChatIDs) > 0 {
			placeholders := make([]string, 0, len(req.Filters.ChatIDs))
			for _, id := range req.Filters.ChatIDs {
				placeholders = append(placeholders, "?")
				urlArgs = append(urlArgs, id)
			}
			urlQuery += " AND m.chat_id IN (" + strings.Join(placeholders, ",") + ")"
		}
		urlQuery += " ORDER BY rank ASC, m.ts DESC LIMIT ?"
		urlArgs = append(urlArgs, limit)

		urlRows, queryErr := s.db.QueryContext(ctx, urlQuery, urlArgs...)
		if queryErr != nil {
			return nil, queryErr
		}
		defer urlRows.Close()

		for urlRows.Next() {
			var item domain.SearchResult
			if err := urlRows.Scan(
				&item.ChatID,
				&item.MsgID,
				&item.Timestamp,
				&item.ChatTitle,
				&item.Sender,
				&item.MessageText,
				&item.URL,
				&item.URLFinal,
				&item.URLTitle,
				&item.Snippet,
				&item.Score,
			); err != nil {
				return nil, err
			}
			item.SourceType = "url"
			if req.Mode == domain.SearchModeHybrid {
				item.Score = item.Score * 0.9
			}
			mergeSearchResult(merged, item)
		}
		if err := urlRows.Err(); err != nil {
			return nil, err
		}
	}

	results := make([]domain.SearchResult, 0, len(merged))
	for _, item := range merged {
		results = append(results, item)
	}
	sort.Slice(results, func(i int, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].Timestamp > results[j].Timestamp
		}
		return results[i].Score < results[j].Score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func mergeSearchResult(merged map[string]domain.SearchResult, candidate domain.SearchResult) {
	key := strconv.FormatInt(candidate.ChatID, 10) + ":" + strconv.FormatInt(candidate.MsgID, 10)
	current, exists := merged[key]
	if !exists {
		merged[key] = candidate
		return
	}
	if candidate.Score < current.Score {
		merged[key] = mergeSearchFields(candidate, current)
		return
	}
	if candidate.Timestamp > current.Timestamp {
		current.Timestamp = candidate.Timestamp
	}
	merged[key] = mergeSearchFields(current, candidate)
}

func mergeSearchFields(target domain.SearchResult, source domain.SearchResult) domain.SearchResult {
	if target.Snippet == "" {
		target.Snippet = source.Snippet
	}
	if target.MessageText == "" {
		target.MessageText = source.MessageText
	}
	if target.SourceType == "" {
		target.SourceType = source.SourceType
	}
	if target.URL == "" {
		target.URL = source.URL
	}
	if target.URLFinal == "" {
		target.URLFinal = source.URLFinal
	}
	if target.URLTitle == "" {
		target.URLTitle = source.URLTitle
	}
	return target
}

func (s *Store) GetIndexStatus(ctx context.Context, mcpEndpoint string, mcpEnabled bool, mcpStatus string, mcpPort int) (domain.IndexStatus, error) {
	var (
		queueDepth int
		msgCount   int
		chunkCount int
	)
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM tasks WHERE state IN ('pending','running')`).Scan(&queueDepth); err != nil {
		return domain.IndexStatus{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM messages WHERE deleted = 0`).Scan(&msgCount); err != nil {
		return domain.IndexStatus{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM chunks WHERE deleted = 0`).Scan(&chunkCount); err != nil {
		return domain.IndexStatus{}, err
	}
	status := domain.IndexStatus{
		SyncState:         "idle",
		BackfillProgress:  0,
		QueueDepth:        queueDepth,
		LastSyncUnix:      time.Now().Unix(),
		MCPEndpoint:       mcpEndpoint,
		MCPEnabled:        mcpEnabled,
		MCPStatus:         mcpStatus,
		MCPPort:           mcpPort,
		MessageCount:      msgCount,
		IndexedChunkCount: chunkCount,
	}
	status.Touch()
	return status, nil
}

func (s *Store) SeedDemoData(ctx context.Context) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM chats`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	baseTime := time.Now().Add(-24 * time.Hour).Unix()
	chats := []domain.ChatPolicy{
		{ChatID: 1, Title: "Saved Messages", Type: "saved", Enabled: false, HistoryMode: "full", URLsMode: "off"},
		{ChatID: 2, Title: "Engineering", Type: "group", Enabled: true, HistoryMode: "full", URLsMode: "off"},
		{ChatID: 3, Title: "Product Notes", Type: "private", Enabled: true, HistoryMode: "full", URLsMode: "lazy"},
	}
	for _, chat := range chats {
		if err := s.UpsertChat(ctx, chat); err != nil {
			return err
		}
	}

	sample := []domain.Message{
		{ChatID: 2, MsgID: 101, Timestamp: baseTime + 60, SenderID: 10, SenderDisplay: "Alex", Text: "MCP streamable HTTP endpoint is ready on localhost", HasURL: false},
		{ChatID: 2, MsgID: 102, Timestamp: baseTime + 120, SenderID: 11, SenderDisplay: "Nina", Text: "Need SQLite FTS5 query parser with simple and advanced mode", HasURL: false},
		{ChatID: 3, MsgID: 201, Timestamp: baseTime + 180, SenderID: 12, SenderDisplay: "You", Text: "Review URL fetch safety baseline for SSRF before MVP", HasURL: false},
	}
	for _, msg := range sample {
		if err := s.UpsertMessage(ctx, msg); err != nil {
			return err
		}
	}
	return nil
}

func encodeFloat32Slice(values []float32) []byte {
	if len(values) == 0 {
		return nil
	}
	out := make([]byte, len(values)*4)
	for idx, value := range values {
		binary.LittleEndian.PutUint32(out[idx*4:], math.Float32bits(value))
	}
	return out
}

func decodeFloat32Slice(blob []byte) []float32 {
	if len(blob) == 0 || len(blob)%4 != 0 {
		return nil
	}
	out := make([]float32, len(blob)/4)
	for idx := 0; idx < len(out); idx++ {
		bits := binary.LittleEndian.Uint32(blob[idx*4:])
		out[idx] = math.Float32frombits(bits)
	}
	return out
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot float64
	var normA float64
	var normB float64
	for idx := range a {
		av := float64(a[idx])
		bv := float64(b[idx])
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
