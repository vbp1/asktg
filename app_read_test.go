package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"asktg/internal/urlfetch"
)

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

func TestSameInt64Set(t *testing.T) {
	if !sameInt64Set([]int64{1, 2, 3}, []int64{3, 2, 1}) {
		t.Fatal("expected sets to be equal")
	}
	if sameInt64Set([]int64{1, 2}, []int64{1, 2, 3}) {
		t.Fatal("expected sets with different size to differ")
	}
	if sameInt64Set([]int64{1, 2, 3}, []int64{1, 2, 4}) {
		t.Fatal("expected sets with different elements to differ")
	}
}

func TestGrowRealtimeBackoff(t *testing.T) {
	if got := growRealtimeBackoff(0); got != realtimeReconnectBase {
		t.Fatalf("expected base backoff %v, got %v", realtimeReconnectBase, got)
	}
	if got := growRealtimeBackoff(realtimeReconnectMax); got != realtimeReconnectMax {
		t.Fatalf("expected capped backoff %v, got %v", realtimeReconnectMax, got)
	}
	if got := growRealtimeBackoff(2 * time.Second); got != 4*time.Second {
		t.Fatalf("expected doubled backoff 4s, got %v", got)
	}
}

func TestRequestRealtimeChatRefreshCoalesces(t *testing.T) {
	app := &App{realtimeRefreshCh: make(chan struct{}, 1)}
	app.requestRealtimeChatRefresh()
	app.requestRealtimeChatRefresh()

	select {
	case <-app.realtimeRefreshSignal():
	default:
		t.Fatal("expected a queued refresh signal")
	}

	select {
	case <-app.realtimeRefreshSignal():
		t.Fatal("expected at most one queued refresh signal")
	default:
	}
}

func TestNormalizeReactionMode(t *testing.T) {
	if got := normalizeReactionMode("eyes_reaction"); got != reactionModeEyes {
		t.Fatalf("expected %q, got %q", reactionModeEyes, got)
	}
	if got := normalizeReactionMode(" EYES_REACTION "); got != reactionModeEyes {
		t.Fatalf("expected normalized eyes mode, got %q", got)
	}
	if got := normalizeReactionMode("invalid"); got != reactionModeOff {
		t.Fatalf("expected fallback off mode, got %q", got)
	}
}

func TestURLTaskRetryBackoff(t *testing.T) {
	if got := urlTaskRetryBackoff(1); got != 5*time.Minute {
		t.Fatalf("attempt 1 backoff mismatch: %v", got)
	}
	if got := urlTaskRetryBackoff(3); got != 2*time.Hour {
		t.Fatalf("attempt 3 backoff mismatch: %v", got)
	}
	if got := urlTaskRetryBackoff(7); got != 7*24*time.Hour {
		t.Fatalf("attempt 7 backoff mismatch: %v", got)
	}
	if got := urlTaskRetryBackoff(99); got != 7*24*time.Hour {
		t.Fatalf("capped backoff mismatch: %v", got)
	}
}

func TestClassifyURLTaskError(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		terminal     bool
		expectPause  bool
		expectReason string
	}{
		{
			name:         "blocked",
			err:          urlfetch.ErrURLBlocked,
			terminal:     true,
			expectPause:  false,
			expectReason: "blocked",
		},
		{
			name:         "404",
			err:          &urlfetch.HTTPStatusError{StatusCode: 404, Status: "404 Not Found"},
			terminal:     true,
			expectPause:  false,
			expectReason: "http_404",
		},
		{
			name:         "429",
			err:          &urlfetch.HTTPStatusError{StatusCode: 429, Status: "429 Too Many Requests"},
			terminal:     false,
			expectPause:  true,
			expectReason: "http_429",
		},
		{
			name:         "500",
			err:          &urlfetch.HTTPStatusError{StatusCode: 500, Status: "500 Internal Server Error"},
			terminal:     false,
			expectPause:  true,
			expectReason: "http_500",
		},
		{
			name:         "deadline",
			err:          context.DeadlineExceeded,
			terminal:     false,
			expectPause:  false,
			expectReason: "timeout",
		},
		{
			name:         "unknown",
			err:          errors.New("boom"),
			terminal:     false,
			expectPause:  false,
			expectReason: "transient_error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := classifyURLTaskError(tc.err)
			if out.Terminal != tc.terminal {
				t.Fatalf("terminal mismatch: got=%v want=%v", out.Terminal, tc.terminal)
			}
			if (out.HostPause > 0) != tc.expectPause {
				t.Fatalf("host pause mismatch: got=%v want=%v", out.HostPause > 0, tc.expectPause)
			}
			if out.Reason != tc.expectReason {
				t.Fatalf("reason mismatch: got=%q want=%q", out.Reason, tc.expectReason)
			}
		})
	}
}
