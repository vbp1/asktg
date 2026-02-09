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
