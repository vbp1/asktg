package main

import (
	"testing"
	"time"

	"asktg/internal/domain"
	"asktg/internal/vector"
)

func TestFuseByRRFRecencyBoost(t *testing.T) {
	now := time.Now().Unix()
	old := domain.SearchResult{
		ChatID:    101,
		MsgID:     1,
		Timestamp: now - 14*24*3600,
		Snippet:   "older message",
	}
	recent := domain.SearchResult{
		ChatID:    202,
		MsgID:     2,
		Timestamp: now - 30*60,
		Snippet:   "recent message",
	}

	fused := fuseByRRF([]domain.SearchResult{old}, []domain.SearchResult{recent}, 10)
	if len(fused) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(fused))
	}
	if fused[0].ChatID != recent.ChatID || fused[0].MsgID != recent.MsgID {
		t.Fatalf("expected recent result first, got chat=%d msg=%d", fused[0].ChatID, fused[0].MsgID)
	}
}

func TestFuseByRRFKeepsSemanticSimilarity(t *testing.T) {
	fts := domain.SearchResult{
		ChatID:    303,
		MsgID:     9,
		Timestamp: time.Now().Unix(),
		Snippet:   "fts snippet",
		MatchFTS:  true,
	}
	semantic := domain.SearchResult{
		ChatID:             303,
		MsgID:              9,
		Timestamp:          fts.Timestamp,
		Snippet:            "semantic snippet",
		MatchSemantic:      true,
		SemanticSimilarity: 0.8123,
	}

	fused := fuseByRRF([]domain.SearchResult{fts}, []domain.SearchResult{semantic}, 10)
	if len(fused) != 1 {
		t.Fatalf("expected one fused result, got %d", len(fused))
	}
	if !fused[0].MatchSemantic {
		t.Fatalf("expected fused result to keep semantic flag")
	}
	if fused[0].SemanticSimilarity != semantic.SemanticSimilarity {
		t.Fatalf("expected semantic similarity %.4f, got %.4f", semantic.SemanticSimilarity, fused[0].SemanticSimilarity)
	}
}

func TestSemanticLatinAnchorQuery(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{name: "mixed query", query: "логи postgres", want: "postgres logs"},
		{name: "mixed multiple latin tokens", query: "ошибка postgres wal", want: "postgres wal error"},
		{name: "inflected tracing", query: "трейсинг postgres", want: "postgres tracing"},
		{name: "workload phrase", query: "генератор нагрузки postgres", want: "postgres generator workload"},
		{name: "latin only", query: "postgres logs", want: ""},
		{name: "cyrillic only", query: "логи постгрес", want: ""},
		{name: "dedupe latin", query: "ошибка postgres postgres", want: "postgres error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := semanticLatinAnchorQuery(tc.query)
			if got != tc.want {
				t.Fatalf("semanticLatinAnchorQuery(%q)=%q want=%q", tc.query, got, tc.want)
			}
		})
	}
}

func TestMergeVectorCandidates(t *testing.T) {
	primary := []vector.Candidate{
		{ChunkID: 101, Distance: 0.10, RawRank: 1},
		{ChunkID: 102, Distance: 0.20, RawRank: 2},
		{ChunkID: 103, Distance: 0.30, RawRank: 3},
	}
	secondary := []vector.Candidate{
		{ChunkID: 201, Distance: 0.11, RawRank: 1},
		{ChunkID: 102, Distance: 0.19, RawRank: 2},
	}

	merged := mergeVectorCandidates(primary, secondary, 0)
	if len(merged) != 4 {
		t.Fatalf("expected 4 merged candidates, got %d", len(merged))
	}
	if merged[0].ChunkID != 101 || merged[1].ChunkID != 201 || merged[2].ChunkID != 102 {
		t.Fatalf("unexpected order after merge: %#v", merged)
	}
	if merged[2].Distance != 0.19 {
		t.Fatalf("expected duplicate chunk to keep better distance, got %.2f", merged[2].Distance)
	}
	if merged[0].RawRank != 1 || merged[1].RawRank != 2 || merged[2].RawRank != 3 {
		t.Fatalf("expected ranks to be normalized after merge, got %#v", merged)
	}
}
