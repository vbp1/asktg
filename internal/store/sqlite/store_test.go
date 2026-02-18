package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"asktg/internal/domain"
)

func TestPurgeChatData(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedStoreData(t, store, ctx)

	if err := store.PurgeChatData(ctx, 1); err != nil {
		t.Fatalf("purge chat failed: %v", err)
	}

	assertCount(t, store, `SELECT COUNT(1) FROM messages WHERE chat_id = 1`, 0)
	assertCount(t, store, `SELECT COUNT(1) FROM messages WHERE chat_id = 2`, 1)
	assertCount(t, store, `SELECT COUNT(1) FROM url_docs WHERE chat_id = 1`, 0)
	assertCount(t, store, `SELECT COUNT(1) FROM url_docs WHERE chat_id = 2`, 1)
	assertCount(t, store, `SELECT COUNT(1) FROM file_docs WHERE chat_id = 1`, 0)
	assertCount(t, store, `SELECT COUNT(1) FROM file_docs WHERE chat_id = 2`, 1)
	assertCount(t, store, `SELECT COUNT(1) FROM tasks WHERE type = 'url_fetch' AND payload LIKE '%"chat_id":1,%'`, 0)
	assertCount(t, store, `SELECT COUNT(1) FROM tasks WHERE type = 'url_fetch' AND payload LIKE '%"chat_id":2,%'`, 1)
	assertCount(t, store, `SELECT COUNT(1) FROM tasks WHERE type = 'pdf_fetch_tg' AND payload LIKE '%"chat_id":1,%'`, 0)
	assertCount(t, store, `SELECT COUNT(1) FROM tasks WHERE type = 'pdf_fetch_tg' AND payload LIKE '%"chat_id":2,%'`, 1)
	assertCount(t, store, `SELECT COUNT(1) FROM tasks WHERE type = 'embed_file' AND payload LIKE '%"chat_id":1,%'`, 0)
	assertCount(t, store, `SELECT COUNT(1) FROM tasks WHERE type = 'embed_file' AND payload LIKE '%"chat_id":2,%'`, 1)

	chatOne, err := store.GetChatPolicy(ctx, 1)
	if err != nil {
		t.Fatalf("load chat one failed: %v", err)
	}
	if chatOne.SyncCursor != "" || chatOne.LastMessageUnix != 0 || chatOne.LastSyncedUnix != 0 {
		t.Fatalf("chat one cursor fields were not reset: %+v", chatOne)
	}

	chatTwo, err := store.GetChatPolicy(ctx, 2)
	if err != nil {
		t.Fatalf("load chat two failed: %v", err)
	}
	if chatTwo.SyncCursor == "" || chatTwo.LastMessageUnix == 0 || chatTwo.LastSyncedUnix == 0 {
		t.Fatalf("chat two cursor fields should remain set: %+v", chatTwo)
	}
}

func TestPurgeAllData(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedStoreData(t, store, ctx)

	if err := store.PurgeAllData(ctx); err != nil {
		t.Fatalf("purge all failed: %v", err)
	}

	assertCount(t, store, `SELECT COUNT(1) FROM messages`, 0)
	assertCount(t, store, `SELECT COUNT(1) FROM chunks`, 0)
	assertCount(t, store, `SELECT COUNT(1) FROM fts_chunks`, 0)
	assertCount(t, store, `SELECT COUNT(1) FROM url_docs`, 0)
	assertCount(t, store, `SELECT COUNT(1) FROM fts_url_docs`, 0)
	assertCount(t, store, `SELECT COUNT(1) FROM file_docs`, 0)
	assertCount(t, store, `SELECT COUNT(1) FROM fts_file_docs`, 0)
	assertCount(t, store, `SELECT COUNT(1) FROM message_files`, 0)
	assertCount(t, store, `SELECT COUNT(1) FROM tg_files`, 0)
	assertCount(t, store, `SELECT COUNT(1) FROM tasks`, 0)
	assertCount(t, store, `SELECT COUNT(1) FROM chats`, 2)

	chats, err := store.ListChats(ctx)
	if err != nil {
		t.Fatalf("list chats failed: %v", err)
	}
	for _, chat := range chats {
		if chat.SyncCursor != "" || chat.LastMessageUnix != 0 || chat.LastSyncedUnix != 0 {
			t.Fatalf("chat cursor fields were not reset: %+v", chat)
		}
	}
}

