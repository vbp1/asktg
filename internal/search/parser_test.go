package search

import "testing"

func TestBuildFTSMatchSimple(t *testing.T) {
	got, err := BuildFTSMatch(`roadmap "streamable http" -draft vector*`, false)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got == "" {
		t.Fatal("expected non-empty expression")
	}
}

func TestBuildFTSMatchRequiresPositive(t *testing.T) {
	_, err := BuildFTSMatch(`-secret -token`, true)
	if err == nil {
		t.Fatal("expected error for query with only negative terms")
	}
}
