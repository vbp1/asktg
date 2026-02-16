package main

import (
	"testing"
	"time"

	"asktg/internal/domain"
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
