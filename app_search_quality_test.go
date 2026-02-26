//go:build quality
// +build quality

package main

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"testing"

	"asktg/internal/config"
	"asktg/internal/domain"
	"asktg/internal/store/sqlite"

	_ "modernc.org/sqlite"
)

type crossLangCase struct {
	Name    string
	ChatID  int64
	MsgID   int64
	QueryEN string
	QueryRU string
}

type evalMetrics struct {
	Evaluated int

	ENHit5  int
	ENHit10 int
	ENMRR   float64
	ENNDCG  float64

	RUHit5  int
	RUHit10 int
	RUMRR   float64
	RUNDCG  float64
}

var crossLangCases = []crossLangCase{
	{Name: "postgres logs thread", ChatID: -1003799435561, MsgID: 238, QueryEN: "postgres logs", QueryRU: "\u043b\u043e\u0433\u0438 postgres"},
	{Name: "pg_stat_ch clickhouse metrics", ChatID: -1003799435561, MsgID: 413, QueryEN: "postgres clickhouse metrics", QueryRU: "\u043c\u0435\u0442\u0440\u0438\u043a\u0438 postgres clickhouse"},
	{Name: "pg_tracing distributed tracing", ChatID: -1003799435561, MsgID: 415, QueryEN: "postgres tracing", QueryRU: "\u0442\u0440\u0435\u0439\u0441\u0438\u043d\u0433 postgres"},
	{Name: "pg_stat_monitor query performance", ChatID: -1003799435561, MsgID: 416, QueryEN: "postgres query performance monitoring", QueryRU: "\u043c\u043e\u043d\u0438\u0442\u043e\u0440\u0438\u043d\u0433 \u043f\u0440\u043e\u0438\u0437\u0432\u043e\u0434\u0438\u0442\u0435\u043b\u044c\u043d\u043e\u0441\u0442\u0438 \u0437\u0430\u043f\u0440\u043e\u0441\u043e\u0432 postgres"},
	{Name: "postgres 18 aio tuning", ChatID: -1003799435561, MsgID: 370, QueryEN: "postgresql 18 aio tuning", QueryRU: "\u043d\u0430\u0441\u0442\u0440\u043e\u0439\u043a\u0430 aio postgresql 18"},
	{Name: "noisia workload generator", ChatID: -1003799435561, MsgID: 247, QueryEN: "postgres workload generator", QueryRU: "\u0433\u0435\u043d\u0435\u0440\u0430\u0442\u043e\u0440 \u043d\u0430\u0433\u0440\u0443\u0437\u043a\u0438 postgres"},
	{Name: "postgres hooks", ChatID: -1003799435561, MsgID: 235, QueryEN: "postgres hooks", QueryRU: "\u0445\u0443\u043a\u0438 postgres"},
	{Name: "explain visualizer", ChatID: -1003799435561, MsgID: 191, QueryEN: "postgres explain visualizer", QueryRU: "\u0432\u0438\u0437\u0443\u0430\u043b\u0438\u0437\u0430\u0442\u043e\u0440 explain postgres"},
	{Name: "postgres cache restore", ChatID: -1003799435561, MsgID: 189, QueryEN: "postgres cache restore", QueryRU: "\u0432\u043e\u0441\u0441\u0442\u0430\u043d\u043e\u0432\u043b\u0435\u043d\u0438\u0435 \u043a\u044d\u0448\u0430 postgres"},
	{Name: "query router", ChatID: -1003799435561, MsgID: 172, QueryEN: "postgres query router", QueryRU: "\u0440\u043e\u0443\u0442\u0435\u0440 \u0437\u0430\u043f\u0440\u043e\u0441\u043e\u0432 postgres"},
	{Name: "pgbouncer dashboard", ChatID: -1003799435561, MsgID: 161, QueryEN: "pgbouncer dashboard", QueryRU: "\u0434\u0430\u0448\u0431\u043e\u0440\u0434 pgbouncer"},
	{Name: "index health", ChatID: -1003799435561, MsgID: 200, QueryEN: "postgres index analysis tool", QueryRU: "\u0430\u043d\u0430\u043b\u0438\u0437 \u0438\u043d\u0434\u0435\u043a\u0441\u043e\u0432 postgres"},
	{Name: "postgres backup module", ChatID: -1003799435561, MsgID: 201, QueryEN: "postgres backup module", QueryRU: "\u043c\u043e\u0434\u0443\u043b\u044c \u0440\u0435\u0437\u0435\u0440\u0432\u043d\u043e\u0433\u043e \u043a\u043e\u043f\u0438\u0440\u043e\u0432\u0430\u043d\u0438\u044f postgres"},
	{Name: "tuned acceleration", ChatID: -1003799435561, MsgID: 194, QueryEN: "postgres tuned performance", QueryRU: "\u0443\u0441\u043a\u043e\u0440\u0435\u043d\u0438\u0435 postgres tuned"},
	{Name: "useful sql queries", ChatID: -1003799435561, MsgID: 125, QueryEN: "useful sql queries postgres", QueryRU: "\u043f\u043e\u043b\u0435\u0437\u043d\u044b\u0435 sql \u0437\u0430\u043f\u0440\u043e\u0441\u044b postgres"},
	{Name: "harness engineering openai", ChatID: -1003799435561, MsgID: 451, QueryEN: "harness engineering codex", QueryRU: "\u0438\u043d\u0436\u0435\u043d\u0435\u0440\u0438\u044f harness codex"},
	{Name: "claude cowork plugins", ChatID: -1003799435561, MsgID: 440, QueryEN: "claude cowork plugins", QueryRU: "\u043f\u043b\u0430\u0433\u0438\u043d\u044b claude \u0434\u043b\u044f \u043a\u043e\u043c\u0430\u043d\u0434"},
	{Name: "anthropic plugins", ChatID: -1003799435561, MsgID: 447, QueryEN: "anthropic knowledge work plugins", QueryRU: "\u043f\u043b\u0430\u0433\u0438\u043d\u044b anthropic knowledge work"},
	{Name: "aichat cli", ChatID: -1003799435561, MsgID: 446, QueryEN: "aichat llm cli", QueryRU: "cli \u0438\u043d\u0441\u0442\u0440\u0443\u043c\u0435\u043d\u0442 aichat llm"},
	{Name: "hodoscope agents", ChatID: -1003799435561, MsgID: 453, QueryEN: "hodoscope ai agents", QueryRU: "hodoscope ai \u0430\u0433\u0435\u043d\u0442\u044b"},
}

