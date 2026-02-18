package main

import "testing"

func TestTrackReadMaxMessageID(t *testing.T) {
	got := map[int64]int64{}

	trackReadMaxMessageID(got, 101, 5)
	trackReadMaxMessageID(got, 101, 3)
	trackReadMaxMessageID(got, 101, 9)
	trackReadMaxMessageID(got, 0, 10)
	trackReadMaxMessageID(got, 102, 0)

	if len(got) != 1 {
		t.Fatalf("expected one chat marker, got %d", len(got))
	}
	if got[101] != 9 {
		t.Fatalf("expected max message id 9, got %d", got[101])
	}
}