func TestExportDatabaseSnapshot(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.SetSetting(ctx, "hello", "world"); err != nil {
		t.Fatalf("set setting failed: %v", err)
	}
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.db")
	if err := store.ExportDatabaseSnapshot(ctx, snapshotPath); err != nil {
		t.Fatalf("snapshot export failed: %v", err)
	}

	snapshot, err := Open(snapshotPath)
	if err != nil {
		t.Fatalf("open snapshot failed: %v", err)
	}
	defer snapshot.Close()

	val, err := snapshot.GetSetting(ctx, "hello", "")
	if err != nil {
		t.Fatalf("read snapshot setting failed: %v", err)
	}
	if val != "world" {
		t.Fatalf("unexpected snapshot value: %q", val)
	}
}

func TestSearchByEmbedding(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedStoreData(t, store, ctx)
	if err := store.SetChatPolicy(ctx, 1, true, "lazy", true, "lazy", "off"); err != nil {
		t.Fatalf("set chat policy failed: %v", err)
	}

	candidates, err := store.ListEmbeddingCandidates(ctx, 10)
	if err != nil {
		t.Fatalf("list embedding candidates failed: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("expected no historical candidates immediately after enabling embeddings, got=%d", len(candidates))
	}

	newMessage := domain.Message{
		ChatID:        1,
		MsgID:         101,
		Timestamp:     time.Now().Unix() + 1,
		SenderID:      11,
		SenderDisplay: "alice",
		Text:          "fresh message for embeddings",
		HasURL:        false,
	}
	if err := store.UpsertMessage(ctx, newMessage); err != nil {
		t.Fatalf("upsert fresh message failed: %v", err)
	}

	candidates, err = store.ListEmbeddingCandidates(ctx, 10)
	if err != nil {
		t.Fatalf("list embedding candidates failed: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("expected embedding candidates for fresh messages")
	}
	if err := store.UpsertEmbedding(ctx, candidates[0].ChunkID, "test-model", []float32{1, 0, 0}, time.Now().Unix()); err != nil {
		t.Fatalf("upsert embedding failed: %v", err)
	}

	req := domain.SearchRequest{
		Query: "test",
		Mode:  domain.SearchModeHybrid,
		Filters: domain.SearchFilters{
			Limit:   5,
			ChatIDs: []int64{candidates[0].ChatID},
		},
	}
	results, err := store.SearchByEmbedding(ctx, req, []float32{1, 0, 0}, 100)
	if err != nil {
		t.Fatalf("search by embedding failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected embedding search result")
	}
	if results[0].ChatID != candidates[0].ChatID || results[0].MsgID != candidates[0].MsgID {
		t.Fatalf("unexpected result: %+v", results[0])
	}
}

func TestEmbeddingCandidatesBackfillWhenHistoryFull(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedStoreData(t, store, ctx)

	if err := store.SetChatPolicy(ctx, 1, true, "full", true, "lazy", "off"); err != nil {
		t.Fatalf("set chat policy failed: %v", err)
	}

	candidates, err := store.ListEmbeddingCandidates(ctx, 10)
	if err != nil {
		t.Fatalf("list embedding candidates failed: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("expected historical candidates in backfill mode")
	}
}

func TestSearchIncludesPDFFileDocs(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedStoreData(t, store, ctx)

	req := domain.SearchRequest{
		Query: "pdf",
		Mode:  domain.SearchModeFTS,
		Filters: domain.SearchFilters{
			Limit:   10,
			ChatIDs: []int64{1, 2},
		},
	}
	results, err := store.Search(ctx, req)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results")
	}
	found := false
	for _, r := range results {
		if r.ChatID == 1 && r.MsgID == 100 && r.SourceType == "file" && r.FileName == "one.pdf" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected pdf file-doc result, got: %+v", results)
	}
}

func TestChatReactionModeDefaultsOffAndPersists(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.UpsertChat(ctx, domain.ChatPolicy{
		ChatID:      1,
		Title:       "chat",
		Type:        "private",
		Enabled:     true,
		HistoryMode: "full",
		URLsMode:    "off",
	}); err != nil {
		t.Fatalf("upsert chat failed: %v", err)
	}

	chat, err := store.GetChatPolicy(ctx, 1)
	if err != nil {
		t.Fatalf("get chat policy failed: %v", err)
	}
	if chat.ReactionMode != "off" {
		t.Fatalf("expected default reaction mode off, got %q", chat.ReactionMode)
	}

	if err := store.SetChatPolicy(ctx, 1, true, "full", false, "off", "eyes_reaction"); err != nil {
		t.Fatalf("set chat policy failed: %v", err)
	}
	chat, err = store.GetChatPolicy(ctx, 1)
	if err != nil {
		t.Fatalf("get chat policy after update failed: %v", err)
	}
	if chat.ReactionMode != "eyes_reaction" {
		t.Fatalf("expected reaction mode eyes_reaction, got %q", chat.ReactionMode)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open store failed: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	return store
}

func seedStoreData(t *testing.T, store *Store, ctx context.Context) {
	t.Helper()
	now := time.Now().Unix() - 10
	for _, chat := range []domain.ChatPolicy{
		{ChatID: 1, Title: "chat one", Type: "private", Enabled: true, HistoryMode: "full", URLsMode: "lazy"},
		{ChatID: 2, Title: "chat two", Type: "private", Enabled: true, HistoryMode: "full", URLsMode: "lazy"},
	} {
		if err := store.UpsertChat(ctx, chat); err != nil {
			t.Fatalf("upsert chat failed: %v", err)
		}
	}
	if err := store.UpdateChatSyncState(ctx, 1, "cursor-1", now, now); err != nil {
		t.Fatalf("update sync one failed: %v", err)
	}
	if err := store.UpdateChatSyncState(ctx, 2, "cursor-2", now, now); err != nil {
		t.Fatalf("update sync two failed: %v", err)
	}

	for _, msg := range []domain.Message{
		{
			ChatID:        1,
			MsgID:         100,
			Timestamp:     now,
			SenderID:      11,
			SenderDisplay: "alice",
			Text:          "https://example.com/one",
			HasURL:        true,
		},
		{
			ChatID:        2,
			MsgID:         200,
			Timestamp:     now,
			SenderID:      22,
			SenderDisplay: "bob",
			Text:          "https://example.com/two",
			HasURL:        true,
		},
	} {
		if err := store.UpsertMessage(ctx, msg); err != nil {
			t.Fatalf("upsert message failed: %v", err)
		}
	}

	if err := store.UpsertURLDoc(ctx, 1, 100, "https://example.com/one", "https://example.com/one", "one", "body one", "text/html", "h1", now); err != nil {
		t.Fatalf("upsert url one failed: %v", err)
	}
	if err := store.UpsertURLDoc(ctx, 2, 200, "https://example.com/two", "https://example.com/two", "two", "body two", "text/html", "h2", now); err != nil {
		t.Fatalf("upsert url two failed: %v", err)
	}

	if err := store.EnqueueURLTask(ctx, 1, 100, "https://example.com/one", 10); err != nil {
		t.Fatalf("enqueue one failed: %v", err)
	}
	if err := store.EnqueueURLTask(ctx, 2, 200, "https://example.com/two", 10); err != nil {
		t.Fatalf("enqueue two failed: %v", err)
	}

	for _, f := range []TGFile{
		{DocumentID: 1001, AccessHash: 5001, DCID: 2, FileReference: []byte{1, 2, 3}, Mime: "application/pdf", Size: 12345, Filename: "one.pdf", UpdatedAt: now},
		{DocumentID: 2001, AccessHash: 6001, DCID: 2, FileReference: []byte{4, 5, 6}, Mime: "application/pdf", Size: 23456, Filename: "two.pdf", UpdatedAt: now},
	} {
		if err := store.UpsertTGFile(ctx, f); err != nil {
			t.Fatalf("upsert tg file failed: %v", err)
		}
	}
	if err := store.LinkMessageFile(ctx, 1, 100, 1001); err != nil {
		t.Fatalf("link msg file one failed: %v", err)
	}
	if err := store.LinkMessageFile(ctx, 2, 200, 2001); err != nil {
		t.Fatalf("link msg file two failed: %v", err)
	}
	docOne, err := store.UpsertFileDoc(ctx, 1, 100, 1001, "one.pdf", "application/pdf", 12345, "pdf body one", "ph1", now)
	if err != nil {
		t.Fatalf("upsert file doc one failed: %v", err)
	}
	docTwo, err := store.UpsertFileDoc(ctx, 2, 200, 2001, "two.pdf", "application/pdf", 23456, "pdf body two", "ph2", now)
	if err != nil {
		t.Fatalf("upsert file doc two failed: %v", err)
	}
	if err := store.EnqueuePDFTask(ctx, 1, 100, 1001, 10); err != nil {
		t.Fatalf("enqueue pdf one failed: %v", err)
	}
	if err := store.EnqueuePDFTask(ctx, 2, 200, 2001, 10); err != nil {
		t.Fatalf("enqueue pdf two failed: %v", err)
	}
	if err := store.EnqueueFileEmbeddingTask(ctx, 1, 100, docOne, 10); err != nil {
		t.Fatalf("enqueue embed file one failed: %v", err)
	}
	if err := store.EnqueueFileEmbeddingTask(ctx, 2, 200, docTwo, 10); err != nil {
		t.Fatalf("enqueue embed file two failed: %v", err)
	}
}

func assertCount(t *testing.T, store *Store, query string, expected int) {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(context.Background(), query).Scan(&count); err != nil {
		t.Fatalf("query failed: %s err=%v", query, err)
	}
	if count != expected {
		t.Fatalf("query %q expected=%d got=%d", query, expected, count)
	}
}