func TestSearchQualityIndexSnapshot(t *testing.T) {
	dbPath := config.Load().DBPath()
	if _, err := os.Stat(dbPath); err != nil {
		t.Skipf("db not found: %s (%v)", dbPath, err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	type stat struct {
		Name  string
		Count int
	}
	rows, err := db.Query(`
SELECT 'messages' AS name, COUNT(1) FROM messages
UNION ALL SELECT 'chunks', COUNT(1) FROM chunks
UNION ALL SELECT 'embeddings', COUNT(1) FROM embeddings
UNION ALL SELECT 'url_docs', COUNT(1) FROM url_docs
UNION ALL SELECT 'url_embeddings', COUNT(1) FROM url_embeddings
UNION ALL SELECT 'file_docs', COUNT(1) FROM file_docs
UNION ALL SELECT 'file_embeddings', COUNT(1) FROM file_embeddings
`)
	if err != nil {
		t.Fatalf("query stats: %v", err)
	}
	defer rows.Close()
	stats := make([]stat, 0, 8)
	for rows.Next() {
		var s stat
		if scanErr := rows.Scan(&s.Name, &s.Count); scanErr != nil {
			t.Fatalf("scan stats: %v", scanErr)
		}
		stats = append(stats, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows stats err: %v", err)
	}
	if len(stats) == 0 {
		t.Fatal("empty index snapshot")
	}
	for _, s := range stats {
		t.Logf("index.%s=%d", s.Name, s.Count)
	}

	chatRows, err := db.Query(`
SELECT c.chat_id, c.title, COUNT(1) AS cnt
FROM messages m
JOIN chats c ON c.chat_id = m.chat_id
WHERE m.deleted = 0
GROUP BY c.chat_id, c.title
ORDER BY cnt DESC
LIMIT 5
`)
	if err != nil {
		t.Fatalf("query chats: %v", err)
	}
	defer chatRows.Close()
	for chatRows.Next() {
		var chatID int64
		var title string
		var cnt int
		if scanErr := chatRows.Scan(&chatID, &title, &cnt); scanErr != nil {
			t.Fatalf("scan chats: %v", scanErr)
		}
		t.Logf("chat[%d] title=%q messages=%d", chatID, title, cnt)
	}
	if err := chatRows.Err(); err != nil {
		t.Fatalf("rows chats err: %v", err)
	}
}

func TestSearchQualityCrossLanguageComparison(t *testing.T) {
	app := openQualityEvalApp(t)
	limit := 20

	metrics := evalMetrics{}
	for _, tc := range crossLangCases {
		if _, err := app.GetMessage(tc.ChatID, tc.MsgID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				t.Logf("skip missing target %s chat=%d msg=%d", tc.Name, tc.ChatID, tc.MsgID)
				continue
			}
			t.Fatalf("load target %s: %v", tc.Name, err)
		}

		enResults, err := app.searchMessages(app.ctx, domain.SearchRequest{
			Query: tc.QueryEN,
			Mode:  domain.SearchModeHybrid,
			Filters: domain.SearchFilters{
				Limit: limit,
			},
		})
		if err != nil {
			t.Fatalf("search EN %s: %v", tc.Name, err)
		}

		ruResults, err := app.searchMessages(app.ctx, domain.SearchRequest{
			Query: tc.QueryRU,
			Mode:  domain.SearchModeHybrid,
			Filters: domain.SearchFilters{
				Limit: limit,
			},
		})
		if err != nil {
			t.Fatalf("search RU %s: %v", tc.Name, err)
		}

		enRank := rankOfTarget(enResults, tc.ChatID, tc.MsgID)
		ruRank := rankOfTarget(ruResults, tc.ChatID, tc.MsgID)

		metrics.Evaluated++
		if enRank > 0 && enRank <= 5 {
			metrics.ENHit5++
		}
		if enRank > 0 && enRank <= 10 {
			metrics.ENHit10++
		}
		metrics.ENMRR += reciprocalRank(enRank)
		metrics.ENNDCG += ndcgAt(limit, enRank)

		if ruRank > 0 && ruRank <= 5 {
			metrics.RUHit5++
		}
		if ruRank > 0 && ruRank <= 10 {
			metrics.RUHit10++
		}
		metrics.RUMRR += reciprocalRank(ruRank)
		metrics.RUNDCG += ndcgAt(limit, ruRank)

		t.Logf(
			"case=%q target=%d/%d enRank=%d ruRank=%d enQ=%q ruQ=%q",
			tc.Name, tc.ChatID, tc.MsgID, enRank, ruRank, tc.QueryEN, tc.QueryRU,
		)
	}

	if metrics.Evaluated < 12 {
		t.Fatalf("not enough evaluated cases: got=%d want>=12", metrics.Evaluated)
	}

	enHit5 := float64(metrics.ENHit5) / float64(metrics.Evaluated)
	enHit10 := float64(metrics.ENHit10) / float64(metrics.Evaluated)
	enMRR := metrics.ENMRR / float64(metrics.Evaluated)
	enNDCG := metrics.ENNDCG / float64(metrics.Evaluated)

	ruHit5 := float64(metrics.RUHit5) / float64(metrics.Evaluated)
	ruHit10 := float64(metrics.RUHit10) / float64(metrics.Evaluated)
	ruMRR := metrics.RUMRR / float64(metrics.Evaluated)
	ruNDCG := metrics.RUNDCG / float64(metrics.Evaluated)

	t.Logf("evaluated=%d", metrics.Evaluated)
	t.Logf("EN hit@5=%.3f hit@10=%.3f mrr=%.3f ndcg@%d=%.3f", enHit5, enHit10, enMRR, limit, enNDCG)
	t.Logf("RU hit@5=%.3f hit@10=%.3f mrr=%.3f ndcg@%d=%.3f", ruHit5, ruHit10, ruMRR, limit, ruNDCG)
	t.Logf("RU/EN ratio hit@10=%.3f mrr=%.3f ndcg=%.3f", safeRatio(ruHit10, enHit10), safeRatio(ruMRR, enMRR), safeRatio(ruNDCG, enNDCG))

	// Guardrails keep this suite useful for regression tracking, while still
	// allowing realistic cross-language degradation on mixed corpora.
	if enHit10 < 0.60 {
		t.Fatalf("EN baseline too low: hit@10=%.3f", enHit10)
	}
	if ruHit10 < 0.20 {
		t.Fatalf("RU quality too low: hit@10=%.3f", ruHit10)
	}
	if safeRatio(ruHit10, enHit10) < 0.35 {
		t.Fatalf("RU/EN hit@10 parity too low: ru=%.3f en=%.3f ratio=%.3f", ruHit10, enHit10, safeRatio(ruHit10, enHit10))
	}
}

func openQualityEvalApp(t *testing.T) *App {
	t.Helper()
	cfg := config.Load()
	if _, err := os.Stat(cfg.DBPath()); err != nil {
		t.Skipf("db not found: %s (%v)", cfg.DBPath(), err)
	}
	store, err := sqlite.Open(cfg.DBPath())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate store: %v", err)
	}
	app := NewApp()
	app.ctx = context.Background()
	app.cfg = cfg
	app.store = store
	app.configureEmbeddingsFromStore(app.ctx)
	app.bootstrapVectorIndex(app.ctx)
	if app.embedClient == nil || !app.embedClient.Configured() {
		t.Skip("embeddings are not configured")
	}
	if app.vectorIndex == nil || app.vectorIndex.Len() == 0 {
		t.Skip("vector index is empty")
	}
	return app
}

func rankOfTarget(results []domain.SearchResult, chatID, msgID int64) int {
	for idx, item := range results {
		if item.ChatID == chatID && item.MsgID == msgID {
			return idx + 1
		}
	}
	return 0
}

func reciprocalRank(rank int) float64 {
	if rank <= 0 {
		return 0
	}
	return 1.0 / float64(rank)
}

func ndcgAt(limit int, rank int) float64 {
	if limit <= 0 || rank <= 0 || rank > limit {
		return 0
	}
	// Single relevant document variant: DCG = 1/log2(rank+1), IDCG = 1.
	return 1.0 / math.Log2(float64(rank)+1.0)
}

func safeRatio(a, b float64) float64 {
	if b <= 0 {
		return 0
	}
	return a / b
}

