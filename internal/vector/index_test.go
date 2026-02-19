package vector

import (
	"path/filepath"
	"testing"
)

func TestHNSWAddSearchAndDelete(t *testing.T) {
	index := NewHNSW(4, 16, 200, 64)
	if err := index.Add(101, []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("add 101 failed: %v", err)
	}
	if err := index.Add(102, []float32{0, 1, 0, 0}); err != nil {
		t.Fatalf("add 102 failed: %v", err)
	}
	if err := index.Add(103, []float32{0, 0, 1, 0}); err != nil {
		t.Fatalf("add 103 failed: %v", err)
	}

	results := index.Search([]float32{1, 0, 0, 0}, 3)
	if len(results) == 0 {
		t.Fatal("expected search results")
	}
	if results[0].ChunkID != 101 {
		t.Fatalf("expected closest chunk 101, got %d", results[0].ChunkID)
	}

	index.MarkDeleted(101)
	results = index.Search([]float32{1, 0, 0, 0}, 3)
	for _, item := range results {
		if item.ChunkID == 101 {
			t.Fatal("deleted chunk appeared in search results")
		}
	}
}

func TestHNSWSaveLoad(t *testing.T) {
	index := NewHNSW(3, 16, 200, 64)
	if err := index.Add(11, []float32{0.2, 0.4, 0.6}); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	if err := index.Add(12, []float32{0.8, 0.1, 0.2}); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	index.MarkDeleted(12)

	path := filepath.Join(t.TempDir(), "vectors.graph")
	if err := index.Save(path); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded := NewHNSW(3, 16, 200, 64)
	if err := loaded.Load(path); err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.Len() != 1 {
		t.Fatalf("expected one active vector after load, got %d", loaded.Len())
	}
	results := loaded.Search([]float32{0.2, 0.4, 0.6}, 2)
	if len(results) == 0 {
		t.Fatal("expected loaded search results")
	}
	if results[0].ChunkID != 11 {
		t.Fatalf("expected chunk 11 after load, got %d", results[0].ChunkID)
	}
}
